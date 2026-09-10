package storage

import (
	"errors"
	"testing"
)

func TestRetryTodayDerivedStateError(t *testing.T) {
	if retryTodayDerivedStateError(ErrNoHourlyMetricData) {
		t.Fatal("empty tenant state must wait for ingestion, not retry")
	}
	if retryTodayDerivedStateError(errors.New("temporary database failure")) == false {
		t.Fatal("transient failure must remain retryable")
	}
}
