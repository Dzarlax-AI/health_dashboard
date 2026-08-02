# Friendly Health Dashboard Design

**Date:** 2026-08-01
**Status:** Approved and implemented in `codex/friendly-health-dashboard`;
repeat-review fixes implemented and verified
**Scope:** Main dashboard as the first implementation of an application-wide
web design direction

## Summary

Redesign the main dashboard so it feels calmer and more human without reducing
the amount, precision, or discoverability of health data.

The selected direction combines:

- a scenic background in the “Today” area;
- one clear recommendation for the day;
- Readiness as the primary hero score, supported by Energy and Sleep Quality;
- compact supporting metrics and drill-downs below the hero;
- the existing analytical sections, charts, metric pages, and historical data.

This is an evolution of the current web interface rather than a clone of Bevel.
The dashboard is the first and most expressive application of a reusable visual
system that can later extend to metric details, Settings, Admin, login, and
import flows. The scenic visual language stays concentrated in the top
daily-summary area. Reusable surfaces, typography, spacing, controls, and state
patterns establish the shared direction for the rest of the product.

## Problem

The current hero exposes several independently correct signals but does not
resolve them into one understandable answer. A user can simultaneously see:

- a low or provisional readiness score;
- a positive HRV headline;
- a moderate-activity hint;
- a “push hard” Energy Bank verdict;
- missing-input and data-accrual statuses.

The result is technically transparent but cognitively expensive. The interface
asks the user to reconcile the models themselves.

Sleep is also underrepresented. Duration and stages are available deeper in the
dashboard, but there is no first-class summary of sleep quality alongside
Readiness and Energy.

## Design Goals

1. Answer “What should I do today?” within the first screen.
2. Preserve exact values and make the underlying evidence easy to inspect.
3. Show uncertainty honestly without making it the primary message.
4. Make Sleep Quality a first-class daily score with an understandable
   breakdown.
5. Keep the dashboard useful for both quick morning checks and deeper analysis.
6. Preserve the existing localization, tenant boundaries, accessibility, and
   responsive behavior.
7. Establish reusable design primitives so later pages can adopt the same
   visual language without duplicating dashboard-specific CSS.

## Non-Goals

- Rebuilding the entire application in Bevel’s mobile design language.
- Redesigning Admin, Settings, login, import, or metric-detail pages in the
  first implementation phase. They are expected follow-up surfaces, not
  permanently excluded from the new direction.
- Changing the scientific or heuristic meaning of Readiness or Energy Bank.
- Introducing new health claims, diagnostic language, or AI-generated numeric
  scores.
- Replacing detailed charts with decorative visualizations.
- Showing a scenic background behind every section.

## Considered Directions

### 1. Fully immersive Bevel-style dashboard

Use scenic backgrounds, large circular gauges, glass surfaces, and rounded
cards throughout the page.

**Rejected because:** it would feel like a different product, reduce density,
and make long charts and analytical sections harder to read.

### 2. Cosmetic softening of the current dashboard

Keep the existing layout and only adjust color, radii, shadows, and typography.

**Rejected because:** it would improve polish but leave the competing verdicts
and weak sleep hierarchy unresolved.

### 3. Friendly data-first hybrid

Use an expressive “Today” surface, then transition into compact analytical
cards using the current product’s information architecture.

**Selected because:** it creates emotional warmth where it matters while
preserving the dashboard’s data density and established drill-down paths.

## Information Architecture

The dashboard reads in five layers.

### 1. Application header

Keep a compact web header with the current product name and existing navigation.
Do not copy Bevel’s mobile bottom navigation or device chrome.

### 2. Today hero

The hero uses the approved morning-meadow background with a light readability
wash. It contains:

- date and last-updated time;
- a quiet confidence indicator;
- the Readiness circular gauge;
- one recommendation heading;
- one short explanation connecting the recommendation to the available data.

The recommendation is the highest-priority text. Missing or incomplete inputs
appear as confidence context, not as the hero headline.

### 3. Daily score row

Show two equally structured supporting cards below the hero:

1. **Energy** — current Energy Bank value, capacity, and state.
2. **Sleep Quality** — quality score, qualitative label, duration, and the most
   relevant supporting observation.

Each card links to its existing detailed view or the most appropriate existing
section. Scores use circular gauges, but the numeric value and text label remain
available to assistive technology.

### 4. Supporting evidence

Immediately below the hero, retain compact evidence cards for values such as
HRV, resting heart rate, sleep duration, steps, and activity. Exact content
continues to come from `BriefingResponse.MetricCards`.

