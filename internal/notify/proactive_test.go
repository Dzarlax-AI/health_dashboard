package notify

import "testing"

// The registry is populated by init() in each notification file.
// Verify both production registrations land — guards against an
// accidental removal of one of the init() blocks during refactoring.
func TestRegistry_BothProductionRulesRegistered(t *testing.T) {
	registryMu.Lock()
	defer registryMu.Unlock()

	names := map[string]bool{}
	for _, p := range registry {
		names[p.Name] = true
	}

	for _, want := range []string{"weekly_digest", "energy_backfill"} {
		if !names[want] {
			t.Errorf("registry missing %q; got %v", want, namesSlice(names))
		}
	}
}

// Every production registration must have a Render function — a
// rule that registers but provides no render is dead code and a
// confusing "skip with no Render fn" log line every morning.
// Eligible may be nil (means "always fire" — rare but legitimate).
func TestRegistry_AllHaveRender(t *testing.T) {
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, p := range registry {
		if p.Render == nil {
			t.Errorf("notification %q has nil Render", p.Name)
		}
	}
}

// LegacyKey on the pre-framework rules must not collide with the
// proactive_<name>_last_sent key the framework writes — otherwise
// the migration fallback would read its own output and never
// transition off the legacy key on tenants that had the legacy
// key but no fresh send.
func TestRegistry_LegacyKeysDistinct(t *testing.T) {
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, p := range registry {
		if p.LegacyKey == "" {
			continue
		}
		if p.LegacyKey == proactiveSentKey(p.Name) {
			t.Errorf("notification %q: LegacyKey %q collides with framework key", p.Name, p.LegacyKey)
		}
	}
}

func TestProactiveSentKey_Format(t *testing.T) {
	if got := proactiveSentKey("foo"); got != "proactive_foo_last_sent" {
		t.Errorf("got %q, want proactive_foo_last_sent", got)
	}
}

func namesSlice(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
