package itdog

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var taskIDRegex = regexp.MustCompile(`var task_id='(.+)';`)

type Service struct {
	httpClient *http.Client
	wsDialer   *websocket.Dialer
	cookies    *cookieStore
}

type HTTPOptions struct {
	URL     string `json:"url"`
	Method  string `json:"method"`
	IPv4    string `json:"ipv4"`
	Cookies string `json:"cookies"`
	UA      string `json:"ua"`
	Referer string `json:"referer"`
	Host    string `json:"host"`
	HostS   string `json:"host_s"`
}

func NewService() *Service {
	return &Service{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		wsDialer:   websocket.DefaultDialer,
		cookies:    newCookieStore(),
	}
}

func (s *Service) Ping(ctx context.Context, host string) map[string]any {
	taskID := s.getTaskID(ctx, "https://www.itdog.cn/ping/"+host, url.Values{
		"line":         []string{""},
		"button_click": []string{"yes"},
	})
	if taskID == "" {
		return map[string]any{}
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
	return map[string]any{"results": results, "total": getStatisticians(stats)}
}

func (s *Service) TCPing(ctx context.Context, host string, port int) map[string]any {
	taskID := s.getTaskID(ctx, "https://www.itdog.cn/tcping/"+host+":"+intToString(port), url.Values{
		"line":         []string{""},
		"button_click": []string{"yes"},
	})
	if taskID == "" {
		return map[string]any{}
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
	return map[string]any{"results": results, "total": getStatisticians(stats)}
}

func (s *Service) HTTP(ctx context.Context, opt HTTPOptions) map[string]any {
	uri, err := url.Parse(opt.URL)
	if err != nil {
		return map[string]any{}
	}
	form := url.Values{}
	form.Set("url", opt.URL)
	if opt.Method != "" {
		form.Set("method", opt.Method)
	}
	if opt.IPv4 != "" {
		form.Set("ipv4", opt.IPv4)
	}
	if opt.Cookies != "" {
		form.Set("cookies", opt.Cookies)
	}
	if opt.UA != "" {
		form.Set("ua", opt.UA)
	}
	if opt.Referer != "" {
		form.Set("referer", opt.Referer)
	}
	form.Set("check_mode", "fast")
	form.Set("host", opt.URL)
	form.Set("host_s", uri.Host)

	taskID := s.getTaskID(ctx, "https://www.itdog.cn/http/", form)
	if taskID == "" {
		return map[string]any{}
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
			"redirect_time": formatDelay(m["redirect_time"]),
			"redirect":      stringFrom(m["redirect"]),
			"ssl_time":      formatDelay(m["ssl_time"]),
			"dns_time":      formatDelay(m["dns_time"]),
			"connect_time":  formatDelay(m["connect_time"]),
			"all_time":      formatDelay(m["all_time"]),
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
	return map[string]any{"results": results, "total": getStatisticians(stats)}
}

func (s *Service) DNS(ctx context.Context, domain, dnsType, server string) map[string]any {
	form := url.Values{}
	form.Set("domain", domain)
	form.Set("dns_type", strings.ToLower(dnsType))
	form.Set("dns_server", server)
	taskID := s.getTaskID(ctx, "https://www.itdog.cn/dns/"+domain, form)
	if taskID == "" {
		return map[string]any{}
	}
	raw, _ := s.sendTask(ctx, taskID, 1)
	results := make([]map[string]any, 0, len(raw))
	stats := make([]StatEntry, 0, len(raw))
	for _, m := range raw {
		name := stringFrom(m["name"])
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
	return map[string]any{"results": results, "total": getStatisticians(stats)}
}

func (s *Service) getTaskID(ctx context.Context, reqURL string, form url.Values) string {
	for i := 0; i < 6; i++ {
		taskID, retry := s.getTaskIDOnce(ctx, reqURL, form)
		if taskID != "" {
			return taskID
		}
		if !retry {
			return ""
		}
		time.Sleep(2 * time.Second)
	}
	return ""
}

func (s *Service) getTaskIDOnce(ctx context.Context, reqURL string, form url.Values) (string, bool) {
	body := bytes.NewBufferString(form.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, body)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/111.0.0.0 Safari/537.36 Edg/111.0.1661.44")
	req.Header.Set("origin", "https://www.itdog.cn")
	req.Header.Set("referer", reqURL)
	req.Header.Set("Content-type", "application/x-www-form-urlencoded")
	cookie := s.cookies.cookieString()
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		if s.cookies.len() == 0 {
			return "", true
		}
		return "", false
	}
	defer resp.Body.Close()

	for k, v := range parseSetCookies(resp.Header.Values("Set-Cookie")) {
		s.cookies.set(k, v)
	}
	data, _ := io.ReadAll(resp.Body)
	bodyText := string(data)
	if m := taskIDRegex.FindStringSubmatch(bodyText); len(m) == 2 {
		return m[1], false
	}

	if strings.Contains(bodyText, "/_guard/auto.js") || s.cookies.len() == 0 {
		s.cookies.clear()
		for k, v := range parseSetCookies(resp.Header.Values("Set-Cookie")) {
			s.cookies.set(k, v)
		}
		if s.cookies.get("guardok") != "" {
			return "", true
		}
		if guard := s.cookies.get("guard"); guard != "" {
			s.cookies.set("guardret", getGuardRet(guard))
			return "", true
		}
	}
	return "", false
}

func (s *Service) sendTask(ctx context.Context, taskID string, num int) ([]map[string]any, error) {
	if taskID == "" {
		return []map[string]any{}, nil
	}
	conn, _, err := s.wsDialer.DialContext(ctx, "wss://www.itdog.cn/websockets", nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	payload := map[string]string{"task_id": taskID, "task_token": getToken(taskID)}
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
	hardTimeout := time.NewTimer(30 * time.Second)
	defer hardTimeout.Stop()
	var debounce *time.Timer
	var debounceC <-chan time.Time

	for {
		select {
		case b := <-msgCh:
			item := map[string]any{}
			if err := json.Unmarshal(b, &item); err == nil {
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
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if ok {
		return s
	}
	return ""
}

func intToString(v int) string {
	return strconv.Itoa(v)
}
