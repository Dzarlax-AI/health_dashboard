package registry

import (
	"context"
	"errors"
	"health-receiver/internal/testdb"
	"os"
	"sync"
	"testing"
)

func newEmptyTestRegistry(t *testing.T) (*Registry, context.Context) {
	t.Helper()
	ctx := context.Background()
	dsn := testdb.DSN(t)
	if registryDSN := os.Getenv("REGISTRY_TEST_DSN"); registryDSN != "" {
		dsn = registryDSN
	}
	r, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	requireDisposableRegistryDatabase(t, r, ctx)
	if err = r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if !r.IsEmpty(ctx) {
		t.Fatal("disposable registry integration database must start with an empty health_registry.users table")
	}
	t.Cleanup(func() {
		_, _ = r.pool.Exec(context.Background(), `DELETE FROM health_registry.tenant_provisioning_operations WHERE username IN ('alpha','bravo','third')`)
		_, _ = r.pool.Exec(context.Background(), `DELETE FROM health_registry.users WHERE username IN ('alpha','bravo','third')`)
	})
	return r, ctx
}

func requireDisposableRegistryDatabase(t *testing.T, r *Registry, ctx context.Context) {
	t.Helper()
	var disposable bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM health_test_metadata
			WHERE key = 'disposable_database' AND value = 'true'
		)
	`).Scan(&disposable)
	if err != nil || !disposable {
		t.Fatalf("HEALTH_DB_TESTS=1 requires health_test_metadata disposable_database=true marker (query error: %v)", err)
	}
}

func TestCreateFirstUserConcurrentExactlyOneWins(t *testing.T) {
	r, ctx := newEmptyTestRegistry(t)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"alpha", "bravo"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			<-start
			_, _, err := r.ReserveFirstUser(ctx, CreateUserReq{Username: name, Password: "secret", IsAdmin: true})
			errs <- err
		}(name)
	}
	close(start)
	wg.Wait()
	close(errs)
	var success, closed int
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrSetupClosed) {
			closed++
		} else {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if success != 1 || closed != 1 {
		t.Fatalf("success=%d closed=%d", success, closed)
	}
	if r.IsEmpty(ctx) {
		t.Fatal("registry remained empty")
	}
	if _, _, err := r.ReserveFirstUser(ctx, CreateUserReq{Username: "third", Password: "secret"}); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("closed setup err=%v", err)
	}
}
