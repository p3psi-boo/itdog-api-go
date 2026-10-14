package itdog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	baseURL      = "https://www.itdog.cn"
	wsBaseURL    = "wss://www.itdog.cn/websockets/"
	taskTokenSalt = "What this is is no longer important."
	uaString     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

var taskIDRegex = regexp.MustCompile(`var task_id='(.+)';`)

type Service struct {
	httpClient *http.Client
	wsDialer   *websocket.Dialer
	solver     CaptchaSolver
	broker     *CaptchaBroker
	cookieFile string // 非 nil 时把会话 cookie 持久化到磁盘

	taskMu sync.Mutex // 串行化任务创建，避免验证码/会话竞争
}

type HTTPOptions struct {
	URL           string `json:"url"`
	Method        string `json:"method"`
	IPv4          string `json:"ipv4"`
	Cookies       string `json:"cookies"`
	UA            string `json:"ua"`
	Referer       string `json:"referer"`
	Host          string `json:"host"`
	HostS         string `json:"host_s"`
	HTTPVersion   string `json:"http_version"`   // auto/http_1_1/http_2/http_3
	CheckMode     string `json:"check_mode"`     // fast/slow
	RedirectNum   int    `json:"redirect_num"`   // 0~10
	DNSServerType string `json:"dns_server_type"` // isp/custom
	DNSServer     string `json:"dns_server"`
	Line          string `json:"line"`
}

type Option func(*Service)

func WithSolver(s CaptchaSolver) Option {
	return func(svc *Service) { svc.solver = s }
}

func WithBroker(b *CaptchaBroker) Option {
	return func(svc *Service) {
		if b != nil {
			svc.broker = b
		}
	}
}

// WithCookieFile 启用会话持久化：验证通过的 cookie 落盘，重启后免验证码。
func WithCookieFile(path string) Option {
	return func(svc *Service) { svc.cookieFile = path }
}

func NewService(opts ...Option) *Service {
	jar, _ := cookiejar.New(nil)
	s := &Service{
		httpClient: &http.Client{Timeout: 20 * time.Second, Jar: jar},
		wsDialer:   websocket.DefaultDialer,
		broker:     NewCaptchaBroker(),
	}
	for _, o := range opts {
		o(s)
	}
	s.loadCookies()
	return s
}

const upstreamHost = "www.itdog.cn"

var upstreamURL = &url.URL{Scheme: "https", Host: upstreamHost, Path: "/"}

// saveCookies 把当前会话 cookie 写入磁盘（验证通过后调用，重启免验证）。
func (s *Service) saveCookies() {
	if s.cookieFile == "" {
		return
	}
	cookies := s.httpClient.Jar.Cookies(upstreamURL)
	if len(cookies) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.cookieFile), 0o755); err != nil {
		log.Printf("[itdog] 创建 cookie 目录失败: %v", err)
		return
	}
	data, err := json.MarshalIndent(cookies, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(s.cookieFile, data, 0o600); err != nil {
		log.Printf("[itdog] 写入 cookie 文件失败: %v", err)
		return
	}
	log.Printf("[itdog] 会话已持久化: %s (%d cookies)", s.cookieFile, len(cookies))
}

// loadCookies 启动时恢复上次的会话 cookie。
func (s *Service) loadCookies() {
	if s.cookieFile == "" {
		return
	}
	data, err := os.ReadFile(s.cookieFile)
	if err != nil {
		return
	}
	var cookies []*http.Cookie
	if err := json.Unmarshal(data, &cookies); err != nil {
		log.Printf("[itdog] cookie 文件损坏，忽略: %v", err)
		return
	}
	s.httpClient.Jar.SetCookies(upstreamURL, cookies)
	log.Printf("[itdog] 已恢复会话 cookie: %d 个", len(cookies))
}

// StartKeepalive 定时预检上游，保持已验证会话不过期（interval<=0 时不启用）。
func (s *Service) StartKeepalive(interval time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			st, err := s.precheckCaptcha(ctx)
			cancel()
			if err != nil {
				log.Printf("[itdog] 会话保活失败: %v", err)
				continue
			}
			switch st.Type {
			case "success":
				// 会话仍然有效
			case "error":
				log.Printf("[itdog] 会话保活被拒绝: %s", st.Tip)
			default:
				log.Printf("[itdog] 上游会话已失效，下次请求将需要人机验证")
			}
		}
	}()
}

// Captcha 暴露验证码 broker，供 HTTP API 转发人工解题。
func (s *Service) Captcha() *CaptchaBroker {
	return s.broker
}

