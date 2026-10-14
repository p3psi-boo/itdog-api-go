package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"itdog-api-golang/internal/itdog"
)

type mockService struct {
	pingResp   map[string]any
	tcpingResp map[string]any
	httpResp   map[string]any
	dnsResp    map[string]any
	err        error
}

func (m *mockService) Ping(ctx context.Context, host string) (map[string]any, error) {
	return m.pingResp, m.err
}

func (m *mockService) TCPing(ctx context.Context, host string, port int) (map[string]any, error) {
	return m.tcpingResp, m.err
}

func (m *mockService) HTTP(ctx context.Context, opt itdog.HTTPOptions) (map[string]any, error) {
	return m.httpResp, m.err
}

func (m *mockService) DNS(ctx context.Context, domain, dnsType, server string) (map[string]any, error) {
	return m.dnsResp, m.err
}

func TestPingMissingHost(t *testing.T) {
	g := newTestRouter(&mockService{})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/it/ping", nil)
	g.ServeHTTP(w, req)

	assertEnvelope(t, w, http.StatusBadRequest, "不正确的主机名或IPV4地址！")
}

func TestTCPingInvalidPort(t *testing.T) {
	g := newTestRouter(&mockService{})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/it/tcping?host=127.0.0.1&port=70000", nil)
	g.ServeHTTP(w, req)

	assertEnvelope(t, w, http.StatusBadRequest, "无效的端口号！")
}

func TestHTTPInvalidMethod(t *testing.T) {
	g := newTestRouter(&mockService{})
	body := []byte(`{"url":"https://example.com","method":"put"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/it/http", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	g.ServeHTTP(w, req)

	assertEnvelope(t, w, http.StatusBadRequest, "无效的请求方式！")
}

func TestDNSInvalidType(t *testing.T) {
	g := newTestRouter(&mockService{})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/it/dns?domain=127.0.0.1&type=invalid", nil)
	g.ServeHTTP(w, req)

	assertEnvelope(t, w, http.StatusBadRequest, "无效的解析类型！")
}

func TestNoRouteWrapped(t *testing.T) {
	g := newTestRouter(&mockService{})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/not-found", nil)
	g.ServeHTTP(w, req)

	assertEnvelope(t, w, http.StatusNotFound, "Not Found")
}

func TestPingSuccessPayloadShape(t *testing.T) {
	resp := map[string]any{
		"results": []any{map[string]any{"ip": "1.1.1.1", "delay": "12ms", "node": "测试节点", "address": "北京"}},
		"total":   map[string]any{"全部": map[string]any{"nodes": 1}},
	}
	g := newTestRouter(&mockService{pingResp: resp})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/it/ping?host=127.0.0.1", nil)
	g.ServeHTTP(w, req)

	assertSuccessData(t, w, func(data map[string]any) {
		mustHas(t, data, "results")
		mustHas(t, data, "total")
	})
}

func TestTCPingSuccessPayloadShape(t *testing.T) {
	resp := map[string]any{
		"results": []any{map[string]any{"ip": "1.1.1.1", "delay": "12ms", "node": "测试节点", "address": "北京", "port": 443}},
		"total":   map[string]any{"全部": map[string]any{"nodes": 1}},
	}
	g := newTestRouter(&mockService{tcpingResp: resp})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/it/tcping?host=127.0.0.1&port=443", nil)
	g.ServeHTTP(w, req)

	assertSuccessData(t, w, func(data map[string]any) {
		mustHas(t, data, "results")
		mustHas(t, data, "total")
	})
}

func TestHTTPSuccessPayloadShape(t *testing.T) {
	resp := map[string]any{
		"results": []any{map[string]any{"address": "北京", "node": "测试节点", "ip": "1.1.1.1", "http_code": "200", "all_time": "20ms", "head": "h"}},
		"total":   map[string]any{"全部": map[string]any{"nodes": 1}},
	}
	g := newTestRouter(&mockService{httpResp: resp})
	body := []byte(`{"url":"https://example.com","method":"get"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/it/http", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	g.ServeHTTP(w, req)

	assertSuccessData(t, w, func(data map[string]any) {
		mustHas(t, data, "results")
		mustHas(t, data, "total")
	})
}

func TestDNSSuccessPayloadShape(t *testing.T) {
	resp := map[string]any{
		"results": []any{map[string]any{"node": "测试节点", "delay": "15ms", "result": []any{"1.1.1.1"}}},
		"total":   map[string]any{"全部": map[string]any{"nodes": 1}},
	}
	g := newTestRouter(&mockService{dnsResp: resp})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/it/dns?domain=127.0.0.1&type=a", nil)
	g.ServeHTTP(w, req)

	assertSuccessData(t, w, func(data map[string]any) {
		mustHas(t, data, "results")
		mustHas(t, data, "total")
	})
}

func newTestRouter(svc service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	return NewRouter(svc)
}

func assertEnvelope(t *testing.T, w *httptest.ResponseRecorder, code int, message string) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP status 200, got %d", w.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json response: %v", err)
	}
	if got := int(payload["code"].(float64)); got != code {
		t.Fatalf("expected code %d, got %d", code, got)
	}
	if got := payload["message"]; got != message {
		t.Fatalf("expected message %q, got %v", message, got)
	}
	if _, ok := payload["timestamp"]; !ok {
		t.Fatal("timestamp is missing")
	}
}

func assertSuccessData(t *testing.T, w *httptest.ResponseRecorder, check func(data map[string]any)) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP status 200, got %d", w.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json response: %v", err)
	}
	if got := int(payload["code"].(float64)); got != http.StatusOK {
		t.Fatalf("expected code 200, got %d", got)
	}
	if _, ok := payload["timestamp"]; !ok {
		t.Fatal("timestamp is missing")
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected object data, got %T", payload["data"])
	}
	check(data)
}

func mustHas(t *testing.T, data map[string]any, key string) {
	t.Helper()
	if _, ok := data[key]; !ok {
		t.Fatalf("expected key %q in data", key)
	}
}

func TestCaptchaTokenGuard(t *testing.T) {
	g := newTestRouter(&mockService{})
	// 未配置令牌时不限制
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/it/captcha/status", nil)
	g.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("未配置令牌时应放行，got %d", w.Code)
	}
}
