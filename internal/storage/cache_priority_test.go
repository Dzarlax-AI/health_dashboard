package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func waitForPriorityLockState(t *testing.T, lock *CachePriorityLock, predicate func(held bool, foreground, background int) bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		lock.mu.Lock()
		if predicate(lock.held, lock.waitingForeground, lock.waitingBackground) {
			lock.mu.Unlock()
			return
		}
		changed := lock.changedLocked()
		lock.mu.Unlock()
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("timed out waiting for lock state")
		}
	}
}

func TestCachePriorityLockForegroundWinsNextAcquisition(t *testing.T) {
	var lock CachePriorityLock
	lock.Lock()

	backgroundAcquired := make(chan struct{}, 1)
	backgroundDone := make(chan struct{})
	go func() {
		defer close(backgroundDone)
		if err := lock.LockBackground(context.Background()); err != nil {
			return
		}
		backgroundAcquired <- struct{}{}
		lock.Unlock()
	}()
	waitForPriorityLockState(t, &lock, func(held bool, _, background int) bool { return held && background == 1 })

	foregroundAcquired := make(chan struct{}, 1)
	foregroundDone := make(chan struct{})
	go func() {
		defer close(foregroundDone)
		lock.Lock()
		foregroundAcquired <- struct{}{}
		lock.Unlock()
	}()
	waitForPriorityLockState(t, &lock, func(held bool, foreground, _ int) bool { return held && foreground == 1 })

	lock.Unlock()
	select {
	case <-foregroundAcquired:
	case <-time.After(2 * time.Second):
		t.Fatal("foreground waiter did not acquire after current operation")
	}
	select {
	case <-backgroundAcquired:
	case <-time.After(2 * time.Second):
		t.Fatal("background waiter did not acquire after foreground work")
	}
	<-foregroundDone
	<-backgroundDone
}

func TestCachePriorityLockBackgroundCancellationUnregistersWaiter(t *testing.T) {
	var lock CachePriorityLock
	lock.Lock()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- lock.LockBackground(ctx) }()
	waitForPriorityLockState(t, &lock, func(held bool, _, background int) bool { return held && background == 1 })
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("LockBackground error = %v, want context.Canceled", err)
	}
	waitForPriorityLockState(t, &lock, func(held bool, _, background int) bool { return held && background == 0 })
	lock.Unlock()

	if err := lock.LockBackground(context.Background()); err != nil {
		t.Fatalf("LockBackground after cancellation: %v", err)
	}
	lock.Unlock()
}
