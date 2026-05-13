package notify

import (
	"context"
	"log"
	"sync"
	"time"

	"health-receiver/internal/storage"
)

// ProactiveNotification is one self-contained Telegram "we should tell
// the user about this" rule. Two motivating instances ship today
// (`weekly_digest`, `energy_backfill`); the framework lives apart so
// the next half-dozen — illness flag, HRV crash early warning, sleep
// streak, consistency milestone, etc. — register without copying the
// gate / cadence / persist boilerplate that the two pre-framework
// versions duplicated.
//
// Lifecycle for each registered notification on a morning tick:
//
//   1. Cadence check: was this rule sent within the last `Cadence`?
//      Reads `proactive_<Name>_last_sent` from the settings table.
//   2. Hour gate (optional): if `HourOfDay >= 0`, only fire when the
//      current hour-in-tenant-TZ equals it. -1 means "any tick".
//   3. Eligible(): return false to skip silently (logged once at
//      debug level for ops, never user-visible).
//   4. Render(): build the message. Has access to the DB pool and
//      baseURL for clickable links.
//   5. Bot.Send + persist the date marker.
//
// Errors at any step log + skip, never bubble — a notification rule
// must not break the morning report path.
type ProactiveNotification struct {
	// Name is the stable identifier used as the settings key for
	// last-sent persistence ("proactive_<Name>_last_sent") and in
	// logs. Renaming a notification orphans its persistence row;
	// migration is the caller's problem.
	Name string

	// Cadence is the minimum gap between consecutive sends. Use
	// `7*24*time.Hour` for weekly, `24*time.Hour` for daily,
	// `0` for "fire every tick the gate passes" (rare — usually a
	// sign the gate has its own throttle already).
	Cadence time.Duration

	// HourOfDay optionally restricts firing to one hour-of-day in
	// tenant TZ (0..23). -1 = any tick. Most notifications use -1
	// and rely on the morning scheduler's once-per-day cadence.
	// Use a specific hour when piggy-backing on a tick that fires
	// more frequently than daily (none today, but the design
	// anticipates future intra-day ticks).
	HourOfDay int

	// Eligible returns (true, "") to render+send, (false, "reason")
	// to skip. The reason string is logged for ops debugging but
	// never reaches the user. It should be a short snake_case
	// identifier — same convention as the v1 `*_*_last_sent`
	// reasons (no_tz, not_enough_data, already_backfilled, etc.).
	Eligible func(ctx context.Context, db *storage.DB, cfg Config) (bool, string)

	// Render produces the Telegram message body. Called only after
	// Eligible passes. Has access to `cfg.Lang` for i18n via tr()
	// and `baseURL` for clickable links back to the dashboard.
	// Return an error to log + skip; an empty string is also
	// treated as "skip" (idiomatic when the render itself
	// re-evaluates a condition the eligibility didn't cover).
	Render func(ctx context.Context, db *storage.DB, cfg Config, baseURL string) (string, error)

	// LegacyKey, if set, is read as a fallback on the cadence check
	// when the framework's own key is empty. Used during one-shot
	// migration of pre-framework notifications (digest, energy_nudge)
	// — without this, the first post-deploy tick would consider the
	// new key empty and re-fire even though the user already got
	// the same message yesterday under the old key. After the first
	// new-key write, this fallback is never consulted again for
	// this tenant. Leave zero-value on new notifications that
	// have no pre-framework predecessor.
	LegacyKey string
}

// registry holds every proactive notification known to the binary.
// Populated at init() time by each notification's own file (digest.go,
// energy_nudge.go, etc.). Mutex guards Register against the rare
// case where init() ordering is non-deterministic between two packages
// both registering — never observed in this codebase, but cheap
// insurance.
var (
	registry   []ProactiveNotification
	registryMu sync.Mutex
)

// Register adds a proactive notification to the registry. Call from
// the notification file's init() so it's wired up before any code
// path tries to fire.
//
// Duplicate names are NOT detected — the caller is responsible for
// uniqueness, since accidental duplicates would silently share the
// same persistence key and step on each other's "last sent" date.
func Register(p ProactiveNotification) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, p)
}

