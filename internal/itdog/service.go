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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	baseURL       = "https://www.itdog.cn"
	wsBaseURL     = "wss://www.itdog.cn/websockets/"
	taskTokenSalt = "What this is is no longer important."
	uaString      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

var taskIDRegex = regexp.MustCompile(`var\s+task_id\s*=\s*['"]([^'"\r\n]+)['"]\s*;`)

type Service struct {
	httpClient *http.Client
	wsDialer   *websocket.Dialer
	solver     CaptchaSolver
	broker     *CaptchaBroker
	cookieFile string // 非空时把会话 cookie 持久化到磁盘

	taskGate  chan struct{} // Protects upstream session and task creation.
	taskSlots chan struct{} // Active creator plus bounded waiters.
	limits    Limits
	baseURL   string
	wsBaseURL string
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
	HTTPVersion   string `json:"http_version"`    // auto/http_1_1/http_2/http_3
	CheckMode     string `json:"check_mode"`      // fast/slow
	RedirectNum   *int   `json:"redirect_num"`    // 0~10
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
	dialer := *websocket.DefaultDialer
	s := &Service{
		httpClient: &http.Client{Timeout: 20 * time.Second, Jar: jar},
		wsDialer:   &dialer,
		limits:     DefaultLimits(),
		baseURL:    baseURL,
		wsBaseURL:  wsBaseURL,
		broker:     NewCaptchaBroker(),
	}
	for _, o := range opts {
		o(s)
	}
	s.limits = s.limits.withDefaults()
	s.taskGate = make(chan struct{}, 1)
	s.taskSlots = make(chan struct{}, s.limits.QueueCapacity+1)
	s.loadCookies()
	return s
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
	raw, taskErr := s.sendTask(ctx, taskID)
	results := make([]map[string]any, 0, len(raw))
	stats := make([]StatEntry, 0, len(raw))
	for _, m := range raw {
		delay, valid := nodeDelay(m, "result")
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
			"delay":   formatDelay(delay),
			"node":    name,
			"address": address,
		}
		results = append(results, result)
		stats = append(stats, StatEntry{
			Line:     intFrom(m["line"]),
			Region:   intFrom(m["region"]),
			Province: intFrom(m["province"]),
			Name:     name,
			Delay:    delay,
			Failed:   !valid,
		})
	}
	return taskResponse(results, stats, taskErr), taskErr
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
	raw, taskErr := s.sendTask(ctx, taskID)
	results := make([]map[string]any, 0, len(raw))
	stats := make([]StatEntry, 0, len(raw))
	for _, m := range raw {
		delay, valid := nodeDelay(m, "result")
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
			"delay":   formatDelay(delay),
			"node":    name,
			"address": address,
			"port":    port,
		})
		stats = append(stats, StatEntry{
			Line:     intFrom(m["line"]),
			Region:   intFrom(m["region"]),
			Province: intFrom(m["province"]),
			Name:     name,
			Delay:    delay,
			Failed:   !valid,
		})
	}
	return taskResponse(results, stats, taskErr), taskErr
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
	redirectNum := 5
	if opt.RedirectNum != nil {
		redirectNum = *opt.RedirectNum
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
	raw, taskErr := s.sendTask(ctx, taskID)
	results := make([]map[string]any, 0, len(raw))
	stats := make([]StatEntry, 0, len(raw))
	for _, m := range raw {
		delay, valid := nodeDelay(m, "all_time")
		if stringFrom(m["type"]) == "finished" {
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
			"redirect":      intFrom(m["redirect"]),
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
			Delay:    delay,
			Failed:   !valid,
		})
	}
	return taskResponse(results, stats, taskErr), taskErr
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
	raw, taskErr := s.sendTask(ctx, taskID)
	results := make([]map[string]any, 0, len(raw))
	stats := make([]StatEntry, 0, len(raw))
	for _, m := range raw {
		delay, valid := nodeDelay(m, "time")
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
				"delay":  formatDelay(delay),
				"result": m["result"],
			})
		}
		stats = append(stats, StatEntry{
			Line:     intFrom(m["line"]),
			Region:   intFrom(m["region"]),
			Province: intFrom(m["province"]),
			Name:     name,
			Delay:    delay,
			Failed:   !valid,
		})
	}
	return taskResponse(results, stats, taskErr), taskErr
}