func (s *Service) Ping(ctx context.Context, host string) (map[string]any, error) {
	taskID, err := s.createTask(ctx, "/ping/"+host, url.Values{
		"line":            []string{""},
		"button_click":    []string{"yes"},
		"dns_server_type": []string{"isp"},
		"dns_server":      []string{""},
	})
	if err != nil {
		return nil, err
	}
	raw, _ := s.sendTask(ctx, taskID, 1)
	results := make([]map[string]any, 0, len(raw))
	stats := make([]StatEntry, 0, len(raw))
	for _, m := range raw {
		name, _ := m["name"].(string)
		address, _ := m["address"].(string)
		if name == "" && address == "" {
			continue
		}
		if stringFrom(m["type"]) == "finished" {
			continue
		}
		result := map[string]any{
			"ip":      stringFrom(m["ip"]),
			"delay":   formatDelay(m["result"]),
			"node":    name,
			"address": address,
		}
		results = append(results, result)
		stats = append(stats, StatEntry{
			Line:     intFrom(m["line"]),
			Region:   intFrom(m["region"]),
			Province: intFrom(m["province"]),
			Name:     name,
			Delay:    numberFromAny(m["result"]),
		})
	}
	return map[string]any{"results": results, "total": getStatisticians(stats)}, nil
}

func (s *Service) TCPing(ctx context.Context, host string, port int) (map[string]any, error) {
	taskID, err := s.createTask(ctx, "/tcping/"+host+":"+intToString(port), url.Values{
		"line":            []string{""},
		"button_click":    []string{"yes"},
		"dns_server_type": []string{"isp"},
		"dns_server":      []string{""},
	})
	if err != nil {
		return nil, err
	}
	raw, _ := s.sendTask(ctx, taskID, 1)
	results := make([]map[string]any, 0, len(raw))
	stats := make([]StatEntry, 0, len(raw))
	for _, m := range raw {
		name, _ := m["name"].(string)
		address, _ := m["address"].(string)
		if name == "" && address == "" {
			continue
		}
		if stringFrom(m["type"]) == "finished" {
			continue
		}
		results = append(results, map[string]any{
			"ip":      stringFrom(m["ip"]),
			"delay":   formatDelay(m["result"]),
			"node":    name,
			"address": address,
			"port":    port,
		})
		stats = append(stats, StatEntry{
			Line:     intFrom(m["line"]),
			Region:   intFrom(m["region"]),
			Province: intFrom(m["province"]),
			Name:     name,
			Delay:    numberFromAny(m["result"]),
		})
	}
	return map[string]any{"results": results, "total": getStatisticians(stats)}, nil
}

func (s *Service) HTTP(ctx context.Context, opt HTTPOptions) (map[string]any, error) {
	uri, err := url.Parse(opt.URL)
	if err != nil {
		return nil, errors.New("无效的请求链接")
	}
	host := opt.Host
	if host == "" {
		host = opt.URL
	}
	hostS := opt.HostS
	if hostS == "" {
		hostS = uri.Host
	}
	method := opt.Method
	if method == "" {
		method = "get"
	}
	checkMode := opt.CheckMode
	if checkMode == "" {
		checkMode = "fast"
	}
	httpVersion := opt.HTTPVersion
	if httpVersion == "" {
		httpVersion = "auto"
	}
	redirectNum := opt.RedirectNum
	if redirectNum == 0 {
		redirectNum = 5
	}
	dnsType := opt.DNSServerType
	if dnsType == "" {
		dnsType = "isp"
	}

	form := url.Values{}
	form.Set("line", opt.Line)
	form.Set("host", host)
	form.Set("host_s", hostS)
	form.Set("check_mode", checkMode)
	form.Set("http_version", httpVersion)
	form.Set("ipv4", opt.IPv4)
	form.Set("method", method)
	form.Set("referer", opt.Referer)
	form.Set("ua", opt.UA)
	form.Set("cookies", opt.Cookies)
	form.Set("redirect_num", strconv.Itoa(redirectNum))
	form.Set("dns_server_type", dnsType)
	form.Set("dns_server", opt.DNSServer)

	taskID, err := s.createTask(ctx, "/http/", form)
	if err != nil {
		return nil, err
	}
	raw, _ := s.sendTask(ctx, taskID, 1)
	results := make([]map[string]any, 0, len(raw))
	stats := make([]StatEntry, 0, len(raw))
	for _, m := range raw {
		if stringFrom(m["type"]) != "success" {
			continue
		}
		head := strings.ReplaceAll(stringFrom(m["head"]), "<br>", "\n")
		name := stringFrom(m["name"])
		results = append(results, map[string]any{
			"address":       stringFrom(m["address"]),
			"node":          name,
			"ip":            stringFrom(m["ip"]),
			"http_code":     stringFrom(m["http_code"]),
			"redirect_time": stringFrom(m["redirect_time"]),
			"redirect":     intFrom(m["redirect"]),
			"download_time": stringFrom(m["download_time"]),
			"dns_time":      stringFrom(m["dns_time"]),
			"connect_time":  stringFrom(m["connect_time"]),
			"all_time":      stringFrom(m["all_time"]),
			"head":          head,
		})
		stats = append(stats, StatEntry{
			Line:     intFrom(m["line"]),
			Region:   intFrom(m["region"]),
			Province: intFrom(m["province"]),
			Name:     name,
			Delay:    numberFromAny(m["all_time"]),
		})
	}
	return map[string]any{"results": results, "total": getStatisticians(stats)}, nil
}

