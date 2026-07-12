package registry

import (
	"context"
	"strings"
	"testing"
)

func TestCreateFirstUserPreparesBeforeProvision(t *testing.T) {
	for _, req := range []CreateUserReq{{Username: "alpha", Password: strings.Repeat("x", 73)}, {Username: "alpha", Password: "ok", Email: strings.Repeat("x", 321)}} {
		r := &Registry{}
		if _, _, err := r.ReserveFirstUser(context.Background(), req); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
}
