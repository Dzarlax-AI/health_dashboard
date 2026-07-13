package ui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"health-receiver/internal/registry"
)

type reconcileTenantSetup struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
	err     error
}

func (s *reconcileTenantSetup) CreateFirstTenant(context.Context, registry.CreateUserReq) (*registry.User, error) {
	return nil, errors.New("not implemented")
}
func (s *reconcileTenantSetup) CreateTenant(context.Context, registry.CreateUserReq) (*registry.User, error) {
	return nil, s.err
}
func (s *reconcileTenantSetup) ReconcileNonterminal(context.Context) error {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 && s.entered != nil {
		close(s.entered)
		<-s.release
	}
	return nil
}
func (s *reconcileTenantSetup) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestSetupReconciliationCoalescesAndCoolsDown(t *testing.T) {
	setup := &reconcileTenantSetup{entered: make(chan struct{}), release: make(chan struct{})}
	h := &Handler{tenantSetup: setup}
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	done := make(chan struct{})
	go func() {
		h.reconcileSetupIfDue(context.Background(), now)
		close(done)
	}()
	<-setup.entered
	h.reconcileSetupIfDue(context.Background(), now)
	if got := setup.callCount(); got != 1 {
		t.Fatalf("concurrent reconciliation calls=%d", got)
	}
	close(setup.release)
	<-done

	h.reconcileSetupIfDue(context.Background(), now.Add(setupReconcileCooldown-time.Second))
	if got := setup.callCount(); got != 1 {
		t.Fatalf("cooldown reconciliation calls=%d", got)
	}
	h.reconcileSetupIfDue(context.Background(), now.Add(setupReconcileCooldown))
	if got := setup.callCount(); got != 2 {
		t.Fatalf("post-cooldown reconciliation calls=%d", got)
	}
}

func TestAdminCreateTenantDoesNotExposeInternalError(t *testing.T) {
	setup := &reconcileTenantSetup{err: errors.New("database password secret leaked")}
	h := &Handler{reg: &registry.Registry{}, tenantSetup: setup}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/admin/users", strings.NewReader(`{"username":"new_user","password":"secret"}`))
	h.adminUsers(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "database password") || !strings.Contains(w.Body.String(), "failed to create tenant") {
		t.Fatalf("unsafe error response=%q", w.Body.String())
	}
}