func (s *Service) DNS(ctx context.Context, domain, dnsType, server string) (map[string]any, error) {
	form := url.Values{}
	form.Set("line", "")
	form.Set("dns_type", strings.ToLower(dnsType))
	form.Set("dns_server_type", "custom")
	form.Set("dns_server", server)
	taskID, err := s.createTask(ctx, "/dns/"+domain, form)
	if err != nil {
		return nil, err
	}
	raw, _ := s.sendTask(ctx, taskID, 1)
	results := make([]map[string]any, 0, len(raw))
	stats := make([]StatEntry, 0, len(raw))
	for _, m := range raw {
		name := stringFrom(m["name"])
		if stringFrom(m["type"]) == "finished" {
			continue
		}
		if stringFrom(m["type"]) != "success" {
			results = append(results, map[string]any{
				"node":   name,
				"delay":  "-1ms",
				"result": m["result"],
			})
		} else {
			results = append(results, map[string]any{
				"node":   name,
				"delay":  formatDelay(m["time"]),
				"result": m["result"],
			})
		}
		stats = append(stats, StatEntry{
			Line:     intFrom(m["line"]),
			Region:   intFrom(m["region"]),
			Province: intFrom(m["province"]),
			Name:     name,
			Delay:    numberFromAny(m["time"]),
		})
	}
	return map[string]any{"results": results, "total": getStatisticians(stats)}, nil
}

// ---------------------------------------------------------------------------
// 任务创建：人机验证预检 -> 提交表单 -> 提取 task_id
// ---------------------------------------------------------------------------

// ensureVerified 遵循上游流程：先 GET /verify/clicaptcha.php?type=ajax 预检，
// 返回 verify 时解点选验证码并提交，直到会话通过验证。
// 无限重试：仅在上游明确拒绝（type=error）、未配置 solver 或 ctx 取消时退出。
func (s *Service) ensureVerified(ctx context.Context) error {
	if s.solver == nil {
		return errors.New("上游需要人机验证，但未配置验证码 solver")
	}
	var lastErr error
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			// 递增退避（1s → 最大 15s），避免频繁失败触发上游 bot_verify_limited
			select {
			case <-ctx.Done():
				if lastErr != nil {
					return fmt.Errorf("人机验证未通过（最后错误: %w）: %v", lastErr, ctx.Err())
				}
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff = backoff * 3 / 2
			}
		}
		st, err := s.precheckCaptcha(ctx)
		if err != nil {
			log.Printf("[itdog] 验证码预检失败: %v", err)
			lastErr = err
			continue
		}
		switch st.Type {
		case "success":
			s.saveCookies()
			return nil
		case "error":
			return fmt.Errorf("上游拒绝请求: %s", st.Tip)
		}
		log.Printf("[itdog] 需要人机验证（第 %d 次尝试，solver=%s）", attempt, solverName(s.solver))

		img, w, h, err := decodeCaptchaImage(st.Img)
		if err != nil {
			lastErr = err
			continue
		}
		sol, err := s.solver.Solve(ctx, CaptchaChallenge{Image: img, Width: w, Height: h, Attempt: attempt})
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("人机验证未通过（最后错误: %w）: %v", err, ctx.Err())
			}
			lastErr = fmt.Errorf("验证码求解失败: %w", err)
			continue
		}
		if err := s.submitCaptcha(ctx, w, h, sol.Points); err != nil {
			log.Printf("[itdog] 验证码提交失败: %v", err)
			lastErr = err
			continue
		}
		log.Printf("[itdog] 验证码校验已提交")
		s.saveCookies()
		// 提交成功后会话即通过验证，下一轮 precheck 会返回 success
	}
}

func (s *Service) precheckCaptcha(ctx context.Context) (captchaStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, captchaPrecheckURL, nil)
	if err != nil {
		return captchaStatus{}, err
	}
	req.Header.Set("User-Agent", uaString)
	req.Header.Set("Referer", baseURL+"/ping/")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return captchaStatus{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var st captchaStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return captchaStatus{}, fmt.Errorf("验证码预检响应解析失败: %s", truncate(string(data), 120))
	}
	return st, nil
}

