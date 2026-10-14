package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"itdog-api-golang/internal/itdog"
)

type captchaMock struct {
	mockService
	broker *itdog.CaptchaBroker
}

func (m *captchaMock) Captcha() *itdog.CaptchaBroker { return m.broker }

func TestCaptchaTokenGuard(t *testing.T) {
	for _, tc := range []struct {
		name, token, query, header string
		status                     int
	}{
		{"unconfigured", "", "", "", 200},
		{"missing", "secret", "", "", 403},
		{"wrong", "secret", "wrong", "", 403},
		{"header", "secret", "", "secret", 200},
		{"query", "secret", "secret", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ITDOG_CAPTCHA_TOKEN", tc.token)
			g := newTestRouter(&captchaMock{broker: itdog.NewCaptchaBroker()})
			for _, endpoint := range []struct{ method, path, body string }{{"GET", "/it/captcha", ""}, {"GET", "/it/captcha/status", ""}, {"POST", "/it/captcha", `{"points":[[1,1],[2,2]],"version":1}`}} {
				target := endpoint.path
				if tc.query != "" {
					target += "?token=" + tc.query
				}
				r := httptest.NewRequest(endpoint.method, target, strings.NewReader(endpoint.body))
				r.Header.Set("X-Captcha-Token", tc.header)
				w := httptest.NewRecorder()
				g.ServeHTTP(w, r)
				want := tc.status
				// Authorized POST reaches the broker, which has no pending challenge.
				if endpoint.method == "POST" && want == 200 {
					want = 400
				}
				if w.Code != want {
					t.Fatalf("%s: %d %s", endpoint.path, w.Code, w.Body)
				}
				if endpoint.path == "/it/captcha/status" && want == 200 {
					var envelope struct {
						Code int
						Data struct{ Pending bool }
					}
					if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || envelope.Code != 200 || envelope.Data.Pending {
						t.Fatalf("wrong endpoint response: %s", w.Body)
					}
				}
			}
		})
	}
}

func TestNoMethodWrapped(t *testing.T) {
	g := newTestRouter(&mockService{})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("POST", "/it/ping", nil))
	assertEnvelope(t, w, 405, "Method Not Allowed")
}

func TestServiceFailureStatusAndPartialData(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{
		{errors.New("upstream"), 502}, {fmt.Errorf("wrapped: %w", context.DeadlineExceeded), 504}, {context.Canceled, 499}, {itdog.ErrQueueFull, 503}, {itdog.ErrQueueTimeout, 503},
	} {
		t.Run(fmt.Sprint(tc.code, tc.err), func(t *testing.T) {
			partial := map[string]any{"complete": false, "partial": true, "results": []any{map[string]any{"node": "A"}}}
			g := newTestRouter(&mockService{err: tc.err, pingResp: partial})
			w := httptest.NewRecorder()
			g.ServeHTTP(w, httptest.NewRequest("GET", "/it/ping?host=fixture.invalid", nil))
			assertEnvelope(t, w, tc.code, tc.err.Error())
			var env struct {
				Data struct {
					Complete, Partial bool
					Results           []any
				}
			}
			_ = json.Unmarshal(w.Body.Bytes(), &env)
			if env.Data.Complete || !env.Data.Partial || len(env.Data.Results) != 1 {
				t.Fatalf("lost partial data: %s", w.Body)
			}
			if tc.code == 503 && w.Header().Get("Retry-After") == "" {
				t.Fatal("missing retry hint")
			}
		})
	}
}

func TestRequestLogRedactionAndRecovery(t *testing.T) {
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	t.Setenv("ITDOG_CAPTCHA_TOKEN", "secret-token")
	g := newTestRouter(&captchaMock{broker: itdog.NewCaptchaBroker()})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("GET", "/it/captcha/status?token=secret-token", nil))
	id := w.Header().Get("X-Request-ID")
	if id == "" || !strings.Contains(logs.String(), id) || !strings.Contains(logs.String(), "status=200") || !strings.Contains(logs.String(), "duration=") {
		t.Fatalf("missing request fields: %s", &logs)
	}
	if strings.Contains(logs.String(), "secret-token") {
		t.Fatal("token leaked")
	}
	g.GET("/panic", func(c *gin.Context) { panic("secret-panic") })
	w = httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("GET", "/panic", nil))
	assertEnvelope(t, w, 500, "Internal Server Error")
	if !strings.Contains(logs.String(), "stack=") || !strings.Contains(logs.String(), "status=500") {
		t.Fatal("panic not logged")
	}
	if strings.Contains(logs.String(), "secret-panic") {
		t.Fatal("panic value leaked")
	}
}

type waitingService struct{ mockService }

