package itdog

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CaptchaChallenge 是一次点选验证码挑战。
// 图片为 JPEG 原始字节，顶部为提示文字（请按顺序点击 N 个字），下方散布着待点击的文字。
type CaptchaChallenge struct {
	Image   []byte
	Width   int
	Height  int
	Attempt int // 第几次尝试（从 1 开始）
}

// CaptchaSolution 是按顺序点击的坐标（图片原始像素坐标）。
type CaptchaSolution struct {
	Points [][2]int `json:"points"`
}

// CaptchaSolver 解决一次验证码挑战。
type CaptchaSolver interface {
	Solve(ctx context.Context, ch CaptchaChallenge) (CaptchaSolution, error)
	Name() string
}

// ---------------------------------------------------------------------------
// Broker：把挑战转交给外部（HTTP API /it/captcha）并等待人工提交答案
// ---------------------------------------------------------------------------

type pendingChallenge struct {
	ch      CaptchaChallenge
	created time.Time
	version int64 // 当前进程内单调递增，用于防止把旧答案提交到新挑战
}

// CaptchaBroker 持有待解决的验证码挑战，供人工通过 HTTP API 处理。
type CaptchaBroker struct {
	mu        sync.Mutex
	pending   *pendingChallenge
	solCh     chan CaptchaSolution
	resolved  int
	version   int64
	submitted bool
}

func NewCaptchaBroker() *CaptchaBroker {
	return &CaptchaBroker{}
}

// Publish 发布挑战并阻塞等待答案（或 ctx 取消）。
func (b *CaptchaBroker) Publish(ctx context.Context, ch CaptchaChallenge) (CaptchaSolution, error) {
	if err := ctx.Err(); err != nil {
		return CaptchaSolution{}, err
	}
	b.mu.Lock()
	if b.pending != nil {
		b.mu.Unlock()
		return CaptchaSolution{}, errors.New("已有待处理的验证码")
	}
	b.version = max(b.version+1, time.Now().UnixMilli())
	ch.Image = append([]byte(nil), ch.Image...)
	b.pending = &pendingChallenge{ch: ch, created: time.Now(), version: b.version}
	b.submitted = false
	b.solCh = make(chan CaptchaSolution, 1)
	solCh := b.solCh
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		b.pending = nil
		b.solCh = nil
		b.mu.Unlock()
	}()

	select {
	case sol := <-solCh:
		if len(sol.Points) != 2 {
			return CaptchaSolution{}, errors.New("需要两个点击坐标")
		}
		return sol, nil
	case <-ctx.Done():
		return CaptchaSolution{}, ctx.Err()
	}
}

// Pending 返回当前待解决的挑战及其版本号。
func (b *CaptchaBroker) Pending() (CaptchaChallenge, int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil {
		return CaptchaChallenge{}, 0, false
	}
	ch := b.pending.ch
	ch.Image = append([]byte(nil), ch.Image...)
	return ch, b.pending.version, true
}

// Submit 提交人工答案；version 不匹配说明挑战已刷新。
func (b *CaptchaBroker) Submit(sol CaptchaSolution, version int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil {
		return errors.New("当前没有需要处理的验证码")
	}
	if version != b.pending.version {
		return errChallengeRefreshed
	}
	if len(sol.Points) != 2 {
		return errors.New("需要两个点击坐标")
	}
	for _, p := range sol.Points {
		if p[0] < 0 || p[0] >= b.pending.ch.Width || p[1] < 0 || p[1] >= b.pending.ch.Height {
			return errors.New("坐标超出图片范围")
		}
	}
	if b.submitted {
		return errors.New("验证码答案已在处理中")
	}
	sol.Points = append([][2]int(nil), sol.Points...)
	select {
	case b.solCh <- sol:
		b.resolved++
		b.submitted = true
		return nil
	default:
		return errors.New("验证码答案已在处理中")
	}
}

var errChallengeRefreshed = errors.New("验证码已刷新，请重新获取")

// ManualSolver 通过 Broker 等待人工解题。
type ManualSolver struct {
	Broker    *CaptchaBroker
	WaitLimit time.Duration
}

func (m *ManualSolver) Name() string { return "manual" }

func (m *ManualSolver) Solve(ctx context.Context, ch CaptchaChallenge) (CaptchaSolution, error) {
	if m.Broker == nil {
		return CaptchaSolution{}, errors.New("manual solver 未配置 broker")
	}
	if m.WaitLimit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.WaitLimit)
		defer cancel()
	}
	return m.Broker.Publish(ctx, ch)
}

// ---------------------------------------------------------------------------
// VisionSolver：调用 OpenAI 兼容的视觉模型识别验证码
// ---------------------------------------------------------------------------

