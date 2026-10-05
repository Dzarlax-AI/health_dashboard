package storage

import (
	"context"
	"sync"
)

// CachePriorityLock serializes cache mutations while allowing foreground work
// to acquire the lock before queued background work. It does not preempt a
// background operation that already holds the lock.
//
// The zero value is ready to use. Lock and Unlock preserve the sync.Mutex-like
// API used by existing foreground callers; background callers use
// LockBackground so shutdown can cancel a queued acquisition.
type CachePriorityLock struct {
	mu                sync.Mutex
	held              bool
	waitingForeground int
	waitingBackground int
	changed           chan struct{}
}

func (l *CachePriorityLock) changedLocked() <-chan struct{} {
	if l.changed == nil {
		l.changed = make(chan struct{})
	}
	return l.changed
}

func (l *CachePriorityLock) signalLocked() {
	if l.changed != nil {
		close(l.changed)
	}
	l.changed = make(chan struct{})
}

// Lock acquires the lock as foreground work. Foreground waiters are counted
// before blocking, so a newly arriving background waiter cannot pass them.
func (l *CachePriorityLock) Lock() {
	l.mu.Lock()
	l.waitingForeground++
	l.signalLocked()
	for {
		if !l.held {
			l.held = true
			l.waitingForeground--
			l.signalLocked()
			l.mu.Unlock()
			return
		}
		changed := l.changedLocked()
		l.mu.Unlock()
		<-changed
		l.mu.Lock()
	}
}

// LockBackground acquires the lock only when no foreground waiter is queued.
// Cancellation removes the waiter without changing lock ownership.
func (l *CachePriorityLock) LockBackground(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	registered := false
	for {
		l.mu.Lock()
		if err := ctx.Err(); err != nil {
			if registered {
				l.waitingBackground--
				l.signalLocked()
			}
			l.mu.Unlock()
			return err
		}
		if !l.held && l.waitingForeground == 0 {
			l.held = true
			if registered {
				l.waitingBackground--
			}
			l.signalLocked()
			l.mu.Unlock()
			return nil
		}
		if !registered {
			registered = true
			l.waitingBackground++
			l.signalLocked()
		}
		changed := l.changedLocked()
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			l.mu.Lock()
			if registered {
				l.waitingBackground--
				l.signalLocked()
				registered = false
			}
			l.mu.Unlock()
			return ctx.Err()
		case <-changed:
		}
	}
}

// Unlock releases the held operation and wakes all waiters to re-check
// foreground priority.
func (l *CachePriorityLock) Unlock() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held {
		panic("storage: unlock of unlocked CachePriorityLock")
	}
	l.held = false
	l.signalLocked()
}
