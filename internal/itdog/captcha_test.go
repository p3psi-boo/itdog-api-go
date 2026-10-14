package itdog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestGetToken(t *testing.T) {
	// 实测样本：上游 2026-10 版本（task_id -> ws token）
	const taskID = "20261014000008sxvbzb6qiavdpnvh5v"
	const want = "4c476e94634ca159"
	if got := getToken(taskID); got != want {
		t.Fatalf("getToken(%q) = %q, want %q", taskID, got, want)
	}
}

func TestGetTokenLength(t *testing.T) {
	if got := getToken("abc"); len(got) != 16 {
		t.Fatalf("token length = %d, want 16", len(got))
	}
}

func TestBuildCaptchaInfoFormat(t *testing.T) {
	info := buildCaptchaInfo(350, 200, [][2]int{{195, 142}, {122, 137}})
	parts := strings.Split(info, ";")
	if len(parts) != 4 {
		t.Fatalf("info 应有 4 段: %q", info)
	}
	if parts[0] != "195,142-122,137" {
		t.Fatalf("坐标段错误: %q", parts[0])
	}
	if parts[1] != "350" || parts[2] != "200" {
		t.Fatalf("尺寸段错误: %q %q", parts[1], parts[2])
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		t.Fatalf("payload base64 解码失败: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("payload json 解码失败: %v", err)
	}
	if payload["v"].(float64) != 2 {
		t.Fatalf("payload v = %v", payload["v"])
	}
	if payload["webdriver"].(bool) {
		t.Fatal("webdriver 应为 false")
	}
	clicks, ok := payload["clicks"].([]any)
	if !ok || len(clicks) != 2 {
		t.Fatalf("clicks 应为 2 个: %v", payload["clicks"])
	}
	c0 := clicks[0].(map[string]any)
	if c0["x"].(float64) != 195 || c0["y"].(float64) != 142 {
		t.Fatalf("click[0] 坐标错误: %v", c0)
	}
	if c0["trusted"].(float64) != 1 || c0["pointer"].(string) != "mouse" {
		t.Fatalf("click[0] 行为字段错误: %v", c0)
	}
}

func TestParseVisionPoints(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"fenced", "```json\n{\"points\":[{\"x\":220,\"y\":170},{\"x\":223,\"y\":118}]}\n```"},
		{"bare", "{\"points\":[{\"x\":10,\"y\":20},{\"x\":30,\"y\":40}]}"},
		{"mixed text", "结果如下 {\"points\":[{\"x\":1,\"y\":2},{\"x\":3,\"y\":4}]} 供参考"},
	}
	for _, c := range cases {
		pts := parseVisionPoints(c.in)
		if len(pts) != 2 {
			t.Fatalf("%s: 解析出 %d 个点", c.name, len(pts))
		}
	}
}

func TestJpegSize(t *testing.T) {
	// 最小化验证：非 JPEG 数据返回 0
	if w, h := jpegSize([]byte{0xFF, 0xD8, 0xFF}); w != 0 || h != 0 {
		t.Fatalf("应返回 0,0，得到 %d,%d", w, h)
	}
}

// --- VisionFallbackSolver 降级策略测试 ---

type fakeSolver struct {
	name  string
	calls int
	err   error
	pts   [][2]int
}

func (f *fakeSolver) Name() string { return f.name }
func (f *fakeSolver) Solve(ctx context.Context, ch CaptchaChallenge) (CaptchaSolution, error) {
	f.calls++
	if f.err != nil {
		return CaptchaSolution{}, f.err
	}
	return CaptchaSolution{Points: f.pts}, nil
}

func TestVisionFallbackAfterFiveFailures(t *testing.T) {
	ctx := context.Background()
	vision := &fakeSolver{name: "vision", err: errors.New("boom")}
	manual := &fakeSolver{name: "manual", pts: [][2]int{{1, 1}, {2, 2}}}
	s := NewVisionFallbackSolver(vision, manual, 5)

	// 前 5 次：全部走 vision 且返回错误
	for i := 1; i <= 5; i++ {
		_, err := s.Solve(ctx, CaptchaChallenge{})
		if i < 5 {
			if err == nil {
				t.Fatalf("第 %d 次应返回错误", i)
			}
			if s.ManualPhase() {
				t.Fatalf("第 %d 次不应进入人工阶段", i)
			}
		}
	}
	if vision.calls != 5 {
		t.Fatalf("vision 应被调用 5 次，实际 %d", vision.calls)
	}
	if manual.calls != 0 {
		t.Fatalf("前 5 次不应调用 manual，实际 %d", manual.calls)
	}
	if !s.ManualPhase() {
		t.Fatal("第 5 次失败后应进入人工阶段")
	}
	// 第 6 次起：走人工
	sol, err := s.Solve(ctx, CaptchaChallenge{})
	if err != nil || len(sol.Points) != 2 {
		t.Fatalf("人工阶段应成功: %v %v", sol, err)
	}
	if manual.calls != 1 || vision.calls != 5 {
		t.Fatalf("调用计数错误 vision=%d manual=%d", vision.calls, manual.calls)
	}
}

func TestVisionFallbackSuccessStaysVision(t *testing.T) {
	ctx := context.Background()
	vision := &fakeSolver{name: "vision", pts: [][2]int{{5, 5}, {6, 6}}}
	manual := &fakeSolver{name: "manual"}
	s := NewVisionFallbackSolver(vision, manual, 5)
	for i := 0; i < 3; i++ {
		sol, err := s.Solve(ctx, CaptchaChallenge{})
		if err != nil || len(sol.Points) != 2 {
			t.Fatalf("vision 成功应直接返回: %v %v", sol, err)
		}
	}
	if manual.calls != 0 {
		t.Fatal("vision 成功时不应调用 manual")
	}
}

func TestVisionUnlimitedNeverFallsBack(t *testing.T) {
	ctx := context.Background()
	vision := &fakeSolver{name: "vision", err: errors.New("boom")}
	manual := &fakeSolver{name: "manual"}
	s := NewVisionFallbackSolver(vision, manual, 0) // 0 = 无限

	for i := 1; i <= 8; i++ {
		_, err := s.Solve(ctx, CaptchaChallenge{})
		if err == nil {
			t.Fatalf("第 %d 次应返回错误", i)
		}
		if s.ManualPhase() {
			t.Fatal("无限模式不应进入人工阶段")
		}
	}
	if vision.calls != 8 || manual.calls != 0 {
		t.Fatalf("调用计数错误 vision=%d manual=%d", vision.calls, manual.calls)
	}
	if got := s.Name(); got != "vision(8/∞)" {
		t.Fatalf("Name() = %q", got)
	}
}