type VisionSolver struct {
	BaseURL    string // 例如 https://api.openai.com/v1
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

func (v *VisionSolver) Name() string { return "vision" }

var (
	jsonBlockRe = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")
	coordPairRe = regexp.MustCompile(`(?i)\{\s*"x"\s*:\s*(\d+)\s*,\s*"y"\s*:\s*(\d+)\s*\}`)
)

func (v *VisionSolver) Solve(ctx context.Context, ch CaptchaChallenge) (CaptchaSolution, error) {
	if v.BaseURL == "" || v.Model == "" {
		return CaptchaSolution{}, errors.New("vision solver 未配置")
	}
	client := v.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	imgB64 := base64.StdEncoding.EncodeToString(ch.Image)
	prompt := fmt.Sprintf(
		"这是一个中文点选验证码，图片尺寸 %dx%d 像素。图片顶部有一行提示文字，其中指出了需要按顺序点击的 2 个汉字（提示文字本身除外）。"+
			"请找出这 2 个汉字散布在图片主体区域中的位置（每个字点击它的中心；不要点击顶部提示区里的字）。"+
			"只返回 JSON，格式：{\"points\":[{\"x\":<像素x>,\"y\":<像素y>},{\"x\":<像素x>,\"y\":<像素y>}]}，按点击顺序排列。",
		ch.Width, ch.Height)
	body := map[string]any{
		"model":       v.Model,
		"temperature": 0,
		"max_tokens":  4000, // reasoning 模型的思考过程也计入，需要余量
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": prompt},
					{"type": "image_url", "image_url": map[string]string{"url": "data:image/jpeg;base64," + imgB64}},
				},
			},
		},
	}
	raw, _ := json.Marshal(body)
	url := strings.TrimRight(v.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return CaptchaSolution{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if v.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+v.APIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return CaptchaSolution{}, err
	}
	defer resp.Body.Close()
	data, err := readResponse(resp, 1<<20)
	if err != nil {
		return CaptchaSolution{}, fmt.Errorf("vision API: %w", err)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil || len(parsed.Choices) == 0 {
		return CaptchaSolution{}, errors.New("vision API 响应解析失败")
	}
	content := parsed.Choices[0].Message.Content
	pts := parseVisionPoints(content)
	if len(pts) != 2 {
		return CaptchaSolution{}, fmt.Errorf("vision 模型未返回两个坐标: %s", truncate(content, 120))
	}
	for _, p := range pts {
		if p[0] < 0 || p[0] >= ch.Width || p[1] < 0 || p[1] >= ch.Height {
			return CaptchaSolution{}, fmt.Errorf("vision 坐标超出图片范围: %v", p)
		}
	}
	return CaptchaSolution{Points: pts}, nil
}

func parseVisionPoints(content string) [][2]int {
	find := func(s string) [][2]int {
		var out [][2]int
		for _, m := range coordPairRe.FindAllStringSubmatch(s, -1) {
			x, _ := strconv.Atoi(m[1])
			y, _ := strconv.Atoi(m[2])
			out = append(out, [2]int{x, y})
		}
		return out
	}
	if m := jsonBlockRe.FindStringSubmatch(content); len(m) == 2 {
		if pts := find(m[1]); len(pts) == 2 {
			return pts
		}
	}
	if i := strings.Index(content, "{"); i >= 0 {
		if j := strings.LastIndex(content, "}"); j > i {
			if pts := find(content[i : j+1]); len(pts) == 2 {
				return pts
			}
		}
	}
	return find(content)
}

// ---------------------------------------------------------------------------
// ChainSolver：按顺序尝试多个 solver
// ---------------------------------------------------------------------------

type ChainSolver struct {
	Solvers []CaptchaSolver
}

func (c *ChainSolver) Name() string {
	names := make([]string, 0, len(c.Solvers))
	for _, s := range c.Solvers {
		names = append(names, s.Name())
	}
	return "chain(" + strings.Join(names, ",") + ")"
}

func (c *ChainSolver) Solve(ctx context.Context, ch CaptchaChallenge) (CaptchaSolution, error) {
	var errs []string
	for _, s := range c.Solvers {
		if err := ctx.Err(); err != nil {
			return CaptchaSolution{}, err
		}
		sol, err := s.Solve(ctx, ch)
		if err == nil {
			return sol, nil
		}
		errs = append(errs, s.Name()+": "+err.Error())
	}
	return CaptchaSolution{}, errors.New(strings.Join(errs, "; "))
}

// ---------------------------------------------------------------------------
// 验证码协议：获取挑战 / 提交答案
// ---------------------------------------------------------------------------

type captchaStatus struct {
	Type string `json:"type"` // success / verify / error
	Tip  string `json:"tip"`
	Img  string `json:"img"` // data:image/jpeg;base64,...
}

// buildCaptchaPayload 构造行为轨迹 payload（与前端 clicaptcha-plugin.js 一致，
// 服务端仅做弱校验：点击坐标、顺序、接近目标字形位置）。
func buildCaptchaPayload(pts [][2]int) string {
	now := time.Now().UnixMilli()
	clicks := make([]map[string]any, 0, len(pts))
	for i, p := range pts {
		down := now - 400 + int64(i*220)
		up := down + 70 + int64(i*15)
		clicks = append(clicks, map[string]any{
			"x":       p[0],
			"y":       p[1],
			"down":    down - (now - 2500),
			"up":      up - (now - 2500),
			"hold":    up - down,
			"button":  0,
			"trusted": 1,
			"pointer": "mouse",
		})
	}
	payload := map[string]any{
		"v":         2,
		"loadTs":    now - 2500,
		"submitTs":  now,
		"moves":     8 + len(pts)*3,
		"travel":    150 + len(pts)*40,
		"visible":   "visible",
		"webdriver": false,
		"dpr":       1,
		"clicks":    clicks,
	}
	raw, _ := json.Marshal(payload)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// buildCaptchaInfo 构造 do=check 的 info 字段：`x,y-x,y;宽;高;payload`。
func buildCaptchaInfo(w, h int, pts [][2]int) string {
	parts := make([]string, 0, len(pts))
	for _, p := range pts {
		parts = append(parts, fmt.Sprintf("%d,%d", p[0], p[1]))
	}
	return fmt.Sprintf("%s;%d;%d;%s", strings.Join(parts, "-"), w, h, buildCaptchaPayload(pts))
}

func decodeCaptchaImage(imgData string) ([]byte, int, int, error) {
	comma := strings.Index(imgData, ",")
	if comma < 0 {
		return nil, 0, 0, errors.New("验证码图片格式错误")
	}
	raw, err := base64.StdEncoding.DecodeString(imgData[comma+1:])
	if err != nil {
		return nil, 0, 0, err
	}
	w, h := jpegSize(raw)
	if w <= 0 || h <= 0 {
		return nil, 0, 0, errors.New("无效的 JPEG 验证码图片")
	}
	return raw, w, h, nil
}

// jpegSize uses the standard decoder rather than trusting marker offsets.
func jpegSize(data []byte) (int, int) {
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return config.Width, config.Height
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func solverName(s CaptchaSolver) string {
	if s == nil {
		return "none"
	}
	return s.Name()
}

// ErrChallengeRefreshed 表示提交时挑战已被新挑战替换。
var ErrChallengeRefreshed = errChallengeRefreshed

// ---------------------------------------------------------------------------
// VisionFallbackSolver：方案 C 的降级策略
// 验证码出现时先用视觉模型连续尝试（识别失败或提交答错都算消耗一次），
// 超过 maxVision 次后进入人工通道（/it/captcha 页面），不再调用视觉模型。
// ---------------------------------------------------------------------------

type VisionFallbackSolver struct {
	Vision    CaptchaSolver
	Manual    CaptchaSolver
	MaxVision int // 视觉模型最大尝试次数；<=0 表示无限重试（不降级人工）

	mu          sync.Mutex
	visionCalls int  // 视觉阶段已消耗的挑战数
	manualPhase bool // 是否已进入人工阶段（仅配置了上限时可达）
}

func NewVisionFallbackSolver(vision, manual CaptchaSolver, maxVision int) *VisionFallbackSolver {
	return &VisionFallbackSolver{Vision: vision, Manual: manual, MaxVision: maxVision}
}

func (s *VisionFallbackSolver) Name() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.manualPhase {
		return "vision->manual(人工阶段)"
	}
	if s.MaxVision <= 0 {
		return fmt.Sprintf("vision(%d/∞)", s.visionCalls)
	}
	return fmt.Sprintf("vision(%d/%d)", s.visionCalls, s.MaxVision)
}

// ManualPhase 返回是否已降级到人工通道。
func (s *VisionFallbackSolver) ManualPhase() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manualPhase
}

// Reset starts a new verification cycle. Service calls it while holding the
// session gate, so a solved challenge never consumes the next cycle's budget.
func (s *VisionFallbackSolver) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.visionCalls = 0
	s.manualPhase = false
}

func (s *VisionFallbackSolver) Solve(ctx context.Context, ch CaptchaChallenge) (CaptchaSolution, error) {
	if err := ctx.Err(); err != nil {
		return CaptchaSolution{}, err
	}
	s.mu.Lock()
	if s.manualPhase || s.MaxVision > 0 && s.visionCalls >= s.MaxVision {
		s.manualPhase = true
		s.mu.Unlock()
		if s.Manual == nil {
			return CaptchaSolution{}, errors.New("manual solver 未配置")
		}
		return s.Manual.Solve(ctx, ch)
	}
	s.visionCalls++
	attempt := s.visionCalls
	// Count every invocation, including well-formed coordinates rejected later
	// by the upstream verifier. The next challenge uses the manual channel.
	if s.MaxVision > 0 && attempt >= s.MaxVision {
		s.manualPhase = true
	}
	s.mu.Unlock()
	if s.Vision == nil {
		return CaptchaSolution{}, errors.New("vision solver 未配置")
	}
	sol, err := s.Vision.Solve(ctx, ch)
	if err != nil {
		return CaptchaSolution{}, fmt.Errorf("vision 第 %d 次尝试失败: %w", attempt, err)
	}
	return sol, nil
}
