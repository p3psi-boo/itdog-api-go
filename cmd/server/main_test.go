package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"itdog-api-golang/internal/itdog"
)

func cleanEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"HTTP_PORT", "ITDOG_SESSION_KEEPALIVE", "ITDOG_REQUEST_TIMEOUT", "ITDOG_QUEUE_SIZE", "ITDOG_QUEUE_TIMEOUT", "ITDOG_VERIFY_TIMEOUT", "ITDOG_RESULT_TIMEOUT", "ITDOG_CAPTCHA_SOLVER", "ITDOG_CAPTCHA_MANUAL_WAIT", "ITDOG_CAPTCHA_VISION_TRIES", "ITDOG_CAPTCHA_VISION_TIMEOUT", "ITDOG_CAPTCHA_VISION_KEY", "ITDOG_CAPTCHA_VISION_MODEL"} {
		t.Setenv(name, "")
	}
}

func TestConfigDefaultsAndValidation(t *testing.T) {
	cleanEnvironment(t)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.port != 3000 || cfg.requestTimeout != 17*time.Minute || cfg.limits.QueueCapacity != 32 {
		t.Fatalf("%+v", cfg)
	}
	for _, name := range []string{"HTTP_PORT", "ITDOG_SESSION_KEEPALIVE", "ITDOG_REQUEST_TIMEOUT", "ITDOG_QUEUE_SIZE", "ITDOG_QUEUE_TIMEOUT", "ITDOG_VERIFY_TIMEOUT", "ITDOG_RESULT_TIMEOUT"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "-1")
			if _, err := loadConfig(); err == nil {
				t.Fatal("invalid setting ignored")
			}
		})
	}
	t.Setenv("ITDOG_SESSION_KEEPALIVE", "0")
	cfg, err = loadConfig()
	if err != nil || cfg.keepalive != 0 {
		t.Fatalf("%+v %v", cfg, err)
	}
}

func TestBuildSolverModes(t *testing.T) {
	cleanEnvironment(t)
	for _, mode := range []string{"auto", "manual", "none", "vision", "typo"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("ITDOG_CAPTCHA_SOLVER", mode)
			s, err := buildSolver(itdog.NewCaptchaBroker())
			wantErr := mode == "vision" || mode == "typo"
			if (err != nil) != wantErr {
				t.Fatal(err)
			}
			if mode == "none" && s != nil {
				t.Fatal("none mode has solver")
			}
		})
	}
	t.Setenv("ITDOG_CAPTCHA_VISION_KEY", "fixture")
	t.Setenv("ITDOG_CAPTCHA_VISION_MODEL", "fixture")
	s, err := buildSolver(itdog.NewCaptchaBroker())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*itdog.VisionFallbackSolver); !ok {
		t.Fatalf("%T", s)
	}
	for _, name := range []string{"ITDOG_CAPTCHA_MANUAL_WAIT", "ITDOG_CAPTCHA_VISION_TRIES", "ITDOG_CAPTCHA_VISION_TIMEOUT"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "invalid")
			if _, err := buildSolver(itdog.NewCaptchaBroker()); err == nil {
				t.Fatal("invalid setting ignored")
			}
		})
	}
}

func TestGracefulShutdownCancelsActiveRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	srv := newHTTPServer(ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(ready); <-r.Context().Done(); w.WriteHeader(503) }), time.Second)
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatal("missing server timeout")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, listener) }()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		c := http.Client{Timeout: 2 * time.Second}
		resp, err := c.Get("http://" + listener.Addr().String())
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown stuck")
	}
	<-clientDone
}
