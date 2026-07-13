package tenants

import (
	"os"
	"strings"
	"testing"
)

func TestManagerSourceDoesNotStoreAdministrativeOrRegistryDSNs(t *testing.T) {
	source, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, forbidden := range []string{"AdminDSN", "RegistryDSN", "adminDSN", "registryDSN", "*AdminProvisioner"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("manager request path contains privileged dependency %q", forbidden)
		}
	}
}
