package registry

import "testing"

func TestValidateUsername(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{name: "admin", ok: true},
		{name: "mariia_2", ok: true},
		{name: "a123456789012345678901234567890", ok: true},
		{name: "", ok: false},
		{name: "Admin", ok: false},
		{name: "1admin", ok: false},
		{name: "admin-user", ok: false},
		{name: "admin;drop", ok: false},
		{name: "a1234567890123456789012345678901", ok: false},
	}
	for _, tt := range tests {
		err := ValidateUsername(tt.name)
		if (err == nil) != tt.ok {
			t.Fatalf("ValidateUsername(%q) err=%v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}

func TestValidateSchemaName(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{name: "health", ok: true},
		{name: "health_mariia", ok: true},
		{name: "a12345678901234567890123456789012345678901234567890123456789012", ok: true},
		{name: "", ok: false},
		{name: "Health", ok: false},
		{name: "1health", ok: false},
		{name: "health-mariia", ok: false},
		{name: "health;drop", ok: false},
		{name: "a123456789012345678901234567890123456789012345678901234567890123", ok: false},
	}
	for _, tt := range tests {
		err := ValidateSchemaName(tt.name)
		if (err == nil) != tt.ok {
			t.Fatalf("ValidateSchemaName(%q) err=%v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}
