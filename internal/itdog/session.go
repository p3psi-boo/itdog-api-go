package itdog

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

var upstreamURL = &url.URL{Scheme: "https", Host: "www.itdog.cn", Path: "/"}

// saveCookies is called with the session gate held. Atomic rename avoids
// truncating the last usable session if the process exits during a write.
func (s *Service) saveCookies() {
	if s.cookieFile == "" || s.httpClient.Jar == nil {
		return
	}
	cookies := s.httpClient.Jar.Cookies(upstreamURL)
	data, err := json.MarshalIndent(cookies, "", "  ")
	if err == nil {
		err = writePrivateFile(s.cookieFile, data)
	}
	if err != nil {
		log.Printf("[itdog] 会话持久化失败: %v", err)
	}
}

func writePrivateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cookies-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (s *Service) loadCookies() {
	if s.cookieFile == "" || s.httpClient.Jar == nil {
		return
	}
	data, err := os.ReadFile(s.cookieFile)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		log.Printf("[itdog] 读取会话失败: %v", err)
		return
	}
	var cookies []*http.Cookie
	if err := json.Unmarshal(data, &cookies); err != nil {
		log.Printf("[itdog] cookie 文件损坏，忽略: %v", err)
		return
	}
	valid := make([]*http.Cookie, 0, len(cookies))
	for _, c := range cookies {
		if c != nil {
			valid = append(valid, c)
		}
	}
	s.httpClient.Jar.SetCookies(upstreamURL, valid)
}

// StartKeepalive returns a completion channel for orderly shutdown. It skips
// busy sessions instead of competing with verification or task creation.
func (s *Service) StartKeepalive(ctx context.Context, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	if interval <= 0 {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.keepaliveOnce(ctx)
			}
		}
	}()
	return done
}

func (s *Service) keepaliveOnce(ctx context.Context) {
	select {
	case s.taskGate <- struct{}{}:
		defer func() { <-s.taskGate }()
	default:
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	st, err := s.precheckCaptcha(ctx)
	if err != nil {
		log.Printf("[itdog] 会话保活失败: %v", err)
		return
	}
	if st.Type == "success" {
		s.saveCookies()
	} else {
		log.Printf("[itdog] 会话需要重新验证")
	}
}
