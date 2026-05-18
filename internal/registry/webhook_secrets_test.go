package registry

import "testing"

// resolveWebhookSecretsDecision is the pure decision the registry
// orchestration wraps. Contract:
//   - Both env vars set → env wins, persisted into global (operator
//     intent always overrides cached values).
//   - Env empty/partial AND both global vars set → use global.
//   - Otherwise → generate fresh pair.
//
// Half-set env (only one of two vars) is treated as not-set so we
// don't end up running with a mismatched pair.
func TestResolveWebhookSecretsDecision(t *testing.T) {
	cases := []struct {
		name                                 string
		globalSecret, globalToken            string
		envSecret, envToken                  string
		wantSource                           string
		wantPersist                          bool
		wantSecretFromEnv, wantTokenFromEnv  bool
		wantSecretFromGlobal, wantFromGlobal bool
	}{
		{
			name:        "first boot, nothing anywhere — generate",
			wantSource:  "generate",
			wantPersist: true,
		},
		{
			name:                 "global has both, env empty — use global",
			globalSecret:         "g_secret",
			globalToken:          "g_token",
			wantSource:           "global",
			wantPersist:          false,
			wantSecretFromGlobal: true,
			wantFromGlobal:       true,
		},
		{
			name:              "env has both, global empty — env wins, persist",
			envSecret:         "e_secret",
			envToken:          "e_token",
			wantSource:        "env",
			wantPersist:       true,
			wantSecretFromEnv: true,
			wantTokenFromEnv:  true,
		},
		{
			name:              "env has both AND global has both — env wins (rotation), persist",
			globalSecret:      "g_secret",
			globalToken:       "g_token",
			envSecret:         "e_secret",
			envToken:          "e_token",
			wantSource:        "env",
			wantPersist:       true,
			wantSecretFromEnv: true,
			wantTokenFromEnv:  true,
		},
		{
			name:                 "half-set env (secret only) — ignored, fall to global",
			globalSecret:         "g_secret",
			globalToken:          "g_token",
			envSecret:            "e_secret",
			envToken:             "",
			wantSource:           "global",
			wantPersist:          false,
			wantSecretFromGlobal: true,
			wantFromGlobal:       true,
		},
		{
			name:        "half-set env, no global — generate",
			envSecret:   "",
			envToken:    "e_token",
			wantSource:  "generate",
			wantPersist: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			secret, token, source, persist := resolveWebhookSecretsDecision(
				tc.globalSecret, tc.globalToken, tc.envSecret, tc.envToken)
			if source != tc.wantSource {
				t.Errorf("source = %q, want %q", source, tc.wantSource)
			}
			if persist != tc.wantPersist {
				t.Errorf("persist = %v, want %v", persist, tc.wantPersist)
			}
			switch tc.wantSource {
			case "env":
				if secret != tc.envSecret || token != tc.envToken {
					t.Errorf("env source must return env values; got (%q, %q)", secret, token)
				}
			case "global":
				if secret != tc.globalSecret || token != tc.globalToken {
					t.Errorf("global source must return global values; got (%q, %q)", secret, token)
				}
			case "generate":
				if secret != "" || token != "" {
					t.Errorf("generate source must return empty (caller generates); got (%q, %q)", secret, token)
				}
			}
		})
	}
}

func TestGenerateRandomSecret_HexShape(t *testing.T) {
	s, err := generateRandomSecret()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(s) != 64 {
		// 32 bytes hex-encoded = 64 chars.
		t.Errorf("expected 64-char hex string, got len=%d (%q)", len(s), s)
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			t.Errorf("non-hex character in secret: %q", c)
			break
		}
	}
	// Two consecutive calls must not collide — sanity check on the
	// randomness source.
	s2, _ := generateRandomSecret()
	if s == s2 {
		t.Errorf("two generated secrets collided: %q", s)
	}
}
