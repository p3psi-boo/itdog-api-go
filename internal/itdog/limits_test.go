package itdog

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTaskQueueCancellationCapacityAndTimeout(t *testing.T) {
	svc := NewService(WithLimits(Limits{QueueCapacity: 1, QueueTimeout: 200 * time.Millisecond}))
	release, err := svc.acquireTask(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan error, 1)
	go func() {
		release, err := svc.acquireTask(ctx)
		if release != nil {
			release()
		}
		waiting <- err
	}()
	deadline := time.Now().Add(time.Second)
	for len(svc.taskSlots) != 2 {
		if time.Now().After(deadline) {
			t.Fatal("waiter not queued")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := svc.acquireTask(context.Background()); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("want full: %v", err)
	}
	cancel()
	select {
	case err := <-waiting:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter stuck")
	}
	if len(svc.taskSlots) != 1 {
		t.Fatal("canceled waiter leaked slot")
	}
	if _, err := svc.acquireTask(context.Background()); !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("want queue timeout: %v", err)
	}
	release()
	release, err = svc.acquireTask(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if len(svc.taskSlots) != 0 || len(svc.taskGate) != 0 {
		t.Fatal("queue slots leaked")
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := svc.acquireTask(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
