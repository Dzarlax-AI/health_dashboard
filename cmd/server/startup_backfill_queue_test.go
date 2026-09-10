package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartupBackfillQueueRunsTasksSequentially(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := newStartupBackfillQueue(ctx, 0)
	queue.Start()
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var running, maxRunning atomic.Int32

	queue.Enqueue("first", func() {
		if current := running.Add(1); current > maxRunning.Load() {
			maxRunning.Store(current)
		}
		close(firstStarted)
		<-releaseFirst
		running.Add(-1)
	})
	queue.Enqueue("second", func() {
		if current := running.Add(1); current > maxRunning.Load() {
			maxRunning.Store(current)
		}
		close(secondStarted)
		running.Add(-1)
	})

	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first task did not start")
	}
	select {
	case <-secondStarted:
		t.Fatal("second task started before the first task finished")
	default:
	}

	close(releaseFirst)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second task did not start after the first task finished")
	}
	if got := maxRunning.Load(); got != 1 {
		t.Fatalf("maximum concurrent startup tasks = %d, want 1", got)
	}
}

func TestStartupBackfillQueueAcceptsAllBootTenantsBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := newStartupBackfillQueue(ctx, 0)
	previousLogWriter := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(previousLogWriter)
	const tenantCount = 129
	done := make(chan int, tenantCount)
	for i := 0; i < tenantCount; i++ {
		i := i
		queue.Enqueue(fmt.Sprintf("tenant-%03d", i), func() { done <- i })
	}

	// The process only starts the worker after its HTTP listener is ready.
	// Enqueue must therefore never wait for a fixed-size consumer queue.
	queue.Start()
	for want := 0; want < tenantCount; want++ {
		select {
		case got := <-done:
			if got != want {
				t.Fatalf("startup task order = %d, want %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("startup task %d did not run", want)
		}
	}
}
