package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"itdog-api-golang/internal/httpapi"
	"itdog-api-golang/internal/itdog"
)

// buildSolver 根据环境变量组装验证码求解器：
//
//	ITDOG_CAPTCHA_SOLVER       auto | vision | manual | none（默认 auto）
//	  - auto:   vision 连续失败 N 次后降级人工（方案 C，默认）
//	  - vision: 仅视觉模型，不降级
//	  - manual: 仅人工页面
//	ITDOG_CAPTCHA_VISION_URL    OpenAI 兼容接口 base URL（默认 https://api.openai.com/v1）
//	ITDOG_CAPTCHA_VISION_KEY    API Key
//	ITDOG_CAPTCHA_VISION_MODEL  视觉模型名（必须支持图片输入）
//	ITDOG_CAPTCHA_VISION_TRIES  视觉模型尝试上限；不设或 0 = 无限重试（默认）
//	ITDOG_CAPTCHA_VISION_TIMEOUT 单次视觉调用超时秒数（默认 30）
//	ITDOG_CAPTCHA_MANUAL_WAIT   人工解题最长等待秒数（默认 900）
func buildSolver(broker *itdog.CaptchaBroker) itdog.CaptchaSolver {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("ITDOG_CAPTCHA_SOLVER")))
	if mode == "" {
		mode = "auto"
	}

	manualWait := 900 * time.Second
	if v := os.Getenv("ITDOG_CAPTCHA_MANUAL_WAIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			manualWait = time.Duration(n) * time.Second
		}
	}
	manual := &itdog.ManualSolver{Broker: broker, WaitLimit: manualWait}

	// 默认无限重试；显式设置正整数上限后，用尽才降级人工通道
	visionTries := 0
	if v := os.Getenv("ITDOG_CAPTCHA_VISION_TRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			visionTries = n
		}
	}
	visionTimeout := 30 * time.Second
	if v := os.Getenv("ITDOG_CAPTCHA_VISION_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			visionTimeout = time.Duration(n) * time.Second
		}
	}

	visionURL := os.Getenv("ITDOG_CAPTCHA_VISION_URL")
	if visionURL == "" {
		visionURL = "https://api.openai.com/v1"
	}
	vision := &itdog.VisionSolver{
		BaseURL:    visionURL,
		APIKey:     os.Getenv("ITDOG_CAPTCHA_VISION_KEY"),
		Model:      os.Getenv("ITDOG_CAPTCHA_VISION_MODEL"),
		HTTPClient: &http.Client{Timeout: visionTimeout},
	}
	haveVision := vision.Model != "" && os.Getenv("ITDOG_CAPTCHA_VISION_KEY") != ""

	switch mode {
	case "vision":
		if !haveVision {
			log.Fatal("ITDOG_CAPTCHA_SOLVER=vision 但未配置 ITDOG_CAPTCHA_VISION_KEY/ITDOG_CAPTCHA_VISION_MODEL")
		}
		return vision
	case "manual":
		return manual
	case "none":
		return nil
	default: // auto：vision 无限重试（设置 TRIES 上限后用尽才降级人工）
		if haveVision {
			return itdog.NewVisionFallbackSolver(vision, manual, visionTries)
		}
		log.Printf("[itdog] 未配置视觉模型（ITDOG_CAPTCHA_VISION_*），验证码将由人工页面处理")
		return manual
	}
}

func main() {
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "3000"
	}

	cookieFile := os.Getenv("ITDOG_COOKIE_FILE")
	if cookieFile == "" {
		cookieFile = "data/cookies.json"
	}

	broker := itdog.NewCaptchaBroker()
	svc := itdog.NewService(
		itdog.WithBroker(broker),
		itdog.WithSolver(buildSolver(broker)),
		itdog.WithCookieFile(cookieFile),
	)

	// 会话保活：防止已验证会话闲置过期（秒数，0=关闭，默认 600）
	keepalive := 600 * time.Second
	if v := os.Getenv("ITDOG_SESSION_KEEPALIVE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			keepalive = time.Duration(n) * time.Second
		}
	}
	svc.StartKeepalive(keepalive)

	r := httpapi.NewRouter(svc)

	log.Printf("服务已启动: http://localhost:%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}
