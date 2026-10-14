package itdog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func websocketService(t *testing.T, handler func(*websocket.Conn), limits Limits) *Service {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var request map[string]string
		if err := conn.ReadJSON(&request); err != nil {
			return
		}
		if request["task_id"] != "task" {
			t.Errorf("unexpected task: %v", request)
		}
		handler(conn)
	}))
	t.Cleanup(server.Close)
	svc := NewService(WithLimits(limits))
	svc.wsBaseURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	return svc
}

func TestWebsocketCompleteAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name, tail string
		wantError  bool
	}{
		{"complete", `{"type":"finished"}`, false},
		{"disconnect", "", true},
		{"malformed", "{", true},
		{"null", "null", true},
		{"clean_close_without_finished", "close", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := websocketService(t, func(c *websocket.Conn) {
				_ = c.WriteJSON(map[string]any{"type": "success", "name": "A", "result": 10})
				if tc.tail == "close" {
					_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				} else if tc.tail != "" {
					_ = c.WriteMessage(websocket.TextMessage, []byte(tc.tail))
				}
			}, Limits{})
			results, err := svc.sendTask(context.Background(), "task")
			if (err != nil) != tc.wantError || len(results) != 1 {
				t.Fatalf("results=%v err=%v", results, err)
			}
		})
	}
}

func TestWebsocketTimeoutsAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limits  Limits
		handler func(*websocket.Conn)
		timeout bool
	}{
		{"idle", Limits{IdleTimeout: 20 * time.Millisecond}, func(c *websocket.Conn) { _ = c.WriteJSON(map[string]any{"result": 1}); _, _, _ = c.ReadMessage() }, true},
		{"total", Limits{ResultTimeout: 20 * time.Millisecond}, func(c *websocket.Conn) { _, _, _ = c.ReadMessage() }, true},
		{"size", Limits{MaxMessageBytes: 32}, func(c *websocket.Conn) { _ = c.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 100))) }, false},
		{"count", Limits{MaxResults: 1}, func(c *websocket.Conn) {
			_ = c.WriteJSON(map[string]int{"result": 1})
			_ = c.WriteJSON(map[string]int{"result": 2})
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := websocketService(t, tc.handler, tc.limits)
			_, err := svc.sendTask(context.Background(), "task")
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.timeout && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected deadline: %v", err)
			}
		})
	}
}

func TestWebsocketCancellationClosesConnection(t *testing.T) {
	ready, closed := make(chan struct{}), make(chan struct{})
	svc := websocketService(t, func(c *websocket.Conn) { close(ready); _, _, _ = c.ReadMessage(); close(closed) }, Limits{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := svc.sendTask(ctx, "task"); done <- err }()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("connection not ready")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader did not exit")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("connection not closed")
	}
}

func TestWebsocketDialFailure(t *testing.T) {
	svc := NewService()
	svc.wsBaseURL = "invalid://fixture/"
	if _, err := svc.sendTask(context.Background(), "task"); err == nil {
		t.Fatal("missing dial error")
	}
	if _, err := svc.sendTask(context.Background(), ""); err == nil {
		t.Fatal("missing empty task error")
	}
}