// ---------------------------------------------------------------------------
// 任务创建：人机验证预检 -> 提交表单 -> 提取 task_id
// ---------------------------------------------------------------------------

// ensureVerified 遵循上游流程：先 GET /verify/clicaptcha.php?type=ajax 预检，
// 返回 verify 时解点选验证码并提交，直到会话通过验证。
// 在验证时间预算内重试；上游拒绝、未配置 solver 或请求取消时退出。
func (s *Service) ensureVerified(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.limits.VerifyTimeout)
	defer cancel()
	if resetter, ok := s.solver.(interface{ Reset() }); ok {
		resetter.Reset()
	}
	var lastErr error
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("验证超时或取消: %w", err)
		}
		if attempt > 1 {
			// 递增退避（1s → 最大 15s），避免频繁失败触发上游 bot_verify_limited
			select {
			case <-ctx.Done():
				if lastErr != nil {
					return fmt.Errorf("人机验证未通过（最后错误: %v）: %w", lastErr, ctx.Err())
				}
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff = min(backoff*3/2, 15*time.Second)
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
		case "verify":
			// Continue with the challenge below.
		default:
			return errors.New("上游返回未知的验证状态")
		}
		if s.solver == nil {
			return errors.New("上游需要人机验证，但未配置验证码 solver")
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
				return fmt.Errorf("人机验证未通过（最后错误: %v）: %w", err, ctx.Err())
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/verify/clicaptcha.php?type=ajax", nil)
	if err != nil {
		return captchaStatus{}, err
	}
	req.Header.Set("User-Agent", uaString)
	req.Header.Set("Referer", s.baseURL+"/ping/")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return captchaStatus{}, err
	}
	defer resp.Body.Close()
	data, err := readResponse(resp, 1<<20)
	if err != nil {
		return captchaStatus{}, err
	}
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/verify/clicaptcha.php?type=tool", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", uaString)
	req.Header.Set("Origin", s.baseURL)
	req.Header.Set("Referer", s.baseURL+"/verify/clicaptcha.php?type=ajax")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := readResponse(resp, 1<<16)
	if err != nil {
		return err
	}
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
	release, err := s.acquireTask(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	if err := s.ensureVerified(ctx); err != nil {
		return "", fmt.Errorf("人机验证未通过: %w", err)
	}
	id, err := s.postTaskForm(ctx, path, form)
	if err != nil {
		return "", err
	}
	if id != "" {
		return id, nil
	}
	// Only a valid HTML response without an ID may mean expired verification.
	if err := s.ensureVerified(ctx); err != nil {
		return "", fmt.Errorf("人机验证未通过: %w", err)
	}
	id, err = s.postTaskForm(ctx, path, form)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.New("上游未返回任务 ID")
	}
	return id, nil
}

func (s *Service) postTaskForm(ctx context.Context, path string, form url.Values) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+path, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", uaString)
	req.Header.Set("Origin", s.baseURL)
	req.Header.Set("Referer", s.baseURL+path)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("创建任务: %w", err)
	}
	defer resp.Body.Close()
	data, err := readResponse(resp, 4<<20)
	if err != nil {
		return "", fmt.Errorf("创建任务: %w", err)
	}
	if m := taskIDRegex.FindSubmatch(data); len(m) == 2 {
		return string(m[1]), nil
	}
	return "", nil
}

// readResponse never treats a transport error, non-2xx status or truncated body as success.
func readResponse(resp *http.Response, limit int64) ([]byte, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("上游 HTTP 状态 %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("读取上游响应: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, errors.New("上游响应超过大小限制")
	}
	return data, nil
}

func taskResponse(results []map[string]any, stats []StatEntry, err error) map[string]any {
	return map[string]any{"results": results, "total": getStatisticians(stats), "complete": err == nil, "partial": err != nil && len(results) > 0}
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
