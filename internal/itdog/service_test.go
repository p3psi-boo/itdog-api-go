package itdog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func localService(t *testing.T, handler http.HandlerFunc, opts ...Option) *Service {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	svc := NewService(opts...)
	svc.baseURL = server.URL
	svc.wsBaseURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/"
	return svc
}

func TestMethodsPreservePartialResultsAndErrors(t *testing.T) {
	methods := map[string]func(*Service) (map[string]any, error){
		"ping": func(s *Service) (map[string]any, error) { return s.Ping(context.Background(), "fixture.invalid") },
		"tcping": func(s *Service) (map[string]any, error) {
			return s.TCPing(context.Background(), "fixture.invalid", 443)
		},
		"http": func(s *Service) (map[string]any, error) {
			return s.HTTP(context.Background(), HTTPOptions{URL: "https://fixture.invalid"})
		},
		"dns": func(s *Service) (map[string]any, error) {
			return s.DNS(context.Background(), "fixture.invalid", "txt", "127.0.0.1")
		},
	}
	for name, call := range methods {
		for _, complete := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/complete=%v", name, complete), func(t *testing.T) {
				svc := localService(t, func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.URL.Path == "/verify/clicaptcha.php":
						_, _ = io.WriteString(w, `{"type":"success"}`)
					case strings.HasPrefix(r.URL.Path, "/ws/"):
						up := websocket.Upgrader{}
						c, err := up.Upgrade(w, r, nil)
						if err != nil {
							return
						}
						defer c.Close()
						var req any
						if c.ReadJSON(&req) != nil {
							return
						}
						_ = c.WriteJSON(map[string]any{"type": "success", "name": "A", "result": 10, "time": 10, "all_time": 10, "line": 1})
						_ = c.WriteJSON(map[string]any{"type": "node_error", "name": "B", "line": 1})
						if complete {
							_ = c.WriteJSON(map[string]string{"type": "finished"})
						}
					default:
						_, _ = io.WriteString(w, `var task_id = 'task';`)
					}
				})
				data, err := call(svc)
				if (err == nil) != complete || data["complete"] != complete || data["partial"] == complete {
					t.Fatalf("%v %v", data, err)
				}
				results := data["results"].([]map[string]any)
				stats := data["total"].(map[string]any)["全部"].(*statBucket)
				if len(results) != 2 || stats.Nodes != 2 || stats.Success != 1 || stats.Failed != 1 || stats.Avg != "10.00" {
					t.Fatalf("%v %+v", results, stats)
				}
			})
		}
	}
}

func TestHTTPRedirectZeroAndDefault(t *testing.T) {
	zero := 0
	for _, tc := range []struct {
		input *int
		want  string
	}{{nil, "5"}, {&zero, "0"}} {
		got := make(chan string, 1)
		svc := localService(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/verify/") {
				_, _ = io.WriteString(w, `{"type":"success"}`)
				return
			}
			if r.URL.Path == "/http/" {
				_ = r.ParseForm()
				got <- r.FormValue("redirect_num")
				_, _ = io.WriteString(w, `var task_id='task';`)
				return
			}
			http.Error(w, "no websocket", 502)
		})
		_, _ = svc.HTTP(context.Background(), HTTPOptions{URL: "https://fixture.invalid", RedirectNum: tc.input})
		select {
		case value := <-got:
			if value != tc.want {
				t.Errorf("got %s want %s", value, tc.want)
			}
		case <-time.After(time.Second):
			t.Fatal("form not posted")
		}
	}
}

func TestPostTaskFormErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantID  string
		wantErr bool
	}{
		{"good", 200, `var task_id="abc";`, "abc", false},
		{"missing", 200, "no ID", "", false},
		{"upstream_failure", 503, `var task_id='fake';`, "", true},
		{"oversize", 200, strings.Repeat("a", (4<<20)+1), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := localService(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			id, err := svc.postTaskForm(context.Background(), "/ping/test", url.Values{})
			if id != tc.wantID || (err != nil) != tc.wantErr {
				t.Fatalf("%q %v", id, err)
			}
		})
	}
}

func TestVerificationBudgetAndNoneSolver(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantErr    bool
	}{
		{"verified_without_solver", `{"type":"success"}`, false},
		{"needs_solver", `{"type":"verify"}`, true},
		{"rejected", `{"type":"error","tip":"fixture"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := localService(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.body) })
			err := svc.ensureVerified(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatal(err)
			}
		})
	}
	svc := localService(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, WithLimits(Limits{VerifyTimeout: 20 * time.Millisecond}))
	if err := svc.ensureVerified(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost deadline: %v", err)
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failedReader) Close() error             { return nil }
func TestReadResponsePreservesReadError(t *testing.T) {
	_, err := readResponse(&http.Response{StatusCode: 200, Body: failedReader{}}, 100)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
}

func TestVerificationResetsFallbackCycle(t *testing.T) {
	vision := &fakeSolver{name: "vision", pts: [][2]int{{1, 1}, {2, 2}}}
	manual := &fakeSolver{name: "manual", pts: [][2]int{{3, 3}, {4, 4}}}
	solver := NewVisionFallbackSolver(vision, manual, 1)
	_, _ = solver.Solve(context.Background(), CaptchaChallenge{})
	if !solver.ManualPhase() {
		t.Fatal("fixture did not exhaust budget")
	}
	svc := localService(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"type":"success"}`) }, WithSolver(solver))
	if err := svc.ensureVerified(context.Background()); err != nil {
		t.Fatal(err)
	}
	if solver.ManualPhase() {
		t.Fatal("old manual phase carried into new cycle")
	}
	_, _ = solver.Solve(context.Background(), CaptchaChallenge{})
	if vision.calls != 2 || manual.calls != 0 {
		t.Fatalf("vision=%d manual=%d", vision.calls, manual.calls)
	}
}

func TestCreateTaskRetriesOnlyMissingID(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		wantCalls int
		wantErr   bool
	}{
		{"missing_then_present", 200, "", 2, false},
		{"non_2xx_not_retried", 503, `var task_id='fake';`, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			svc := localService(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/verify/") {
					_, _ = io.WriteString(w, `{"type":"success"}`)
					return
				}
				calls++
				w.WriteHeader(tc.status)
				if calls == 2 {
					_, _ = io.WriteString(w, `var task_id='task';`)
				} else {
					_, _ = io.WriteString(w, tc.body)
				}
			})
			id, err := svc.createTask(context.Background(), "/ping/fixture.invalid", url.Values{})
			if (err != nil) != tc.wantErr || calls != tc.wantCalls {
				t.Fatalf("id=%s err=%v calls=%d", id, err, calls)
			}
			if len(svc.taskGate) != 0 || len(svc.taskSlots) != 0 {
				t.Fatal("task creation leaked queue slots")
			}
		})
	}
}
