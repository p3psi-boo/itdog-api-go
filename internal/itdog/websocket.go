package itdog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"
)

// sendTask has a single synchronous reader. Cancellation closes the socket,
// unblocking both reads and writes; there is no channel sender to leave behind.
func (s *Service) sendTask(ctx context.Context, taskID string) ([]map[string]any, error) {
	if taskID == "" {
		return nil, errors.New("任务 ID 为空")
	}
	ctx, cancel := context.WithTimeout(ctx, s.limits.ResultTimeout)
	defer cancel()
	wsURL := s.wsBaseURL + url.PathEscape(taskID) + "/" + getToken(taskID)
	conn, _, err := s.wsDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("连接结果通道: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	conn.SetReadLimit(s.limits.MaxMessageBytes)
	if err := conn.SetWriteDeadline(time.Now().Add(s.limits.WriteTimeout)); err != nil {
		return nil, err
	}
	if err := conn.WriteJSON(map[string]string{"task_id": taskID}); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("发送任务: %w", err)
	}
	results := make([]map[string]any, 0, 32)
	var receivedBytes int
	deadline, _ := ctx.Deadline()
	for {
		readDeadline := deadline
		// Before the first result, allow the full task budget.
		if len(results) > 0 {
			if idle := time.Now().Add(s.limits.IdleTimeout); idle.Before(readDeadline) {
				readDeadline = idle
			}
		}
		if err := conn.SetReadDeadline(readDeadline); err != nil {
			return results, err
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return results, ctx.Err()
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return results, fmt.Errorf("等待结果超时: %w", context.DeadlineExceeded)
			}
			return results, fmt.Errorf("结果流在 finished 前断开: %w", err)
		}
		var item map[string]any
		receivedBytes += len(data)
		if receivedBytes > 16<<20 {
			return results, errors.New("上游结果总大小超过限制")
		}
		if err := json.Unmarshal(data, &item); err != nil || item == nil {
			return results, errors.New("上游结果不是有效 JSON 对象")
		}
		if stringFrom(item["type"]) == "finished" {
			return results, nil
		}
		if len(results) >= s.limits.MaxResults {
			return results, errors.New("上游节点结果超过数量限制")
		}
		results = append(results, item)
	}
}
