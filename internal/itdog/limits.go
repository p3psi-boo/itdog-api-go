package itdog

import (
	"context"
	"errors"
	"time"
)

var (
	ErrQueueFull    = errors.New("任务队列已满")
	ErrQueueTimeout = errors.New("任务排队超时")
)

type Limits struct {
	QueueCapacity   int
	QueueTimeout    time.Duration
	VerifyTimeout   time.Duration
	ResultTimeout   time.Duration
	IdleTimeout     time.Duration
	WriteTimeout    time.Duration
	MaxMessageBytes int64
	MaxResults      int
}

func DefaultLimits() Limits {
	return Limits{
		QueueCapacity:   32,
		QueueTimeout:    30 * time.Second,
		VerifyTimeout:   15 * time.Minute,
		ResultTimeout:   45 * time.Second,
		IdleTimeout:     6 * time.Second,
		WriteTimeout:    5 * time.Second,
		MaxMessageBytes: 1 << 20,
		MaxResults:      4096,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.QueueCapacity <= 0 {
		l.QueueCapacity = d.QueueCapacity
	}
	if l.QueueTimeout <= 0 {
		l.QueueTimeout = d.QueueTimeout
	}
	if l.VerifyTimeout <= 0 {
		l.VerifyTimeout = d.VerifyTimeout
	}
	if l.ResultTimeout <= 0 {
		l.ResultTimeout = d.ResultTimeout
	}
	if l.IdleTimeout <= 0 {
		l.IdleTimeout = d.IdleTimeout
	}
	if l.WriteTimeout <= 0 {
		l.WriteTimeout = d.WriteTimeout
	}
	if l.MaxMessageBytes <= 0 {
		l.MaxMessageBytes = d.MaxMessageBytes
	}
	if l.MaxResults <= 0 {
		l.MaxResults = d.MaxResults
	}
	return l
}

func WithLimits(l Limits) Option { return func(s *Service) { s.limits = l } }

// acquireTask bounds both queue length and waiting time. Cancellation releases
// a waiting slot without needing the active task to finish.
func (s *Service) acquireTask(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case s.taskSlots <- struct{}{}:
	default:
		return nil, ErrQueueFull
	}
	waitCtx, cancel := context.WithTimeout(ctx, s.limits.QueueTimeout)
	defer cancel()
	select {
	case s.taskGate <- struct{}{}:
		if err := waitCtx.Err(); err != nil {
			<-s.taskGate
			<-s.taskSlots
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrQueueTimeout
		}
		return func() { <-s.taskGate; <-s.taskSlots }, nil
	case <-waitCtx.Done():
		<-s.taskSlots
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrQueueTimeout
	}
}
