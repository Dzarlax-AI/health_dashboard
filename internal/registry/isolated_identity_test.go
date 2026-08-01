package registry

import (
	"context"
	"strings"
	"testing"
)

func TestNewWithExpectedIdentityRejectsParsedRoleChangingParamsBeforeOpen(t *testing.T) {
	for name, dsn := range map[string]string{
		"role":                  "postgres://root:secret@127.0.0.1:1/db?role=health_registry",
		"session authorization": "postgres://root:secret@127.0.0.1:1/db?session_authorization=health_registry",
		"options impersonation": "postgres://root:secret@127.0.0.1:1/db?options=-c%20session_authorization%3Dhealth_registry",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewWithExpectedIdentity(context.Background(), dsn, "health_registry")
			if err == nil || !strings.Contains(err.Error(), "forbidden startup parameters") {
				t.Fatalf("role-changing DSN was not rejected before connect: %v", err)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), dsn) {
				t.Fatalf("isolated constructor leaked DSN details: %v", err)
			}
		})
	}
}