The initial visible set should favor metrics that explain today’s
recommendation. All existing metrics remain accessible through the metric
browser.

### 5. Analysis and history

Keep the existing deeper surfaces:

- health alerts and illness suspicion;
- AI insight;
- health-section detail cards;
- weekly activity-versus-recovery analysis;
- historical metric charts and trends.

Their visual treatment becomes slightly softer and more consistent, but this
phase does not remove data or change their navigation.

## Application-Wide Design Direction

The dashboard should not become a polished island inside an unrelated product.
Its implementation must separate shared visual foundations from
dashboard-specific composition.

### Shared foundations

The following become reusable primitives for subsequent pages:

- semantic color roles for recovery, energy, sleep, caution, danger, and
  neutral information;
- type scale for page titles, recommendations, section headings, labels, and
  numeric data;
- spacing, radius, border, and shadow tokens;
- standard opaque and subtly tinted card surfaces;
- button, segmented-control, badge, disclosure, empty-state, loading, and
  freshness treatments;
- keyboard focus, reduced-motion, reduced-transparency, and high-contrast
  behavior;
- desktop, tablet, and mobile page-width conventions.

### Dashboard-specific expression

The scenic background, prominent daily gauge, and recommendation composition
belong to the Today hero. Other pages may use restrained color or illustration
but should not repeat the landscape hero merely for consistency.

### Gauge geometry contract

The Readiness gauge is a layout invariant. It must never visually drift,
stretch, or become off-center as text, locale, viewport width, or neighboring
content changes.

- Render the gauge inside an explicitly square wrapper using `aspect-ratio: 1`.
- Center the wrapper with layout primitives (`grid` or flex alignment), not
  positional nudges, margins, or breakpoint-specific transforms.
- Use one SVG coordinate system and a fixed `viewBox` for the track, progress
  arc, score, and label.
- Keep score and label centered in the same coordinate system; wrapping text
  must not participate in gauge geometry.
- Size the gauge with a bounded responsive value such as `clamp()` while
  preserving a 1:1 ratio.
- Do not compensate for adjacent recommendation copy with `translate`,
  negative margins, or absolute offsets.
- Apply the same reusable gauge primitive to Readiness, Energy, and Sleep
  Quality; differences are limited to value, color, label, and size token.
- Treat any visible center shift, ovalization, arc clipping, or unequal outer
  spacing as a release-blocking visual regression.

### Expected future migration

Once the dashboard direction is proven with real data, later phases may apply
the shared foundations in this order:

1. metric-detail and history pages;
2. Settings and import flows;
3. Admin pages and dense operational tables;
4. login and remaining utility states.

Each phase preserves the information density appropriate to the surface. Admin
tables should feel related to the dashboard without becoming decorative or
less efficient.

## Single Recommendation Contract

The UI must not independently interpret several verdicts. The health layer
produces one daily guidance object for the dashboard.

Proposed shape:

```go
type DashboardTodayGuidance struct {
    Action      string // rest | active_recovery | moderate | push_hard
    Label       string // localized through stable i18n keys
    Summary     string // short user-facing action
    Reason      string // concise evidence explanation
    Confidence  string // final | provisional | low
    UpdatedAt   *time.Time // nil until a payload is successfully processed
}
```

The function accepts the already-computed Energy Bank verdict,
`ReadinessServing`, illness evidence, and Sleep Quality state. It does not
recalculate the underlying scores.

Conservative precedence:

1. Existing illness safety caps remain authoritative.
2. `rest` and `active_recovery` are never promoted by the presentation layer.
3. A `push_hard` Energy Bank verdict is displayed as `moderate` while core
   Readiness evidence is missing, stale, low-confidence, or still accruing.
4. Final fresh evidence preserves the existing Energy Bank verdict.
5. Sleep Quality can explain or conservatively limit guidance, but it cannot
   promote an action above the existing Energy Bank verdict.

This removes contradictions such as “Requires attention” next to “Push hard”
without hiding why the recommendation is conservative.

## Sleep Quality

### User-facing meaning

Sleep Quality answers: “How restorative was the latest sleep period?” It is
separate from duration while still using duration as one component.

The score is displayed from 0 to 100 with a qualitative band:

- `80–100`: Restorative
- `60–79`: Good
- `40–59`: Mixed
- `0–39`: Poor

These labels describe the existing formula output; they are not medical
diagnoses.