func (*waitingService) Ping(ctx context.Context, _ string) (map[string]any, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestRequestDeadline(t *testing.T) {
	g := NewRouter(&waitingService{}, WithRequestTimeout(20*time.Millisecond))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("GET", "/it/ping?host=fixture.invalid", nil))
	if w.Code != 504 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestOversizedBody(t *testing.T) {
	g := newTestRouter(&mockService{})
	w := httptest.NewRecorder()
	body := `{"url":"https://fixture.invalid","cookies":"` + strings.Repeat("x", 129<<10) + `"}`
	g.ServeHTTP(w, httptest.NewRequest("POST", "/it/http", strings.NewReader(body)))
	if w.Code != 413 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestHTTPOptionsValidation(t *testing.T) {
	for _, fields := range []string{
		`"redirect_num":-1`, `"redirect_num":11`, `"http_version":"bad"`, `"check_mode":"bad"`, `"dns_server_type":"bad"`, `"dns_server_type":"custom"`, `"dns_server":"not-an-IP"`, `"ipv4":"::1"`,
	} {
		t.Run(fields, func(t *testing.T) {
			g := newTestRouter(&mockService{})
			w := httptest.NewRecorder()
			g.ServeHTTP(w, httptest.NewRequest("POST", "/it/http", strings.NewReader(`{"url":"https://fixture.invalid",`+fields+`}`)))
			if w.Code != 400 {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
		})
	}
	for _, raw := range []string{`{"url":"https://fixture.invalid","redirect_num":0}`, `{"url":"https://fixture.invalid","dns_server_type":"custom","dns_server":"127.0.0.1","http_version":"http_2","check_mode":"slow","ipv4":"1.1.1.1"}`} {
		w := httptest.NewRecorder()
		newTestRouter(&mockService{}).ServeHTTP(w, httptest.NewRequest("POST", "/it/http", strings.NewReader(raw)))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
}

func TestNameValidationWithoutDNS(t *testing.T) {
	for _, name := range []string{"fixture.invalid", "txt-only.invalid.", "127.0.0.1", "localhost"} {
		if !validHost(name) {
			t.Errorf("valid host rejected: %s", name)
		}
	}
	for _, name := range []string{"", "a..invalid", "-a.invalid", "a-.invalid", "a/b", "a:80", "::1", strings.Repeat("a", 64) + ".invalid"} {
		if validHost(name) {
			t.Errorf("invalid host accepted: %s", name)
		}
	}
	if !validDNSName("_sip._tcp.fixture.invalid") {
		t.Fatal("SRV name rejected")
	}
	g := newTestRouter(&mockService{dnsResp: map[string]any{"results": []any{}}})
	for _, target := range []string{"/it/dns?domain=txt-only.invalid&type=txt", "/it/dns?domain=_sip._tcp.fixture.invalid&type=srv"} {
		w := httptest.NewRecorder()
		g.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", target, w.Code, w.Body)
		}
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("GET", "/it/dns?domain=fixture.invalid&type=a&server=bad", nil))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestURLValidation(t *testing.T) {
	for _, raw := range []string{"https://fixture.invalid", "http://127.0.0.1:8080/a", "https://[::1]/"} {
		if !validURL(raw) {
			t.Errorf("valid URL rejected: %s", raw)
		}
	}
	for _, raw := range []string{"ftp://fixture.invalid", "file:///tmp/test", "https://", "//fixture.invalid", "http://fixture.invalid:0", "http://fixture.invalid:65536", "http://fixture.invalid:"} {
		if validURL(raw) {
			t.Errorf("invalid URL accepted: %s", raw)
		}
	}
}

func TestCaptchaPageCanReloadWithoutQueryToken(t *testing.T) {
	t.Setenv("ITDOG_CAPTCHA_TOKEN", "secret-token")
	g := newTestRouter(&captchaMock{broker: itdog.NewCaptchaBroker()})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("GET", "/it/captcha?token=secret-token", nil))
	cookies := w.Result().Cookies()
	if w.Code != 200 || len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].Path != "/it/captcha" {
		t.Fatalf("cookie missing: %v", cookies)
	}
	for _, path := range []string{"/it/captcha", "/it/captcha/status"} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookies[0])
		w = httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("reload %s: %d", path, w.Code)
		}
	}
}

func TestJSONTrailingContent(t *testing.T) {
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"url":"https://fixture.invalid"}` + `{}`, 400},
		{`{"url":"https://fixture.invalid"}` + strings.Repeat(" ", 129<<10), 413},
		{`{"url":"https://fixture.invalid"}` + " \n", 200},
	} {
		w := httptest.NewRecorder()
		newTestRouter(&mockService{}).ServeHTTP(w, httptest.NewRequest("POST", "/it/http", strings.NewReader(tc.body)))
		if w.Code != tc.want {
			t.Fatalf("got %d want %d", w.Code, tc.want)
		}
	}
}