// MaybeFireAll iterates the registry, checking cadence + eligibility
// for each notification, sending the ones that pass both gates. Called
// from cmd/server/main.go's morning scheduler tick (and any future
// intra-day tick that wants to drive proactive notifications).
//
// Errors and skips are logged but never bubble up — one misbehaving
// notification rule must not block the others. The morning report
// itself fires separately downstream of this call, so even a
// catastrophic registry failure leaves the user with their normal
// morning summary.
func MaybeFireAll(bot *Bot, db *storage.DB, cfg Config, baseURL string) {
	if !cfg.Enabled() {
		// Single gate at the top — none of the registered rules can
		// send without Telegram configured, no point iterating.
		return
	}
	registryMu.Lock()
	rules := make([]ProactiveNotification, len(registry))
	copy(rules, registry)
	registryMu.Unlock()

	for _, p := range rules {
		fireOne(bot, db, cfg, baseURL, p)
	}
}

func fireOne(bot *Bot, db *storage.DB, cfg Config, baseURL string, p ProactiveNotification) {
	loc := cfg.location()
	now := time.Now().In(loc)

	// Hour gate. -1 means "any tick"; most rules use this.
	if p.HourOfDay >= 0 && now.Hour() != p.HourOfDay {
		return
	}

	// Cadence gate. Date stored as YYYY-MM-DD for the same reason
	// digest / nudge stored it pre-framework: NTP jitter can shift
	// a timestamp by seconds without crossing the cadence boundary
	// and trigger a duplicate fire. Date-string compare is bulletproof.
	key := proactiveSentKey(p.Name)
	if p.Cadence > 0 {
		last := db.GetSetting(key, "")
		if last == "" && p.LegacyKey != "" {
			// One-shot migration fallback; cleared on the next
			// successful send by the SaveSettings call at the
			// end of fireOne.
			last = db.GetSetting(p.LegacyKey, "")
		}
		if last != "" {
			if t, err := time.ParseInLocation("2006-01-02", last, loc); err == nil {
				if now.Sub(t) < p.Cadence {
					return
				}
			}
		}
	}

	// Eligibility check. 5s ctx mirrors digest / nudge pre-framework
	// — these are read-only queries against indexed tables, anything
	// slower is a sign of a stuck pool that shouldn't block the
	// morning report.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if p.Eligible != nil {
		ok, reason := p.Eligible(ctx, db, cfg)
		if !ok {
			// Reason logged at info level so ops can grep "skipped
			// proactive_<name>" if a user reports an expected nudge
			// not arriving. Volume is low (≤ once per morning per
			// tenant per rule).
			if reason != "" {
				log.Printf("proactive %s: skip (%s)", p.Name, reason)
			}
			return
		}
	}

	if p.Render == nil {
		log.Printf("proactive %s: no Render fn, skipping", p.Name)
		return
	}
	msg, err := p.Render(ctx, db, cfg, baseURL)
	if err != nil {
		log.Printf("proactive %s: render error: %v", p.Name, err)
		return
	}
	if msg == "" {
		// Render explicitly chose not to fire — usually a second
		// gate the eligibility couldn't express cleanly. Don't
		// persist a "sent" marker because nothing was sent.
		return
	}

	if err := bot.Send(msg); err != nil {
		log.Printf("proactive %s: send error: %v", p.Name, err)
		return
	}
	// Persist the date marker so the cadence gate skips us until
	// next interval. Best-effort — a write failure here means we'll
	// re-fire tomorrow's tick, which is harmless duplication for
	// daily-cadence rules and a slightly-noisy weekly digest at
	// worst. Don't log the write error because GetSetting/SaveSettings
	// have their own internal logging.
	db.SaveSettings(map[string]string{key: now.Format("2006-01-02")})
}

// proactiveSentKey is the settings key for the last-sent date of
// notification `name`. Exposed at package scope so digest.go and
// energy_nudge.go's init() helpers can pre-clean / migrate from
// the pre-framework keys (digestSentSettingKey, energyBackfillNudgeSentKey).
func proactiveSentKey(name string) string {
	return "proactive_" + name + "_last_sent"
}
