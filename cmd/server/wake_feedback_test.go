package main

import (
	"testing"
	"time"

	"health-receiver/internal/notify"
)

func TestTrySendWakeFeedbackAfterMorningRequiresWebhook(t *testing.T) {
	if trySendWakeFeedbackAfterMorning(nil, nil, notify.Config{}, "2026-08-05", time.Now(), false) {
		t.Fatal("wake feedback sent without an available callback webhook")
	}
}
