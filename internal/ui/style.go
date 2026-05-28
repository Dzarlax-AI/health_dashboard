package ui

const cssStyle = `
:root {
  /* Bridge aliases: health-specific names → DS tokens */
  --surface2: var(--surface-2);
  --muted: var(--text-tertiary);
  --fair: var(--warn);
  --fair-bg: var(--warn-bg);
  --low: var(--danger);
  --low-bg: var(--danger-bg);
  /* Admin legacy aliases */
  --card-bg: var(--surface);
  --card-border: var(--border);
  --fg: var(--text);
}
body { min-height: 100vh; font-size: 15px; }

/* ── Top bar ── */
#top-bar {
  padding: 20px 40px;
  display: flex; align-items: center; justify-content: space-between;
  max-width: 1400px; margin: 0 auto;
}
#top-bar-left { display: flex; align-items: center; gap: 10px; }
#top-bar-left svg { color: var(--heart); }
#top-bar-title { font-size: 17px; font-weight: 700; letter-spacing: -0.3px; }
#top-menu-toggle {
  display: none; align-items: center; justify-content: center;
  width: 42px; height: 42px; border: 1px solid var(--border);
  border-radius: var(--radius-xs); background: var(--surface);
  color: var(--text-secondary); box-shadow: var(--shadow); cursor: pointer;
}
#top-menu-toggle:hover { background: var(--surface2); color: var(--text); }
.top-btn {
  background: var(--surface); border: 1px solid var(--border); color: var(--text-secondary);
  padding: 8px 18px; border-radius: var(--radius-xs); cursor: pointer; font-size: 13px;
  font-weight: 500; transition: all 0.15s; display: flex; align-items: center; gap: 6px;
  box-shadow: var(--shadow);
}
.top-btn:hover { background: var(--surface2); color: var(--text); }
.top-admin-label { display: none; }
#top-bar-right { display: flex; align-items: center; gap: 8px; }
#top-bar-right::before { content: ''; display: block; }
.lang-toggle {
  background: none; border: none; box-shadow: none;
  padding: 4px 8px; font-size: 11px; font-weight: 700; letter-spacing: 0.8px;
  color: var(--text-secondary); opacity: 0.6; min-width: unset;
  border-left: 1px solid var(--border); border-radius: 0; margin-left: 4px;
}
.lang-toggle:hover { background: none; color: var(--text); opacity: 1; }

/* ── App container ── */
#app { max-width: 1400px; margin: 0 auto; padding: 0 40px 80px; }

/* ── HERO / TODAY ribbon ──
   Single hero band that combines the readiness score, the cross-metric
   Headline (chip + detail), the Energy Bank verdict + battery, and the
   30-day sparkline. Severity colour is signalled by the top border only —
   keeps the page calm vs full-band tinting. */
#hero-section {
  margin-bottom: 32px;
  background: transparent;
  border-top: 4px solid var(--text);
  border-bottom: 1px solid var(--text);
  border-radius: 0;
  padding: 36px 48px;
  color: var(--text);
  position: relative;
  overflow: hidden;
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) minmax(260px, auto);
  column-gap: 40px;
  align-items: stretch;
  min-height: 220px;
}
#hero-section.headline--warning  { border-top-color: var(--fair); }
#hero-section.headline--positive { border-top-color: var(--good); }
#hero-section.headline--info     { border-top-color: var(--text); }
#hero-bg-glow-1 {
  position: absolute; top: -60px; right: 80px;
  width: 300px; height: 300px; border-radius: 50%;
  background: rgba(255,255,255,0.07); pointer-events: none;
}
#hero-bg-glow-2 {
  position: absolute; bottom: -80px; right: -40px;
  width: 400px; height: 400px; border-radius: 50%;
  background: rgba(255,255,255,0.05); pointer-events: none;
}
#hero-score-block { position: relative; z-index: 2; align-self: center; }
#readiness-label-top {
  font-size: 12px; font-weight: 700; text-transform: uppercase;
  letter-spacing: 2px; opacity: 0.65; margin-bottom: 8px;
}
#readiness-score {
  font-size: 88px; font-weight: 900; line-height: 1;
  letter-spacing: -3px; margin-bottom: 4px;
}
#readiness-status { font-size: 20px; font-weight: 700; opacity: 0.9; }

/* Methodology status badge — surfaces the honest provenance of a score
   (heuristic / experimental / validated floor / labeling framework).
   Intentionally muted so it never competes with the actual score or
   verdict pill; tooltip on hover carries the full explanation. */
.methodology-badge {
  display: inline-block;
  font-size: 9px;
  font-weight: 700;
  letter-spacing: 0.6px;
  text-transform: uppercase;
  padding: 2px 6px;
  border-radius: 4px;
  margin-left: 6px;
  vertical-align: middle;
  background: var(--surface2);
  color: var(--muted);
  border: 1px solid var(--border);
  cursor: help;
  white-space: nowrap;
}
.methodology-badge--heuristic         { background: var(--surface2); color: var(--muted); }

/* Webhook status badge on the settings page. Live-updates every 5s
   while state=pending; static otherwise. Colour-coded by state so the
   operator can scan a row of tenants and spot the failed one without
   reading text. */
.webhook-status-line {
  display: inline-flex; align-items: center; gap: 8px; flex-wrap: wrap;
}
.webhook-badge {
  display: inline-block;
  font-size: 12px; font-weight: 600;
  padding: 2px 8px; border-radius: 4px;
  letter-spacing: 0.2px;
}
.webhook-badge--ok       { background: var(--good-bg); color: var(--good); }
.webhook-badge--pending  { background: var(--fair-bg); color: var(--fair); }
.webhook-badge--failed   { background: var(--low-bg);  color: var(--low); }
.webhook-badge--deleted  { background: var(--surface2); color: var(--muted); }
.webhook-badge--unknown  { background: var(--surface2); color: var(--muted); }
.webhook-status-age {
  font-size: 12px; color: var(--text-secondary);
}
/* Description: raw Telegram API error text under the badge on failed.
   The reason chip is short and stable; the description is the actual
   "what went wrong" string — often names the fix (HTTPS, token typo).
   Muted so it reads as supporting info, not a second badge. */
.webhook-status-description {
  margin-top: 6px;
  font-size: 12px; color: var(--text-secondary);
  font-style: italic;
  max-width: 480px;
  line-height: 1.4;
}
.admin-btn--small {
  padding: 2px 10px; font-size: 12px;
}

/* Subjective morning check-in confirmation line. Rendered right under
   the hero section when the user has tapped a Telegram check-in
   button. Intentionally low-key — single muted line, no chips, no
   icons; the dashboard does not need a fifth verdict pill. */
.subjective-checkin-line {
  margin: 8px 0 16px 0;
  font-size: 13px;
  color: var(--text-secondary);
}
.subjective-checkin-label {
  margin-right: 6px;
}
.subjective-checkin-answer {
  font-weight: 600;
  color: var(--text);
}
.methodology-badge--experimental      { background: var(--fair-bg);  color: var(--fair); border-color: transparent; }
.methodology-badge--validated         { background: var(--good-bg);  color: var(--good); border-color: transparent; }
.methodology-badge--labeling          { background: var(--surface2); color: var(--text-secondary); }

/* Hero column 2: narrative — chip with headline title, detail paragraph,
   tip (only when there's no headline so they don't fight each other),
   and a verdict pill + reason from Energy Bank. */
#hero-narrative-block {
  position: relative; z-index: 2;
  display: flex; flex-direction: column; gap: 12px;
  align-self: center;
  padding: 0 24px;
  border-left: 1px solid var(--border);
  min-width: 0;
}
.hero-headline-chip {
  display: inline-flex; align-items: center; gap: 8px;
  font-size: 13px; font-weight: 700;
  padding: 4px 12px; border-radius: 999px;
  background: var(--surface2); color: var(--text);
  align-self: flex-start;
}
.hero-headline-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--muted); flex-shrink: 0; }
.headline--warning .hero-headline-chip  { background: var(--fair-bg); color: var(--fair); }
.headline--warning .hero-headline-dot   { background: var(--fair); }
.headline--positive .hero-headline-chip { background: var(--good-bg); color: var(--good); }
.headline--positive .hero-headline-dot  { background: var(--good); }
.headline--info .hero-headline-dot      { background: var(--muted); }
.hero-headline-detail {
  font-size: 15px; line-height: 1.55; color: var(--text);
  white-space: pre-line;
}
#readiness-tip {
  font-size: 13px; color: var(--text-secondary); opacity: 0.85; line-height: 1.5;
  white-space: pre-line;
}
.hero-verdict-row {
  display: flex; align-items: center; gap: 12px; flex-wrap: wrap;
  padding-top: 8px;
  border-top: 1px dashed var(--border);
}
.hero-verdict-reason {
  font-size: 13px; color: var(--text-secondary); line-height: 1.45; flex: 1;
}

/* v2.2 §4.3 stress-flag chips. Rendered as a tight row of inline tags
   under hero-verdict-row; colour-coded by severity so the eye gets the
   "anything red?" answer in <1 second.

     red  — illness_signature (safety-critical, forces rest verdict)
     yellow — recovery_debt (suppresses push_hard)
     blue — parasympathetic_rebound (interpretation, not a correction)
     gray — acute_stress / sustained_load (HR-derived diagnostics)
     muted — stale_stress / calibration_warmup / data_accruing (operational state)

   Hover tooltip carries the long-form description from i18n. */
.stress-flags-row {
  display: flex; flex-wrap: wrap; gap: 6px;
  padding-top: 4px;
}
.stress-flag {
  display: inline-block;
  font-size: 11px; font-weight: 600;
  padding: 2px 8px; border-radius: 10px;
  line-height: 1.5;
  cursor: pointer;
  border: none;
  font-family: inherit;
}
.stress-flag:focus-visible {
  outline: 2px solid var(--text);
  outline-offset: 1px;
}
.stress-flag--active {
  box-shadow: 0 0 0 2px var(--text);
}

/* Detail card revealed when a chip is tapped. Pre-renders all
   flag details inline (hidden); toggle handler swaps which one
   is visible. Section headings (h5) come from the i18n HTML
   block per flag. */
.stress-flag-detail {
  margin-top: 8px;
  padding: 12px 14px;
  background: var(--bg-secondary, rgba(0,0,0,0.03));
  border: 1px solid var(--border);
  border-radius: 8px;
  font-size: 13px;
  line-height: 1.5;
  color: var(--text-secondary);
}
.stress-flag-detail h5 {
  margin: 10px 0 2px;
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}
.stress-flag-detail h5:first-child {
  margin-top: 0;
}
.stress-flag-detail p {
  margin: 0 0 4px;
}
.stress-flag-detail p:last-child {
  margin-bottom: 0;
}
.stress-flag--illness_signature { background: var(--low-bg);  color: var(--low); }
.stress-flag--recovery_debt     { background: var(--fair-bg); color: var(--fair); }
.stress-flag--parasympathetic_rebound {
  background: var(--info-bg, var(--good-bg)); color: var(--info, var(--good));
}
.stress-flag--acute_stress,
.stress-flag--sustained_load {
  background: var(--bg-secondary, #f0f0f0); color: var(--text-secondary);
}
.stress-flag--stale_stress,
.stress-flag--calibration_warmup,
.stress-flag--data_accruing {
  background: transparent; color: var(--text-secondary); opacity: 0.7;
  border: 1px dashed var(--border);
}

/* Hero column 3: visual — Energy Bank battery on top, readiness sparkline
   on the bottom. Components drilldown sits beneath the battery (hidden
   in <details>). */
#hero-visual-block {
  position: relative; z-index: 2;
  display: flex; flex-direction: column; gap: 18px;
  align-self: center;
  padding-left: 24px;
  border-left: 1px solid var(--border);
}
.hero-energy { display: flex; flex-direction: column; gap: 6px; }
.hero-energy-header {
  display: flex; align-items: baseline; justify-content: space-between; gap: 12px;
}
.hero-energy-label {
  font-size: 11px; font-weight: 700; text-transform: uppercase;
  letter-spacing: 1.5px; opacity: 0.55;
}
.hero-energy-numbers { font-size: 18px; font-weight: 800; letter-spacing: -0.5px; }
.hero-energy-details { font-size: 12px; color: var(--text-secondary); }
.hero-energy-details summary { cursor: pointer; color: var(--muted); }
.hero-energy-details ul { list-style: none; padding-left: 0; margin-top: 6px; display: flex; flex-direction: column; gap: 4px; }
#energy-hourly-wrap {
  margin-top: 12px;
  padding-top: 10px;
  border-top: 1px solid var(--border);
}
.energy-hourly-label {
  font-size: 10px; font-weight: 700; text-transform: uppercase;
  letter-spacing: 1.5px; opacity: 0.55; margin-bottom: 6px;
  color: var(--muted);
}
#energy-hourly-chart { width: 100% !important; max-height: 140px; }

#hero-sparkline-block {
  cursor: pointer; position: relative;
}
#hero-sparkline-label {
  font-size: 11px; font-weight: 700; text-transform: uppercase;
  letter-spacing: 1.5px; opacity: 0.55; margin-bottom: 6px;
}
#hero-sparkline-wrap {
  width: 100%; min-width: 220px; height: 64px; position: relative;
}

/* AI Insight: collapsed by default (closed <details>). */
#ai-insight-wrap {
  margin-bottom: 32px;
  border: 1px solid var(--border); border-radius: 8px;
  background: var(--surface);
}
#ai-insight-wrap > summary {
  cursor: pointer; padding: 12px 20px;
  font-size: 14px; font-weight: 600; color: var(--text);
}
#ai-insight {
  font-size: 15px; line-height: 1.6;
  font-style: italic;
  padding: 0 20px 16px;
  white-space: pre-line;
  color: var(--text-secondary);
}
#readiness-sparkline { display: block; width: 100% !important; height: 100% !important; }
#hero-date-strip {
  position: absolute; top: 24px; right: 40px; z-index: 2;
  font-size: 13px; opacity: 0.6;
}
.stale-badge {
  display: inline-block; background: var(--border);
  font-size: 12px; font-weight: 600; padding: 3px 10px;
  border-radius: 8px; margin-left: 8px;
}

/* ── Metric cards row ── */
#metric-cards-grid {
  display: grid;
  grid-template-columns: repeat(5, 1fr);
  gap: 14px;
  margin-bottom: 32px;
}
.metric-card {
  background: var(--surface);
  border-radius: var(--radius-sm);
  padding: 24px 20px;
  cursor: pointer;
  transition: all 0.18s;
  box-shadow: var(--shadow);
  border: 1px solid transparent;
}
.metric-card:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-lg);
  border-color: var(--border);
}
.metric-card-icon {
  width: 44px; height: 44px; border-radius: 14px;
  display: flex; align-items: center; justify-content: center;
  margin-bottom: 16px;
}
.metric-card-icon svg { width: 22px; height: 22px; }
.metric-card-name {
  font-size: 12px; color: var(--muted); font-weight: 600;
  text-transform: uppercase; letter-spacing: 0.5px; margin-bottom: 6px;
}
.metric-card-value {
  font-size: 32px; font-weight: 800; color: var(--text);
  letter-spacing: -1px; line-height: 1; margin-bottom: 4px;
}
.metric-card-unit { font-size: 12px; color: var(--muted); margin-bottom: 10px; }
.metric-card-badge {
  font-size: 11px; font-weight: 600; color: var(--muted);
  background: var(--surface-2, rgba(127,127,127,0.12));
  padding: 2px 8px; border-radius: 10px; margin-left: 4px;
  letter-spacing: 0; vertical-align: middle;
}
.metric-card-trend {
  font-size: 12px; font-weight: 700; padding: 3px 10px;
  border-radius: 20px; display: inline-block;
}
.metric-card-trend.positive { background: var(--good-bg); color: var(--good); }
.metric-card-trend.negative { background: var(--low-bg); color: var(--low); }
.metric-card-trend.neutral { background: var(--surface2); color: var(--muted); }
.metric-card-trends { display: flex; flex-wrap: wrap; gap: 6px; }
.metric-card-trend--secondary { opacity: 0.85; font-size: 11px; padding: 2px 8px; }
.metric-card-sparkline {
  height: 36px; margin: 6px 0 8px; position: relative;
}
.metric-card-sparkline canvas { width: 100% !important; height: 100% !important; }

/* ── Energy Bank shared chips (used inline in #hero-narrative-block) ── */
.energy-bar {
  position: relative; height: 14px; border-radius: 7px;
  background: var(--surface2); overflow: hidden;
}
.energy-bar-fill {
  height: 100%; transition: width 0.4s ease, background 0.4s ease;
  background: linear-gradient(to right, var(--low) 0%, var(--fair) 35%, var(--good) 65%);
}
/* Per-level fill colours: discrete bands (red→amber→yellow→green) so the
   bar's colour itself signals the state, independent of the numeric label.
   Aligned with EnergyBank.Level() thresholds in internal/health/types.go. */
.energy-bar-fill--critical { background: var(--low); }
.energy-bar-fill--low      { background: linear-gradient(to right, var(--low), var(--warn, var(--fair))); }
.energy-bar-fill--medium   { background: linear-gradient(to right, var(--warn, var(--fair)), var(--fair)); }
.energy-bar-fill--good     { background: linear-gradient(to right, var(--fair), var(--good)); }
.energy-bar-marker {
  position: absolute; top: -2px; bottom: -2px; width: 2px;
  background: var(--text); opacity: 0.6;
}
.hero-energy-state {
  display: flex; flex-wrap: wrap; align-items: baseline; gap: 6px 8px;
  font-size: 12px; line-height: 1.4; margin: 2px 0 4px;
}
.hero-energy-state-tag {
  font-size: 11px; font-weight: 700; padding: 2px 8px; border-radius: 999px;
  text-transform: uppercase; letter-spacing: 0.4px;
}
.hero-energy-state-tag--good     { background: var(--good-bg); color: var(--good); }
.hero-energy-state-tag--medium   { background: var(--fair-bg); color: var(--fair); }
.hero-energy-state-tag--low      { background: var(--warn-bg, var(--fair-bg)); color: var(--warn, var(--fair)); }
.hero-energy-state-tag--critical { background: var(--low-bg);  color: var(--low); }
.hero-energy-state-desc { color: var(--text-secondary); }
.energy-verdict {
  font-size: 12px; font-weight: 700; padding: 4px 12px; border-radius: 999px;
  display: inline-block;
}
.energy-verdict--push_hard       { background: var(--good-bg); color: var(--good); }
.energy-verdict--moderate        { background: var(--fair-bg); color: var(--fair); }
.energy-verdict--active_recovery { background: var(--warn-bg, var(--fair-bg)); color: var(--warn, var(--fair)); }
.energy-verdict--rest            { background: var(--low-bg);  color: var(--low); }
#energy-sparkline-wrap { height: 36px; margin-top: 6px; }
#energy-sparkline-wrap canvas { width: 100% !important; height: 100% !important; }
.muted { color: var(--muted); }


/* ── Two-column section: Correlation + Insights ── */
#correlation-insights-row {
  display: grid;
  grid-template-columns: 2fr 1fr;
  gap: 20px;
  margin-bottom: 20px;
  min-width: 0;
}
#correlation-section {
  background: var(--surface); border-radius: var(--radius);
  padding: 32px; box-shadow: var(--shadow);
  min-width: 0; overflow: hidden;
}
.section-header { margin-bottom: 24px; }
.section-title { font-size: 20px; font-weight: 700; letter-spacing: -0.4px; margin-bottom: 4px; }
.section-subtitle { font-size: 15px; font-weight: 600; color: var(--text); margin-bottom: 2px; }
.section-sub2 { font-size: 13px; color: var(--muted); }
#corr-legend { display: flex; gap: 20px; font-size: 12px; color: var(--muted); margin-top: 8px; }
.legend-item { display: flex; align-items: center; gap: 6px; font-weight: 500; }
.legend-dot { width: 10px; height: 10px; border-radius: 50%; }
#corr-chart-wrap { height: 220px; position: relative; overflow: hidden; max-width: 100%; }
#corr-chart-wrap canvas { max-width: 100%; }

/* ── Weekly section ── */
#weekly-section { margin-bottom: 32px; }
#weekly-section > .section-title { font-size: 20px; font-weight: 700; letter-spacing: -0.4px; margin-bottom: 20px; }

/* ── Insights panel ── */
#insights-panel {
  background: var(--surface); border-radius: var(--radius);
  padding: 32px; box-shadow: var(--shadow);
  min-width: 0; overflow: hidden;
}
#insights-list { list-style: none; display: flex; flex-direction: column; gap: 16px; }
#insights-list li {
  display: flex; gap: 14px; align-items: flex-start;
  font-size: 14px; color: var(--text-secondary); line-height: 1.55;
}
.insight-dot {
  width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; margin-top: 5px;
}
.insight-dot.positive { background: var(--good); }
.insight-dot.warning  { background: var(--fair); }

/* ── Health alerts ── */
#alerts-panel {
  display: flex; flex-direction: column; gap: 8px; margin-bottom: 20px;
}
/* alert--critical: health severity alias (DS uses --danger) */
.alert--critical { background: var(--danger-bg); color: #991b1b; border-color: #fca5a5; }
[dark-mode] .alert--critical { color: var(--danger); }

/* ── Sleep source comparison (used on /sleep page, not the dashboard) ── */
#sleep-sources { margin-top: 20px; }
.sleep-sources-header { font-size: 12px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.5px; color: var(--muted); margin-bottom: 10px; }
.sleep-src-table { display: flex; flex-direction: column; gap: 4px; }
.sleep-src-row {
  display: grid; grid-template-columns: 1fr repeat(4, 60px);
  align-items: center; gap: 8px;
  padding: 8px 12px; border-radius: var(--radius-xs); font-size: 13px;
}
.sleep-src-row:not(.sleep-src-head) { background: var(--surface2); }
.sleep-src-head { font-size: 11px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: 0.4px; padding-bottom: 4px; }
.sleep-src-head span:not(:first-child) { text-align: right; }
.sleep-src-row span:not(:first-child) { text-align: right; font-weight: 600; }
.sleep-src-name { display: flex; align-items: center; gap: 8px; font-weight: 600; overflow: hidden; }
.sleep-src-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
#sleep-chart-wrap { height: 180px; position: relative; flex: 1; }

/* ── Section detail cards (Recovery, Activity etc) ── */
#section-cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 16px;
  margin-bottom: 32px;
}
.insight-card {
  background: var(--surface); border-radius: var(--radius);
  padding: 28px; box-shadow: var(--shadow);
  border-top: 3px solid var(--border); transition: box-shadow 0.15s;
}
.insight-card:hover { box-shadow: var(--shadow-lg); }
.insight-card.status-good { border-top-color: var(--good); }
.insight-card.status-fair { border-top-color: var(--fair); }
.insight-card.status-low  { border-top-color: var(--low); }
.insight-header { display: flex; align-items: center; gap: 12px; margin-bottom: 12px; }
.insight-icon {
  width: 44px; height: 44px; border-radius: 14px;
  display: flex; align-items: center; justify-content: center; flex-shrink: 0;
}
.insight-title-wrap { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
.insight-card[data-key="recovery"] .insight-icon { background: var(--surface2); color: var(--text); }
.insight-card[data-key="sleep"]    .insight-icon { background: var(--surface2); color: var(--text); }
.insight-card[data-key="activity"] .insight-icon { background: var(--surface2); color: var(--text); }
.insight-card[data-key="cardio"]   .insight-icon { background: var(--surface2); color: var(--text); }
.insight-title { font-size: 16px; font-weight: 700; }
.insight-badge { font-size: 11px; font-weight: 700; padding: 3px 10px; border-radius: 10px; }
.status-good .insight-badge { background: var(--good-bg); color: var(--good); }
.status-fair .insight-badge { background: var(--fair-bg); color: var(--fair); }
.status-low  .insight-badge { background: var(--low-bg);  color: var(--low); }
.insight-summary { font-size: 14px; color: var(--text-secondary); line-height: 1.6; margin-bottom: 16px; }
.insight-details { display: flex; flex-direction: column; gap: 6px; }
.insight-detail {
  display: grid;
  grid-template-columns: 8px 1fr auto;
  grid-template-areas: "dot label value" "dot note note";
  row-gap: 2px; column-gap: 10px;
  padding: 8px 12px; background: var(--surface2); border-radius: var(--radius-xs);
  font-size: 13px;
}
.detail-indicator {
  grid-area: dot; align-self: center;
  width: 6px; height: 6px; border-radius: 50%; flex-shrink: 0;
}
.detail-indicator.up     { background: var(--good); }
.detail-indicator.down   { background: var(--low); }
.detail-indicator.stable { background: var(--muted); }
.detail-label { grid-area: label; font-weight: 600; align-self: center; }
.detail-value { grid-area: value; color: var(--text); white-space: nowrap; align-self: center; }
.detail-note  { grid-area: note;  font-size: 11px; color: var(--muted); line-height: 1.4; }

/* ── Metric cards area ── */
#metric-cards-area { margin-bottom: 32px; }
#metric-cards-area > .section-title { font-size: 20px; font-weight: 700; letter-spacing: -0.4px; margin-bottom: 20px; }

/* ── Section detail cards header ── */
#sections-area { margin-bottom: 32px; }
#sections-area > .section-title { font-size: 20px; font-weight: 700; letter-spacing: -0.4px; margin-bottom: 20px; }

/* ── Metrics view ── */
#metrics-view { padding-top: 8px; }
#metrics-header {
  display: flex; align-items: center; gap: 20px; margin-bottom: 32px;
  flex-wrap: wrap;
}
#metrics-back {
  background: none; border: none; color: var(--accent); cursor: pointer;
  font-size: 15px; font-weight: 600; padding: 0;
  display: flex; align-items: center; gap: 6px; flex-shrink: 0;
}
#metrics-back:hover { text-decoration: underline; }
#metrics-title { font-size: 24px; font-weight: 800; letter-spacing: -0.5px; flex: 1; }
#metrics-search-wrap {
  display: flex; align-items: center; gap: 10px;
  background: var(--surface); border: 1px solid var(--border);
  border-radius: 10px; padding: 10px 16px; min-width: 240px;
  box-shadow: var(--shadow);
}
#metrics-search-wrap svg { color: var(--muted); flex-shrink: 0; }
#metrics-search {
  border: none; outline: none; background: transparent;
  font-size: 15px; color: var(--text); width: 100%;
}
#metrics-search::placeholder { color: var(--muted); }
.metrics-cat-section { margin-bottom: 32px; }
.metrics-cat-label {
  font-size: 12px; font-weight: 700; color: var(--muted);
  margin-bottom: 14px; display: flex; align-items: center; gap: 8px;
  text-transform: uppercase; letter-spacing: 0.5px;
}
.metrics-cat-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
.metrics-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(170px, 1fr)); gap: 12px; }
.metrics-card {
  background: var(--surface); border: 1px solid var(--border-light);
  border-radius: var(--radius-sm); padding: 18px 16px; cursor: pointer; transition: all 0.15s;
}
.metrics-card:hover { border-color: var(--accent); box-shadow: var(--shadow); transform: translateY(-1px); }
.metrics-card-name {
  font-size: 12px; font-weight: 600; color: var(--muted);
  text-transform: uppercase; letter-spacing: 0.3px; margin-bottom: 10px;
}
.metrics-card-bottom { display: flex; align-items: baseline; gap: 6px; flex-wrap: wrap; }
.metrics-card-value { font-size: 26px; font-weight: 800; color: var(--text); letter-spacing: -0.5px; }
.metrics-card-unit { font-size: 12px; color: var(--muted); }
.metrics-card-empty { font-size: 22px; color: var(--muted); }
.metrics-card-trend { font-size: 12px; font-weight: 700; margin-left: auto; }
.metrics-card-trend.up      { color: var(--good); }
.metrics-card-trend.down    { color: var(--low); }
.metrics-card-trend.neutral { color: var(--muted); }
.metrics-empty { padding: 40px; color: var(--muted); text-align: center; font-size: 15px; }

/* ── Chart view ── */
#chart-view { }
#chart-back {
  background: none; border: none; color: var(--accent); cursor: pointer;
  font-size: 15px; font-weight: 600; padding: 0; margin-bottom: 24px;
  display: flex; align-items: center; gap: 6px;
}
#chart-back:hover { text-decoration: underline; }
#chart-title-row { display: flex; align-items: baseline; gap: 12px; margin-bottom: 24px; }
#chart-metric-name { font-size: 28px; font-weight: 800; letter-spacing: -0.6px; }
#chart-period { font-size: 14px; color: var(--muted); }
#chart-controls { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 20px; }
.presets { display: flex; gap: 4px; }
.preset-btn {
  background: var(--surface); border: 1px solid var(--border); color: var(--text-secondary);
  padding: 6px 14px; border-radius: 10px; cursor: pointer; font-size: 13px; font-weight: 500; transition: all 0.15s;
}
.preset-btn:hover { background: var(--surface2); color: var(--text); }
.preset-btn.active { background: var(--accent); border-color: var(--accent); color: #fff; }
.ctrl-group { display: flex; align-items: center; gap: 6px; }
.ctrl-label { font-size: 12px; color: var(--muted); font-weight: 500; }
select, input[type=date] {
  background: var(--surface); border: 1px solid var(--border); color: var(--text);
  padding: 6px 10px; border-radius: 10px; font-size: 13px;
}
select:focus, input[type=date]:focus { outline: none; border-color: var(--accent); }
.toolbar-btn {
  background: var(--surface); border: 1px solid var(--border); color: var(--text-secondary);
  padding: 6px 14px; border-radius: 10px; cursor: pointer; font-size: 13px;
  display: flex; align-items: center; gap: 5px; transition: all 0.15s; font-weight: 500;
}
.toolbar-btn:hover { color: var(--text); border-color: var(--text-secondary); }
.toolbar-btn.active { background: var(--accent); border-color: var(--accent); color: #fff; }
.shift-btns { display: flex; gap: 3px; }
.shift-btns button {
  background: var(--surface); border: 1px solid var(--border); color: var(--text-secondary);
  width: 32px; height: 32px; border-radius: 10px; cursor: pointer; font-size: 16px;
  display: flex; align-items: center; justify-content: center; transition: all 0.15s;
}
.shift-btns button:hover { color: var(--text); border-color: var(--text-secondary); }
#stats-row { display: flex; gap: 12px; flex-wrap: wrap; margin-bottom: 20px; }
#chart-wrap {
  position: relative; min-height: 360px; background: var(--surface);
  border: 1px solid var(--border); border-radius: var(--radius); padding: 24px;
}
#chart-loading {
  position: absolute; inset: 0; display: none; align-items: center;
  justify-content: center; background: var(--surface); border-radius: var(--radius); z-index: 10;
}

#briefing-loading { text-align: center; padding: 80px 20px; color: var(--muted); font-size: 16px; }
.loading-dots::after { content: ''; animation: dots 1.5s steps(4,end) infinite; }
@keyframes dots { 0% { content: ''; } 25% { content: '.'; } 50% { content: '..'; } 75% { content: '...'; } }

/* Inline spinner used by the onboarding wizard while Step 4 backfill /
   Step 6 calibration are running. Pairs with a status line so the
   operator sees the request is in flight (the button is also disabled
   for the duration). */
.spinner {
  display: inline-block;
  width: 14px; height: 14px;
  border: 2px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: spinner-rotate 0.7s linear infinite;
  vertical-align: -2px; margin-right: 6px;
}
@keyframes spinner-rotate { to { transform: rotate(360deg); } }

/* ── Responsive ── */
@media (max-width: 1100px) {
  /* Hero column 3 (visual) drops to a row beneath, narrative still flexes */
  #hero-section { grid-template-columns: auto minmax(0, 1fr); column-gap: 32px; row-gap: 20px; }
  #hero-visual-block { grid-column: 1 / -1; padding-left: 0; border-left: none; padding-top: 16px; border-top: 1px solid var(--border); flex-direction: row; gap: 24px; align-items: center; }
  #hero-visual-block > * { flex: 1; min-width: 0; }
  #metric-cards-grid { grid-template-columns: repeat(3, 1fr); }
  #correlation-insights-row { grid-template-columns: 1fr; }
}
@media (max-width: 768px) {
  #app { padding: 0 16px 48px; }
  #top-bar { padding: 14px 16px; }

  /* Hero — stack all three columns vertically */
  #hero-section {
    grid-template-columns: 1fr; gap: 20px; padding: 28px 22px;
    border-radius: 0; min-height: auto;
  }
  #hero-narrative-block { padding: 0; border-left: none; }
  #hero-visual-block { padding-left: 0; border-left: none; padding-top: 16px; border-top: 1px solid var(--border); flex-direction: column; }
  #hero-date-strip { position: static; margin-top: 8px; font-size: 13px; opacity: 0.7; }
  #readiness-score { font-size: 64px; }
  #readiness-tip { font-size: 15px; }

  /* Metrics */
  #metric-cards-grid { grid-template-columns: repeat(2, 1fr); gap: 10px; }
  .metric-card { padding: 18px 14px; }
  .metric-card-value { font-size: 26px; }
  .metric-card-sparkline { height: 32px; }

  /* Sections */
  .section-title { font-size: 17px; }
  #section-cards { grid-template-columns: 1fr; }
  #metric-cards-area > .section-title { font-size: 17px; }
  #weekly-section > .section-title { font-size: 17px; }
  #sections-area > .section-title { font-size: 17px; }

  /* Insight details */
  .detail-note { display: none; }
  .insight-card { padding: 20px; }

  /* Chart controls: scroll horizontally */
  #chart-controls { overflow-x: auto; flex-wrap: nowrap; padding-bottom: 4px; gap: 6px; -webkit-overflow-scrolling: touch; }
  #chart-controls::-webkit-scrollbar { display: none; }
  .presets { flex-shrink: 0; }
  .ctrl-group { flex-shrink: 0; }

  /* Touch targets */
  .preset-btn { min-height: 40px; padding: 0 14px; }
  .toolbar-btn { min-height: 40px; }
  .shift-btns button { width: 40px; height: 40px; }
  .top-btn { min-height: 40px; }

  #chart-wrap { min-height: 260px; padding: 16px; }
  #corr-chart-wrap { height: 180px; }
}
@media (max-width: 480px) {
  #app { padding: 0 12px 40px; }
  #top-bar {
    position: relative; padding: 12px; gap: 10px; flex-wrap: wrap;
    align-items: center; max-width: none; width: 100%;
  }
  #top-bar-left { min-width: 0; flex: 1; }
  #top-bar-left a { min-width: 0; }
  #top-bar-left span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  #top-menu-toggle { display: inline-flex; flex-shrink: 0; }
  #top-bar-right {
    display: none; position: absolute; z-index: 20; top: calc(100% - 4px); left: 12px; right: 12px;
    flex-direction: column; align-items: stretch; gap: 6px; padding: 8px;
    background: var(--surface); border: 1px solid var(--border); border-radius: 8px;
    box-shadow: var(--shadow); max-width: calc(100vw - 24px);
  }
  #top-bar-right.open { display: flex; }
  #top-bar-right::before { display: none; }
  #top-bar-right .top-btn {
    width: 100%; min-height: 40px; justify-content: flex-start; box-shadow: none;
    border-radius: 6px; padding: 9px 12px;
  }
  .top-admin-label { display: inline; }
  #top-bar-right .lang-toggle {
    width: auto; display: inline-flex; justify-content: center; min-height: 32px;
    padding: 6px 10px; margin-left: 0; border: 1px solid var(--border);
  }
  #hero-section { padding: 22px 16px; }
  #readiness-score { font-size: 56px; letter-spacing: -2px; }
  #readiness-status { font-size: 18px; }
  #readiness-label-top { font-size: 11px; }

  #metric-cards-grid { grid-template-columns: 1fr 1fr; gap: 8px; }
  .metric-card { padding: 14px 12px; }
  .metric-card-icon { width: 36px; height: 36px; border-radius: 10px; margin-bottom: 10px; }
  .metric-card-value { font-size: 22px; }
  .metric-card-unit { font-size: 11px; }
  .metric-card-trend { font-size: 10px; padding: 2px 7px; }

  .metrics-grid { grid-template-columns: 1fr 1fr; gap: 8px; }
  .metrics-card { padding: 12px; }
  .metrics-card-value { font-size: 20px; }
  #metrics-title { font-size: 20px; }
  #metrics-search-wrap { min-width: 0; width: 100%; }
}


/* ── Section detail view ── */
#section-view { }
#section-back {
  background: none; border: none; color: var(--accent); cursor: pointer;
  font-size: 15px; font-weight: 600; padding: 0; margin-bottom: 24px;
  display: flex; align-items: center; gap: 6px;
}
#section-back:hover { text-decoration: underline; }
.sec-header {
  background: var(--surface); border-radius: var(--radius); padding: 28px 32px;
  box-shadow: var(--shadow); margin-bottom: 20px;
}
.sec-title-row { display: flex; align-items: center; gap: 14px; margin-bottom: 10px; }
.sec-icon {
  width: 48px; height: 48px; border-radius: 16px; flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
}
.sec-icon-recovery  { background: var(--warn-bg); color: var(--warn); }
.sec-icon-sleep     { background: #ede9fe; color: var(--sleep); }
.sec-icon-activity  { background: #d1fae5; color: var(--activity); }
.sec-icon-cardio    { background: #dbeafe; color: var(--cardio); }
.sec-title { font-size: 24px; font-weight: 800; letter-spacing: -0.5px; flex: 1; }
.sec-status-badge {
  font-size: 12px; font-weight: 700; padding: 4px 12px; border-radius: 12px; flex-shrink: 0;
}
.sec-status-badge.status-good { background: var(--good-bg); color: var(--good); }
.sec-status-badge.status-fair { background: var(--fair-bg); color: var(--fair); }
.sec-status-badge.status-low  { background: var(--low-bg);  color: var(--low); }
.sec-summary { font-size: 15px; color: var(--text-secondary); line-height: 1.6; }
.sec-detail-block {
  background: var(--surface); border-radius: var(--radius); padding: 20px 24px;
  box-shadow: var(--shadow); margin-bottom: 20px; display: flex; flex-direction: column; gap: 8px;
}
.sec-sleep-stats {
  background: var(--surface); border-radius: var(--radius); padding: 20px 24px;
  box-shadow: var(--shadow); margin-bottom: 20px;
  display: grid; grid-template-columns: repeat(4, 1fr);
}
.sec-sleep-stats .sleep-stat + .sleep-stat { border-left: 1px solid var(--border-light); }
.sec-charts-area {
  display: grid; grid-template-columns: 1fr 1fr; gap: 20px; margin-bottom: 32px;
}
.sec-chart-block {
  background: var(--surface); border-radius: var(--radius); padding: 24px;
  box-shadow: var(--shadow);
}
.sec-chart-title {
  font-size: 14px; font-weight: 700; color: var(--text); margin-bottom: 16px;
  display: flex; align-items: baseline; gap: 6px;
}
.sec-chart-unit { font-size: 12px; color: var(--muted); font-weight: 500; }
.sec-chart-wrap { height: 220px; position: relative; }
.sec-explain-area { margin-bottom: 40px; }
.sec-explain-heading {
  font-size: 20px; font-weight: 700; letter-spacing: -0.4px; margin-bottom: 20px;
}
.sec-explain-grid {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 16px;
}
.sec-explain-card {
  background: var(--surface); border-radius: var(--radius-sm); padding: 24px;
  box-shadow: var(--shadow); border-left: 3px solid var(--border);
}
.sec-explain-title { font-size: 15px; font-weight: 700; margin-bottom: 8px; color: var(--text); }
.sec-explain-body { font-size: 13px; color: var(--text-secondary); line-height: 1.65; }
.sec-card-chevron { color: var(--muted); flex-shrink: 0; margin-left: auto; }
.insight-card { cursor: pointer; }
.insight-card:hover .sec-card-chevron { color: var(--accent); }
@media (max-width: 768px) {
  .sec-charts-area { grid-template-columns: 1fr; }
  .sec-sleep-stats { grid-template-columns: repeat(2, 1fr); gap: 12px; }
  .sec-sleep-stats .sleep-stat + .sleep-stat { border-left: none; }
  .sec-sleep-stats .sleep-stat { background: var(--surface2); border-radius: var(--radius-xs); padding: 12px; }
  .sec-header { padding: 20px; }
  .sec-detail-block { padding: 16px; }
  .sec-title { font-size: 20px; }
}
@media (max-width: 480px) {
  .sec-charts-area { gap: 12px; }
  .sec-explain-grid { grid-template-columns: 1fr; }
}

/* ── Admin / Settings view ── */
#admin-view { padding-top: 8px; }
#admin-header {
  display: flex; align-items: center; gap: 16px; margin-bottom: 28px;
}
#admin-header .back-btn {
  background: none; border: none; color: var(--accent); cursor: pointer;
  font-size: 15px; font-weight: 600; padding: 0;
  display: flex; align-items: center; gap: 6px;
}
#admin-header .back-btn:hover { text-decoration: underline; }
#admin-header .view-title { font-size: 24px; font-weight: 800; letter-spacing: -0.5px; }
#admin-loading { display: flex; justify-content: center; padding: 40px; }
.admin-section { margin-bottom: 14px; }
.admin-scope-switcher {
  display: flex; flex-direction: column; gap: 12px; margin: 0 0 18px;
}
.admin-scope-group {
  display: flex; flex-wrap: wrap; align-items: center; gap: 8px;
}
.admin-scope-label {
  width: 72px; flex: 0 0 72px;
  font-size: 11px; font-weight: 700; letter-spacing: 0.08em;
  text-transform: uppercase; color: var(--text-tertiary);
}
.admin-tabs { display: flex; flex-wrap: wrap; gap: 8px; margin: 0 0 18px; }
.admin-tab {
  border: 1px solid var(--border); background: var(--surface);
  color: var(--text-secondary); border-radius: 8px; padding: 8px 12px;
  font-size: 13px; cursor: pointer;
}
.admin-tab.active {
  border-color: var(--accent); background: var(--surface-2); color: var(--text);
}
.admin-tab-panel[hidden] { display: none; }
.admin-scope-banner {
  border: 1px solid var(--card-border); background: var(--surface-2);
  border-radius: 8px; padding: 12px 14px; margin-bottom: 18px;
  color: var(--text-secondary); font-size: 13px;
}
.admin-scope-banner code { color: var(--text); }
details.admin-section {
  border: 1px solid var(--card-border); border-radius: 8px;
  padding: 0; background: var(--card-bg); overflow: hidden;
}
details.admin-section > summary {
  list-style: none; cursor: pointer; padding: 14px 44px 14px 16px;
  display: flex; align-items: center; justify-content: flex-start;
  gap: 8px; text-align: left; position: relative;
}
details.admin-section > summary.admin-section-header { margin-bottom: 0; }
details.admin-section > summary::-webkit-details-marker { display: none; }
details.admin-section > summary::before {
  content: "›"; display: inline-flex; align-items: center; justify-content: center;
  width: 18px; color: var(--text-tertiary);
  transition: transform 120ms ease;
  position: absolute; right: 16px; top: 50%; margin-top: -9px;
}
details.admin-section > summary .section-title,
details.admin-section > summary.section-title {
  margin: 0; text-align: left;
}
details.admin-section > summary.admin-section-header .section-title {
  flex: 1; min-width: 0;
}
details.admin-section[open] > summary::before { transform: rotate(90deg); }
details.admin-section > summary + * { margin-top: 0; }
details.admin-section[open] { padding-bottom: 16px; }
details.admin-section[open] > :not(summary) { margin-left: 16px; margin-right: 16px; }
.admin-section-actions {
  display: flex; justify-content: flex-start; gap: 8px; flex-wrap: wrap;
  margin-bottom: 12px;
}
.admin-profile-tabs {
  display: flex; flex-wrap: wrap; gap: 8px; margin: 0 0 18px;
  padding-bottom: 12px; border-bottom: 1px solid var(--card-border);
}
.admin-profile-tab {
  border: 1px solid transparent; background: transparent;
  color: var(--text-secondary); border-radius: 8px; padding: 7px 10px;
  font-size: 13px; font-weight: 600; cursor: pointer;
}
.admin-profile-tab:hover { background: var(--surface-2); color: var(--text); }
.admin-profile-tab.active {
  border-color: var(--accent); background: var(--surface-2); color: var(--text);
}
.admin-profile-panel[hidden] { display: none; }
.admin-overview-grid {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 10px; margin: 0 0 22px;
}
.admin-overview-card {
  border: 1px solid var(--card-border); background: var(--card-bg);
  border-radius: 8px; padding: 12px 14px; text-align: left;
  cursor: pointer; color: var(--text); min-height: 78px;
}
.admin-overview-card:hover { border-color: var(--accent); background: var(--surface-2); }
.admin-overview-card span {
  display: block; font-size: 11px; font-weight: 700; letter-spacing: 0.06em;
  text-transform: uppercase; color: var(--text-tertiary); margin-bottom: 6px;
}
.admin-overview-card strong {
  display: block; font-size: 13px; line-height: 1.35; color: var(--text);
}
/* Admin page groups — added in the /admin reorg PR. Each group
   wraps related sections under one heading so the page scans as
   four concerns (Status, Operations, Configuration, Users) instead
   of nine flat sections. */
.admin-group { margin-bottom: 34px; }
.admin-group-header {
  font-size: 11px; font-weight: 700; letter-spacing: 1px;
  text-transform: uppercase; color: var(--text-tertiary);
  padding: 0 0 8px 0; margin-bottom: 16px;
  border-bottom: 1px solid var(--card-border);
}
.admin-group-desc { font-size: 13px; color: var(--text-secondary); margin-bottom: 16px; }
/* Onboarding wizard step cards. Each step is one row with a small
   numbered chip and a refresh / action button on the right. */
.onboarding-step { border: 1px solid var(--card-border); border-radius: 12px; padding: 12px 14px; margin-bottom: 12px; background: var(--card-bg); }
.onboarding-step-header { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
.onboarding-step-num { display: inline-flex; align-items: center; justify-content: center; width: 22px; height: 22px; border-radius: 50%; background: var(--accent); color: white; font-size: 12px; font-weight: 700; }
.onboarding-step-title { font-size: 14px; font-weight: 700; flex: 1; min-width: 0; }
.onboarding-step-body { font-size: 13px; color: var(--text-secondary); }
.onboarding-empty { font-size: 13px; color: var(--text-tertiary); font-style: italic; }
.onboarding-meta { display: flex; gap: 16px; flex-wrap: wrap; font-size: 13px; }
.onboarding-meta strong { color: var(--fg); margin-right: 4px; }
.admin-stat-grid {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 12px; margin-bottom: 16px;
}
.admin-stat-card {
  background: var(--card-bg); border: 1px solid var(--card-border);
  border-radius: 14px; padding: 16px;
  display: flex; align-items: flex-start; gap: 12px;
}
.admin-stat-icon { font-size: 22px; line-height: 1; }
.admin-stat-info { flex: 1; min-width: 0; }
.admin-stat-label { font-size: 13px; font-weight: 700; color: var(--fg); margin-bottom: 2px; }
.admin-stat-rows { font-size: 12px; color: var(--accent); font-weight: 600; }
.admin-stat-range { font-size: 11px; color: var(--muted); margin-top: 2px; }
.admin-meta-row {
  display: flex; gap: 24px; font-size: 13px; color: var(--muted); padding: 0 4px;
}
.admin-meta-row strong { color: var(--fg); }
.checkin-kpis {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 10px; margin-bottom: 10px;
}
.checkin-kpi {
  background: var(--card-bg); border: 1px solid var(--card-border);
  border-radius: 8px; padding: 10px 12px;
}
.checkin-kpi span {
  display: block; font-size: 11px; color: var(--muted);
  text-transform: uppercase; letter-spacing: 0.04em; margin-bottom: 3px;
}
.checkin-kpi strong { font-size: 18px; color: var(--fg); }
.checkin-answer-line { font-size: 13px; margin: 8px 0 12px; color: var(--text-secondary); }
.checkin-table td, .checkin-table th { padding: 7px 8px; }
.checkin-status {
  display: inline-block; font-size: 11px; font-weight: 700;
  padding: 2px 8px; border-radius: 999px;
}
.checkin-status--answered { background: var(--good-bg); color: var(--good); }
.checkin-status--late_answered { background: var(--fair-bg); color: var(--fair); }
.checkin-status--prompted { background: var(--surface2); color: var(--text-secondary); }
.checkin-status--expired { background: var(--low-bg); color: var(--low); }
.checkin-status--missing { background: transparent; color: var(--text-tertiary); border: 1px dashed var(--border); }
.admin-monitoring-block { margin-bottom: 16px; }
.admin-monitoring-head {
  display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
  font-size: 13px; color: var(--text-secondary); margin-bottom: 8px;
}
.admin-monitoring-head strong { color: var(--fg); font-size: 14px; }
.admin-actions { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 12px; }
.admin-action-card {
  background: var(--card-bg); border: 1px solid var(--card-border);
  border-radius: 8px; padding: 18px 20px;
}
.admin-action-title { font-size: 15px; font-weight: 700; margin-bottom: 6px; }
.admin-action-desc { font-size: 13px; color: var(--muted); margin-bottom: 14px; line-height: 1.5; }
#admin-msg, #admin-ai-msg, #admin-notify-msg { margin-top: 14px; }
/* Data integrity section */
.admin-section-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
.admin-section-header .section-title { margin-bottom: 0; }
/* Data gaps */
.admin-gaps-checking { font-size: 13px; color: var(--text-tertiary); padding: 8px 0; }
.admin-gaps-ok { font-size: 13px; color: var(--good); padding: 8px 0; }
.admin-gap-partial { font-size: 11px; color: var(--warn); background: var(--warn-bg); border-radius: 4px; padding: 1px 5px; margin-left: 4px; }
.admin-gaps-section { background: var(--warn-bg); border: 1px solid var(--warn) !important; }
.admin-gaps-title { font-weight: 600; font-size: 14px; color: var(--warn); margin-bottom: 10px; display: flex; align-items: center; gap: 6px; }
.admin-gaps-icon { font-size: 16px; }
.admin-gaps-list { display: flex; flex-direction: column; gap: 8px; }
.admin-gap-row { display: flex; align-items: center; gap: 10px; padding: 8px 12px; background: var(--warn-bg); border-radius: 8px; flex-wrap: wrap; }
.admin-gap-range { font-size: 13px; font-weight: 600; color: var(--warn); flex: 1; min-width: 0; }
.admin-gap-days { font-size: 12px; color: var(--warn); white-space: nowrap; }
.admin-gap-btn { padding: 4px 12px; border-radius: 6px; border: none; background: var(--warn); color: #fff; font-size: 12px; font-weight: 600; cursor: pointer; white-space: nowrap; }
.admin-gap-btn:hover { opacity: 0.85; }
.admin-gap-hint { margin-bottom: 10px; padding: 8px 12px; border-radius: 8px; background: var(--warn-bg); border: 1px solid var(--warn); font-size: 13px; color: var(--warn); }
.admin-gap-row-today { background: var(--warn-bg); border: 1px dashed var(--warn); }
.admin-gap-today { font-size: 13px; font-weight: 500; color: var(--warn); }
.admin-unconfigured { padding: 12px 16px; border-radius: 8px; font-size: 13px; color: var(--text-tertiary); background: var(--surface-2); border: 1px dashed var(--border); margin-top: 8px; }
.admin-settings-form { display: flex; flex-direction: column; gap: 10px; }
.admin-field-row { display: flex; flex-direction: column; gap: 4px; }
.admin-field-row-pair { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.admin-field-half { display: flex; flex-direction: column; gap: 4px; }
.admin-field-label { font-size: 12px; color: var(--muted); }
.admin-field-input { background: var(--surface); border: 1px solid var(--border); color: var(--text); padding: 8px 12px; border-radius: 8px; font-size: 14px; outline: none; width: 100%; }
.admin-field-input:focus { border-color: var(--accent); }
.admin-field-group-title { font-size: 12px; font-weight: 600; color: var(--text-tertiary); text-transform: uppercase; letter-spacing: 0.05em; margin-top: 6px; }
.admin-settings-actions { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 4px; }
.admin-table { width: 100%; border-collapse: collapse; }
.admin-table th,
.admin-table td { padding: 10px 12px; border-bottom: 1px solid var(--border); text-align: left; vertical-align: top; }
.admin-table th { color: var(--text-tertiary); font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; }
#admin-users-table { max-width: 100%; overflow-x: auto; }
#admin-users-table .admin-table { min-width: 640px; }
#admin-users-table td:nth-child(3) { min-width: 210px; }
.admin-api-key {
  display: inline-block; max-width: min(340px, 58vw); overflow: hidden;
  text-overflow: ellipsis; vertical-align: middle; font-size: 11px; margin-right: 8px;
}
.admin-import-body { display: flex; flex-direction: column; gap: 14px; }
.admin-import-desc { font-size: 13px; color: var(--muted); }
.admin-import-form { display: flex; flex-direction: column; gap: 10px; }
.admin-import-file-label { display: inline-block; padding: 8px 14px; background: var(--surface); border: 1px solid var(--border); border-radius: 8px; font-size: 13px; color: var(--text); cursor: pointer; max-width: 320px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.admin-import-file-label:hover { border-color: var(--accent); }
.admin-import-options { display: flex; gap: 16px; align-items: center; flex-wrap: wrap; }
.admin-import-opt-label { display: flex; align-items: center; gap: 6px; font-size: 13px; color: var(--muted); }
.admin-import-opt-label input[type=number] { background: var(--surface); border: 1px solid var(--border); color: var(--text); padding: 5px 8px; border-radius: 6px; font-size: 13px; outline: none; }
.admin-import-opt-label input[type=number]:focus { border-color: var(--accent); }
.import-progress-bar-track { height: 8px; background: var(--surface); border-radius: 4px; overflow: hidden; margin-bottom: 8px; }
.import-progress-bar-fill { height: 100%; background: var(--accent); border-radius: 4px; width: 0%; transition: width 0.4s ease; }
.import-status-text { font-size: 13px; color: var(--muted); }
.section-icon-recovery { color: var(--heart); }
.section-icon-sleep { color: var(--sleep); }
.section-icon-activity { color: var(--activity); }
.section-icon-cardio { color: var(--heart); }

/* kpi-indicator: map health domain trend names → DS colours */
.kpi-indicator.positive { background: var(--good); }
.kpi-indicator.negative { background: var(--danger); }
.kpi-indicator.stable   { background: var(--text-tertiary); }

.editorial-charts {
  display: grid; grid-template-columns: 1fr; gap: 40px; margin-bottom: 48px;
}
.editorial-chart-block {
  background: transparent; box-shadow: none; padding: 0; border: none;
  border-top: 2px solid var(--text); border-radius: 0;
  padding-top: 24px;
}
.editorial-chart-title {
  font-size: 18px; font-weight: 700; letter-spacing: -0.3px; margin-bottom: 16px;
}

.editorial-explain-area { margin-bottom: 60px; border-top: 1px solid var(--border); padding-top: 32px; }
.editorial-explain-heading { font-size: 24px; font-weight: 800; letter-spacing: -0.5px; margin-bottom: 32px; }
.editorial-explain-grid {
  display: grid; grid-template-columns: 1fr 1fr; gap: 40px;
}
.editorial-explain-card {
  background: transparent; box-shadow: none; padding: 0; border: none;
  display: flex; flex-direction: column; gap: 8px;
}
.editorial-explain-title { font-size: 15px; font-weight: 700; color: var(--text); border-bottom: 1px solid var(--border-light); padding-bottom: 8px; }
.editorial-explain-body { font-size: 14px; color: var(--text-secondary); line-height: 1.6; }

@media (max-width: 768px) {
  .kpi-bar { flex-direction: column; gap: 16px; padding: 16px 0; }
  .kpi-item { border-right: none; border-bottom: 1px solid var(--border-light); padding: 0 0 16px 0; }
  .kpi-item:last-child { border-bottom: none; padding-bottom: 0; }
  .editorial-explain-grid { grid-template-columns: 1fr; gap: 24px; }
}
`
