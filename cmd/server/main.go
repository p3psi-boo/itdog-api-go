package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"itdog-api-golang/internal/httpapi"
	"itdog-api-golang/internal/itdog"
)

// envInt rejects misspellings and out-of-range budgets instead of silently
// turning a configured retry/timeout limit into an unlimited operation.
func envInt(name string, fallback, min, max int) (int, error) {
	value, exists := os.LookupEnv(name)
	if !exists || value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s 必须是 %d 到 %d 之间的整数", name, min, max)
	}
	return n, nil
}

func envSeconds(name string, fallback time.Duration, allowZero bool) (time.Duration, error) {
	min := 1
	if allowZero {
		min = 0
	}
	n, err := envInt(name, int(fallback/time.Second), min, 86400)
	return time.Duration(n) * time.Second, err
}

func buildSolver(broker *itdog.CaptchaBroker) (itdog.CaptchaSolver, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("ITDOG_CAPTCHA_SOLVER")))
	if mode == "" {
		mode = "auto"
	}
	manualWait, err := envSeconds("ITDOG_CAPTCHA_MANUAL_WAIT", 900*time.Second, false)
	if err != nil {
		return nil, err
	}
	visionTries, err := envInt("ITDOG_CAPTCHA_VISION_TRIES", 0, 0, 10000)
	if err != nil {
		return nil, err
	}
	visionTimeout, err := envSeconds("ITDOG_CAPTCHA_VISION_TIMEOUT", 30*time.Second, false)
	if err != nil {
		return nil, err
	}
	manual := &itdog.ManualSolver{Broker: broker, WaitLimit: manualWait}
	visionURL := os.Getenv("ITDOG_CAPTCHA_VISION_URL")
	if visionURL == "" {
		visionURL = "https://api.openai.com/v1"
	}
	vision := &itdog.VisionSolver{
		BaseURL: visionURL, APIKey: os.Getenv("ITDOG_CAPTCHA_VISION_KEY"), Model: os.Getenv("ITDOG_CAPTCHA_VISION_MODEL"),
		HTTPClient: &http.Client{Timeout: visionTimeout},
	}
	haveVision := strings.TrimSpace(vision.APIKey) != "" && strings.TrimSpace(vision.Model) != ""
	switch mode {
	case "none":
		return nil, nil
	case "manual":
		return manual, nil
	case "vision":
		if !haveVision {
			return nil, errors.New("vision 模式需要 ITDOG_CAPTCHA_VISION_KEY 和 ITDOG_CAPTCHA_VISION_MODEL")
		}
		return vision, nil
	case "auto":
		if haveVision {
			return itdog.NewVisionFallbackSolver(vision, manual, visionTries), nil
		}
		log.Printf("[itdog] 未配置视觉模型，使用人工页面")
		return manual, nil
	default:
		return nil, fmt.Errorf("无效的 ITDOG_CAPTCHA_SOLVER: %q", mode)
	}
}

type serverConfig struct {
	port           int
	keepalive      time.Duration
	requestTimeout time.Duration
	limits         itdog.Limits
}

func loadConfig() (serverConfig, error) {
	c := serverConfig{limits: itdog.DefaultLimits()}
	var err error
	if c.port, err = envInt("HTTP_PORT", 3000, 1, 65535); err != nil {
		return c, err
	}
	if c.keepalive, err = envSeconds("ITDOG_SESSION_KEEPALIVE", 600*time.Second, true); err != nil {
		return c, err
	}
	if c.requestTimeout, err = envSeconds("ITDOG_REQUEST_TIMEOUT", 17*time.Minute, false); err != nil {
		return c, err
	}
	if c.limits.QueueCapacity, err = envInt("ITDOG_QUEUE_SIZE", 32, 1, 10000); err != nil {
		return c, err
	}
	for _, setting := range []struct {
		name string
		dest *time.Duration
	}{
		{"ITDOG_QUEUE_TIMEOUT", &c.limits.QueueTimeout},
		{"ITDOG_VERIFY_TIMEOUT", &c.limits.VerifyTimeout},
		{"ITDOG_RESULT_TIMEOUT", &c.limits.ResultTimeout},
	} {
		if *setting.dest, err = envSeconds(setting.name, *setting.dest, false); err != nil {
			return c, err
		}
	}
	return c, nil
}

func run(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	broker := itdog.NewCaptchaBroker()
	solver, err := buildSolver(broker)
	if err != nil {
		return err
	}
	cookieFile := os.Getenv("ITDOG_COOKIE_FILE")
	if cookieFile == "" {
		cookieFile = "data/cookies.json"
	}
	svc := itdog.NewService(itdog.WithBroker(broker), itdog.WithSolver(solver), itdog.WithCookieFile(cookieFile), itdog.WithLimits(cfg.limits))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	keepaliveDone := svc.StartKeepalive(ctx, cfg.keepalive)
	defer func() { cancel(); <-keepaliveDone }()
	srv := newHTTPServer(ctx, httpapi.NewRouter(svc, httpapi.WithRequestTimeout(cfg.requestTimeout)), cfg.requestTimeout)
	listener, err := net.Listen("tcp", ":"+strconv.Itoa(cfg.port))
	if err != nil {
		return err
	}
	log.Printf("服务已启动: %s", listener.Addr())
	return serve(ctx, srv, listener)
}

func newHTTPServer(ctx context.Context, handler http.Handler, requestTimeout time.Duration) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      requestTimeout + 30*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
}

func serve(ctx context.Context, srv *http.Server, listener net.Listener) error {
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	select {
	case err := <-done:
		_ = srv.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := srv.Shutdown(shutdownCtx)
		if err != nil {
			_ = srv.Close()
		}
		<-done
		return err
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Printf("服务退出: %v", err)
		os.Exit(1)
	}
}
