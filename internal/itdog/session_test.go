package itdog

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestCookiePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "cookies.json")
	svc := NewService(WithCookieFile(path))
	svc.httpClient.Jar.SetCookies(upstreamURL, []*http.Cookie{{Name: "session", Value: "fixture"}})
	svc.saveCookies()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v", info.Mode())
	}
	restored := NewService(WithCookieFile(path))
	cookies := restored.httpClient.Jar.Cookies(upstreamURL)
	if len(cookies) != 1 || cookies[0].Value != "fixture" {
		t.Fatalf("%v", cookies)
	}
	svc.httpClient.Jar.SetCookies(upstreamURL, []*http.Cookie{{Name: "session", MaxAge: -1}})
	svc.saveCookies()
	if got := NewService(WithCookieFile(path)).httpClient.Jar.Cookies(upstreamURL); len(got) != 0 {
		t.Fatalf("stale cookies: %v", got)
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".cookies-*"))
	if len(files) != 0 {
		t.Fatal(files)
	}
	_ = os.WriteFile(path, []byte("broken"), 0600)
	_ = NewService(WithCookieFile(path))
}

func TestKeepaliveSkipsBusySessionAndStops(t *testing.T) {
	var calls atomic.Int32
	svc := localService(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"type":"success"}`)
	})
	release, err := svc.acquireTask(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	svc.keepaliveOnce(context.Background())
	if calls.Load() != 0 {
		t.Fatal("keepalive competed with task")
	}
	release()
	svc.keepaliveOnce(context.Background())
	if calls.Load() != 1 {
		t.Fatal("keepalive did not run")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := svc.StartKeepalive(ctx, time.Hour)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("keepalive stuck")
	}
	<-svc.StartKeepalive(context.Background(), 0)
}

func TestKeepaliveCancelsInflightRequest(t *testing.T) {
	ready := make(chan struct{})
	svc := localService(t, func(w http.ResponseWriter, r *http.Request) { close(ready); <-r.Context().Done() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := svc.StartKeepalive(ctx, time.Millisecond)
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("no keepalive")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("inflight keepalive stuck")
	}
	if len(svc.taskGate) != 0 {
		t.Fatal("session gate leaked")
	}
}