### Calculation

Use the existing pure formula in `internal/health/energy_v2.go`:

```text
SleepQuality(total, deep, rem, awake) × 100
```

Do not create a second UI-only formula. Expose a pure breakdown helper from the
health package so the total and components cannot drift:

```go
type SleepQualityBreakdown struct {
    ScorePct      *int
    DurationPct   int
    ContinuityPct *int
    StructurePct  *int
    Confidence    string // final | partial | missing | low
}
```

`SleepQuality` should delegate to the same helper, preserving current Energy
Bank behavior and tests. Pointer fields are nil when the corresponding input is
not available, so partial sleep never serializes a fabricated zero score.

### Data-confidence states

- **Final:** same-night total, awake, deep, and REM are available and pass
  existing quality filters.
- **Partial:** total sleep is available, but stage data is absent or incomplete.
  Show duration and “Quality is still being refined”; do not show a precise
  0–100 quality score.
- **Missing:** no same-night sleep total is available. Show the last known night
  only inside details, never as today’s fresh score.
- **Low quality:** source data exists but fails the current readiness
  sleep-quality evidence checks. Show the score as low-confidence and explain
  which component is unreliable.

Coarse `sleep_unspecified` nights therefore remain useful without pretending
that stage quality is known.

### Breakdown

The Sleep Quality detail card shows:

- duration;
- continuity or efficiency;
- stage structure;
- total sleep time;
- deep and REM duration or proportion where available;
- freshness and confidence.

It must explain that Sleep Quality influences conservative readiness serving
but is still inspectable as its own score. The redesign does not silently add a
new weight to `ComputeReadinessScore`.

## Visual System

### Background

Keep the approved morning-meadow image in the Today hero. Apply a light
readability wash and ensure text remains legible without relying on
`backdrop-filter`.

The background is decorative:

- empty `alt`;
- no embedded UI or text;
- responsive crop;
- bundled into the application’s same-origin static assets.

### Surfaces

- Today hero: large scenic surface with restrained glass-like cards.
- Analytical areas: opaque white cards on the existing neutral page
  background.
- Other application pages: shared opaque surfaces and controls, without
  inheriting the scenic hero by default.
- Radius: larger in the hero, closer to the existing design-system radius in
  dense analytical sections.
- Shadows: soft and shallow; borders remain visible for high-contrast and
  reduced-transparency environments.

### Color

- Green: readiness and positive recovery context.
- Amber: energy and cautious states.
- Blue-violet: sleep quality.
- Red remains reserved for safety-critical alerts.

Every state must also have a text label; color is never the only signal.

### Typography

Use the existing system font stack. Reserve the largest display type for the
single daily recommendation. Numeric scores remain prominent but secondary to
the action.

## Responsive Behavior

### Desktop

- Hero: Readiness gauge and recommendation in two columns.
- Daily scores: three cards in one row.
- Supporting evidence: current multi-column metric grid.

### Tablet

- Hero remains two columns where space allows.
- Daily score cards may wrap to two plus one.

### Mobile

- Hero becomes one column.
- Readiness gauge appears before the recommendation.
- Daily scores stack vertically.
- The scenic background remains behind the complete hero, not each card.
- No horizontal scrolling is introduced.

## Empty, Loading, and Error States

### No health data

Show one onboarding message with the existing sync/import actions. Do not render
empty gauges.

### Partial daily data

Show the safest useful recommendation with a visible confidence label. Missing
metrics state what is still arriving and what will update afterward.

### Stale data

Preserve the current freshness thresholds and section-level stale banners.
Stale values are never styled as fresh daily scores.

### Calculation failure

Fall back independently:

- guidance unavailable → show Readiness and Energy values without an action;
- Sleep Quality unavailable → show sleep duration and confidence state;
- background unavailable → retain the same layout on a neutral surface.

One failed component must not blank the rest of the dashboard.

## Accessibility

- Use semantic headings and regions matching visual order.
- Provide accessible names for all circular gauges.
- Preserve visible keyboard focus on cards and disclosure controls.
- Maintain at least WCAG AA text contrast over the scenic image and translucent
  surfaces.
- Respect reduced-transparency preferences by switching glass surfaces to
  opaque cards.
- Do not animate gauges when reduced motion is requested.
- Status text must remain understandable without color.
- Confidence disclosures use buttons with `aria-expanded` and
  `aria-controls`.

## Localization

All new strings require `en`, `ru`, and `sr` entries. Stable enum keys remain in
English; user-facing labels come from the existing i18n maps.