func (s *Service) submitCaptcha(ctx context.Context, w, h int, pts [][2]int) error {
	if len(pts) != 2 {
		return errors.New("需要两个点击坐标")
	}
	form := url.Values{}
	form.Set("do", "check")
	form.Set("info", buildCaptchaInfo(w, h, pts))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, captchaCheckURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", uaString)
	req.Header.Set("Origin", baseURL)
	req.Header.Set("Referer", captchaPrecheckURL)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var res struct {
		Status int    `json:"status"`
		Token  string `json:"token"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return fmt.Errorf("验证码提交响应解析失败: %s", truncate(string(data), 120))
	}
	if res.Status != 1 {
		return errors.New("验证码校验未通过")
	}
	return nil
}

// createTask 先确保会话已通过人机验证，再提交表单创建任务并提取 task_id。
func (s *Service) createTask(ctx context.Context, path string, form url.Values) (string, error) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()

	if err := s.ensureVerified(ctx); err != nil {
		return "", fmt.Errorf("人机验证未通过: %w", err)
	}
	if id := s.postTaskForm(ctx, path, form); id != "" {
		log.Printf("[itdog] 任务已创建: %s", id)
		return id, nil
	}
	log.Printf("[itdog] 任务创建失败（未找到 task_id），重试")
	// 会话可能刚好过期：再做一次验证并重试
	if err := s.ensureVerified(ctx); err != nil {
		return "", fmt.Errorf("人机验证未通过: %w", err)
	}
	id := s.postTaskForm(ctx, path, form)
	if id == "" {
		return "", errors.New("上游未返回任务 ID（可能被限流）")
	}
	return id, nil
}

func (s *Service) postTaskForm(ctx context.Context, path string, form url.Values) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", uaString)
	req.Header.Set("Origin", baseURL)
	req.Header.Set("Referer", baseURL+path)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if m := taskIDRegex.FindStringSubmatch(string(data)); len(m) == 2 {
		return m[1]
	}
	return ""
}

// ---------------------------------------------------------------------------
// WebSocket 结果通道
// 新协议：wss://www.itdog.cn/websockets/<task_id>/<token>
// token = md5(task_id + salt) 的第 8..24 位十六进制字符
// ---------------------------------------------------------------------------

func (s *Service) sendTask(ctx context.Context, taskID string, num int) ([]map[string]any, error) {
	if taskID == "" {
		return []map[string]any{}, nil
	}
	wsURL := wsBaseURL + taskID + "/" + getToken(taskID)
	log.Printf("[itdog] 连接结果通道: %s", wsURL)
	conn, _, err := s.wsDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	payload := map[string]string{"task_id": taskID}
	if err := conn.WriteJSON(payload); err != nil {
		return nil, err
	}
	for i := 1; i < num; i++ {
		time.Sleep(500 * time.Millisecond)
		if err := conn.WriteJSON(payload); err != nil {
			break
		}
	}

	msgCh := make(chan []byte)
	errCh := make(chan error, 1)
	go func() {
		for {
			_, b, e := conn.ReadMessage()
			if e != nil {
				errCh <- e
				return
			}
			msgCh <- b
		}
	}()

	results := make([]map[string]any, 0, 32)
	hardTimeout := time.NewTimer(45 * time.Second)
	defer hardTimeout.Stop()
	var debounce *time.Timer
	var debounceC <-chan time.Time

	for {
		select {
		case b := <-msgCh:
			item := map[string]any{}
			if err := json.Unmarshal(b, &item); err == nil {
				if stringFrom(item["type"]) == "finished" {
					return results, nil
				}
				results = append(results, item)
			}
			if debounce == nil {
				debounce = time.NewTimer(5555 * time.Millisecond)
				debounceC = debounce.C
			} else {
				if !debounce.Stop() {
					select {
					case <-debounce.C:
					default:
					}
				}
				debounce.Reset(5555 * time.Millisecond)
			}
		case <-debounceC:
			return results, nil
		case <-hardTimeout.C:
			return results, nil
		case <-ctx.Done():
			return results, ctx.Err()
		case <-errCh:
			return results, nil
		}
	}
}

func intFrom(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		n, _ := strconv.Atoi(x)
		return n
	default:
		return 0
	}
}

func stringFrom(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		if x == math.Trunc(x) {
			return strconv.Itoa(int(x))
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", x)
	}
}

func intToString(v int) string {
	return strconv.Itoa(v)
}