Russian copy should use human action language such as “Умеренная нагрузка”
rather than exposing internal enum or methodology names in the primary layer.
Methodology remains available in details.

## Data Flow

```text
daily_scores + fresh metric reads
        ↓
GetHealthBriefing
        ↓
existing Readiness / Energy / illness calculations
        ↓
SleepQualityBreakdown + DashboardTodayGuidance
        ↓
BriefingResponse
        ↓
pageDashboard view model
        ↓
dashboard template + existing static chart bundle
```

The UI does not query the database directly and does not duplicate scoring
logic in JavaScript.

## Likely Code Boundaries

- `internal/health/energy_v2.go`
  - expose the shared Sleep Quality breakdown.
- `internal/health/types.go`
  - add response types for sleep quality and dashboard guidance.
- `internal/health/`
  - add the pure daily-guidance resolver and tests.
- `internal/storage/briefing.go`
  - populate the new response fields from the same data frame as the briefing.
- `internal/ui/handler.go`
  - map the new response fields into a presentation view model.
- `internal/ui/templates/pages/dashboard.html`
  - implement the approved information architecture.
- `internal/ui/style.go`
  - introduce reusable visual tokens and primitives, then implement the
    responsive dashboard treatment and accessibility fallbacks.
- `internal/ui/i18n_{en,ru,sr}.go`
  - add localized labels and explanations.
- `internal/ui/static/charts.js`
  - reuse or extend canvas rendering for accessible circular gauges where
    appropriate.
- `internal/ui/*_test.go`, `internal/health/*_test.go`
  - cover state, copy, and rendering contracts.

No stored score column is needed because the score is derived from existing
daily sleep fields. A partial index on successfully processed raw payloads
supports the hero's last-updated timestamp. The index is part of tenant schema
contract v5 and is built only by stopped-service fleet migration or pre-activation
tenant provisioning; runtime startup verifies it but never performs the
potentially blocking build.

## Testing

### Health logic

- Sleep Quality breakdown matches the existing `SleepQuality` result.
- Score and components stay within 0–100.
- Missing stages produce `partial`, not a fabricated precise score.
- Missing sleep produces `missing`.
- Guidance never promotes an existing conservative verdict.
- Provisional or missing readiness evidence caps `push_hard` to `moderate`.
- Final fresh evidence preserves existing Energy Bank behavior.
- Illness caps remain authoritative.

### Storage and handler

- New fields use the same date and source frame as the rest of the briefing.
- Stale or cross-day sleep is not presented as today’s fresh score.
- Existing coarse-sleep and multi-source dedup behavior is preserved.

### Template

- Final, provisional, partial-sleep, missing-sleep, stale, and no-data states.
- `en`, `ru`, and `sr` render without raw i18n keys.
- Gauge values have accessible names.
- Confidence disclosure semantics are correct.

### Visual and responsive

- Desktop, tablet, and mobile screenshots.
- No overflow at 320 px width.
- Gauge geometry checks at 320, 375, 768, 1024, and 1440 px: square bounds,
  centered arc, centered score/label, no clipping, and no movement caused by
  adjacent copy length.
- Gauge checks in `en`, `ru`, and `sr`, including the longest localized labels.
- Compare the gauge center and bounding box between fresh, provisional,
  partial, and missing-data states.
- Scenic-background contrast in light and dark display conditions.
- Reduced-motion and reduced-transparency fallbacks.
- Existing metric, section, AI insight, and weekly-analysis navigation remains
  reachable.

## Rollout

1. Add and test the pure response contracts without changing the template.
2. Add the new dashboard view model behind the existing server-rendered page.
3. Replace the hero and add the score row.
4. Lock the reusable gauge primitive with breakpoint and localization visual
   regression fixtures before reflowing surrounding content.
5. Reflow existing supporting sections without deleting functionality.
6. Verify with live tenant data across fresh, partial, and stale states.
7. Audit the resulting shared primitives against one dense page and one form
   page before planning the broader interface migration.

Rollback is limited to the template, CSS, and new optional response fields.
Existing scoring and storage schemas remain compatible.

Application-wide migration remains a separate implementation plan. The
dashboard phase must leave current pages functional and visually coherent while
they still use the older composition.

## Approval Record

The user approved the visual direction and the HTML implementation plan on
2026-08-01. Implementation was completed in the isolated branch
`codex/friendly-health-dashboard`; production deployment remains a separate
explicit action.
