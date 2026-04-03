package ui

const cssStyle = `
*, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
:root {
  --bg: #FCFAF7;
  --surface: #FFFFFF;
  --surface2: #E8E6E3;
  --border: rgba(26,26,30,0.08);
  --border-light: rgba(26,26,30,0.04);
  --text: #1A1A1E;
  --text-secondary: rgba(26, 26, 30, 0.7);
  --muted: rgba(26, 26, 30, 0.5);
  --accent: #1A1A1E;
  --good: #16a34a; --good-bg: #f0fdf4;
  --fair: #d97706; --fair-bg: #fffbeb;
  --low: #dc2626;  --low-bg: #fef2f2;
  --heart: #e11d48; --activity: #059669; --sleep: #7c3aed; --cardio: #0284c7;
  --shadow: 0 4px 6px -1px rgba(0,0,0,0.1), 0 2px 4px -2px rgba(0,0,0,0.06);
  --shadow-lg: 0 8px 30px rgba(0,0,0,0.04), 0 1px 2px rgba(0,0,0,0.06);
  --radius: 8px; --radius-sm: 4px; --radius-xs: 2px;
}
[dark-mode] {
  --bg: #1A1D21;
  --surface: #22252A;
  --surface2: #2A2D32;
  --border: rgba(255, 255, 255, 0.1);
  --border-light: rgba(255, 255, 255, 0.05);
  --text: #F8F9FA;
  --text-secondary: rgba(255, 255, 255, 0.7);
  --muted: rgba(255, 255, 255, 0.5);
  --accent: #E8E6E3;
}
body {
  font-family: -apple-system, BlinkMacSystemFont, "SF Pro Display", "SF Pro Text", system-ui, sans-serif;
  background: var(--bg); color: var(--text);
  min-height: 100vh; font-size: 15px; line-height: 1.5;
  -webkit-font-smoothing: antialiased;
}

/* ── Top bar ── */
#top-bar {
  padding: 20px 40px;
  display: flex; align-items: center; justify-content: space-between;
  max-width: 1400px; margin: 0 auto;
}
#top-bar-left { display: flex; align-items: center; gap: 10px; }
#top-bar-left svg { color: var(--heart); }
#top-bar-title { font-size: 17px; font-weight: 700; letter-spacing: -0.3px; }
.top-btn {
  background: var(--surface); border: 1px solid var(--border); color: var(--text-secondary);
  padding: 8px 18px; border-radius: var(--radius-xs); cursor: pointer; font-size: 13px;
  font-weight: 500; transition: all 0.15s; display: flex; align-items: center; gap: 6px;
  box-shadow: var(--shadow);
}
.top-btn:hover { background: var(--surface2); color: var(--text); }
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

/* ── HERO: Readiness ── */
#hero-section {
  margin-bottom: 32px;
  background: var(--text);
  border-radius: var(--radius);
  padding: 48px 56px;
  color: var(--bg);
  position: relative;
  overflow: hidden;
  display: grid;
  grid-template-columns: auto 1fr auto;
  column-gap: 48px;
  align-items: center;
  min-height: 240px;
}
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
#hero-score-block { position: relative; z-index: 2; }
#readiness-label-top {
  font-size: 12px; font-weight: 700; text-transform: uppercase;
  letter-spacing: 2px; opacity: 0.65; margin-bottom: 8px;
}
#readiness-score {
  font-size: 96px; font-weight: 900; line-height: 1;
  letter-spacing: -4px; margin-bottom: 4px;
}
#readiness-status { font-size: 22px; font-weight: 700; opacity: 0.9; }
#hero-right-block { position: relative; z-index: 2; }
#readiness-tip {
  font-size: 17px; opacity: 0.85; line-height: 1.5;
  margin-bottom: 28px;
}
#readiness-recovery {}
#recovery-bar-labels {
  display: flex; justify-content: space-between;
  font-size: 13px; opacity: 0.7; margin-bottom: 8px; font-weight: 500;
}
#recovery-bar-track {
  width: 100%; height: 8px; background: rgba(250,250,250,0.2);
  border-radius: 4px; overflow: hidden;
}
#recovery-bar-fill {
  height: 100%; background: var(--bg); border-radius: 4px;
  transition: width 0.8s cubic-bezier(0.4,0,0.2,1);
}
#hero-sparkline-block {
  cursor: pointer; position: relative; z-index: 2;
  padding-left: 40px;
  border-left: 1px solid rgba(255,255,255,0.15);
  align-self: center;
}
#hero-sparkline-label {
  font-size: 11px; font-weight: 700; text-transform: uppercase;
  letter-spacing: 1.5px; opacity: 0.55; margin-bottom: 10px;
}
#hero-sparkline-wrap {
  width: 220px; height: 90px; position: relative;
}
#readiness-sparkline { display: block; width: 100% !important; height: 100% !important; }
#hero-date-strip {
  position: absolute; top: 24px; right: 40px; z-index: 2;
  font-size: 13px; opacity: 0.6;
}
.stale-badge {
  display: inline-block; background: rgba(255,255,255,0.15);
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
.metric-card-trend {
  font-size: 12px; font-weight: 700; padding: 3px 10px;
  border-radius: 20px; display: inline-block;
}
.metric-card-trend.positive { background: var(--good-bg); color: var(--good); }
.metric-card-trend.negative { background: var(--low-bg); color: var(--low); }
.metric-card-trend.neutral { background: var(--surface2); color: var(--muted); }


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
.alert-item {
  display: flex; align-items: center; gap: 10px; padding: 12px 16px;
  border-radius: var(--radius); font-size: 14px; line-height: 1.4;
}
.alert-warning { background: #fef3c7; color: #92400e; border: 1px solid #fcd34d; }
.alert-critical { background: #fee2e2; color: #991b1b; border: 1px solid #fca5a5; }
.alert-icon { font-size: 16px; flex-shrink: 0; }
.alert-text { flex: 1; }

/* ── Sleep section (full width) ── */
#sleep-section {
  background: var(--surface); border-radius: var(--radius);
  padding: 32px; box-shadow: var(--shadow); margin-bottom: 20px;
}
#sleep-section .section-header { margin-bottom: 20px; }
#sleep-body {
  display: grid; grid-template-columns: 1fr auto;
  gap: 40px; align-items: center;
}
#sleep-stats-grid {
  display: grid; grid-template-columns: repeat(4, 1fr);
  gap: 0; border-top: 1px solid var(--border-light); margin-top: 20px;
  padding-top: 20px;
}
.sleep-stat { text-align: center; }
.sleep-stat + .sleep-stat { border-left: 1px solid var(--border-light); }
.sleep-stat-label { font-size: 12px; color: var(--muted); font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px; margin-bottom: 6px; }
.sleep-stat-value { font-size: 28px; font-weight: 800; color: var(--text); letter-spacing: -0.5px; }
.sleep-stat-value.accent { color: var(--good); }

/* ── Sleep source comparison ── */
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

/* ── Trend sparklines ── */
#trends-section { margin-bottom: 32px; }
#trends-section > .section-title { font-size: 20px; font-weight: 700; letter-spacing: -0.4px; margin-bottom: 20px; }
#trend-charts { display: grid; grid-template-columns: repeat(4, 1fr); gap: 16px; }
.trend-card {
  background: var(--surface); border-radius: var(--radius-sm);
  padding: 24px; box-shadow: var(--shadow); cursor: pointer; transition: all 0.18s;
}
.trend-card:hover { box-shadow: var(--shadow-lg); transform: translateY(-2px); }
.trend-card-header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 16px; }
.trend-card-title { font-size: 12px; font-weight: 600; color: var(--muted); text-transform: uppercase; letter-spacing: 0.5px; }
.trend-card-value { font-size: 24px; font-weight: 800; letter-spacing: -0.5px; }
.trend-card-canvas { height: 80px; overflow: hidden; position: relative; }
.trend-card-canvas canvas { max-width: 100%; }

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
.stat-chip {
  background: var(--surface); border: 1px solid var(--border);
  border-radius: var(--radius-sm); padding: 14px 20px;
}
.stat-chip .s-label { font-size: 11px; color: var(--muted); font-weight: 700; text-transform: uppercase; letter-spacing: 0.5px; }
.stat-chip .s-value { font-size: 24px; font-weight: 800; color: var(--accent); margin-top: 2px; letter-spacing: -0.5px; }
#chart-wrap {
  position: relative; min-height: 360px; background: var(--surface);
  border: 1px solid var(--border); border-radius: var(--radius); padding: 24px;
}
#chart-loading {
  position: absolute; inset: 0; display: none; align-items: center;
  justify-content: center; background: var(--surface); border-radius: var(--radius); z-index: 10;
}
.spinner {
  width: 32px; height: 32px; border: 3px solid var(--border);
  border-top-color: var(--accent); border-radius: 50%; animation: spin 0.7s linear infinite;
}
@keyframes spin { to { transform: rotate(360deg); } }
#briefing-loading { text-align: center; padding: 80px 20px; color: var(--muted); font-size: 16px; }
.loading-dots::after { content: ''; animation: dots 1.5s steps(4,end) infinite; }
@keyframes dots { 0% { content: ''; } 25% { content: '.'; } 50% { content: '..'; } 75% { content: '...'; } }

/* ── Responsive ── */
@media (max-width: 1100px) {
  #trend-charts { grid-template-columns: repeat(2, 1fr); }
  #metric-cards-grid { grid-template-columns: repeat(3, 1fr); }
  #correlation-insights-row { grid-template-columns: 1fr; }
  #sleep-stats-grid { grid-template-columns: repeat(4, 1fr); }
}
@media (max-width: 768px) {
  #app { padding: 0 16px 48px; }
  #top-bar { padding: 14px 16px; }

  /* Hero */
  #hero-section {
    grid-template-columns: 1fr; gap: 24px; padding: 32px 24px;
    border-radius: 24px; min-height: auto;
  }
  #hero-date-strip { position: static; margin-top: 8px; font-size: 13px; opacity: 0.7; }
  #readiness-score { font-size: 72px; }
  #readiness-tip { font-size: 15px; margin-bottom: 20px; }

  /* Metrics */
  #metric-cards-grid { grid-template-columns: repeat(2, 1fr); gap: 10px; }
  .metric-card { padding: 18px 14px; }
  .metric-card-value { font-size: 26px; }

  /* Sections */
  .section-title { font-size: 17px; }
  #section-cards { grid-template-columns: 1fr; }
  #metric-cards-area > .section-title { font-size: 17px; }
  #weekly-section > .section-title { font-size: 17px; }
  #sections-area > .section-title { font-size: 17px; }
  #trends-section > .section-title { font-size: 17px; }
  #trend-charts { grid-template-columns: 1fr 1fr; }

  /* Insight details */
  .detail-note { display: none; }
  .insight-card { padding: 20px; }

  /* Sleep */
  #sleep-stats-grid { grid-template-columns: repeat(2, 1fr); gap: 12px; border-top: none; padding-top: 0; }
  .sleep-stat { padding: 12px; background: var(--surface2); border-radius: var(--radius-xs); }
  .sleep-stat + .sleep-stat { border-left: none; }
  .sleep-stat-value { font-size: 22px; }

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
  #top-bar { padding: 12px; }
  #hero-section { padding: 24px 18px; border-radius: 20px; }
  #readiness-score { font-size: 60px; letter-spacing: -3px; }
  #readiness-status { font-size: 18px; }
  #readiness-label-top { font-size: 11px; }

  #metric-cards-grid { grid-template-columns: 1fr 1fr; gap: 8px; }
  .metric-card { padding: 14px 12px; }
  .metric-card-icon { width: 36px; height: 36px; border-radius: 10px; margin-bottom: 10px; }
  .metric-card-value { font-size: 22px; }
  .metric-card-unit { font-size: 11px; }
  .metric-card-trend { font-size: 10px; padding: 2px 7px; }

  #trend-charts { grid-template-columns: 1fr; }

  .metrics-grid { grid-template-columns: 1fr 1fr; gap: 8px; }
  .metrics-card { padding: 12px; }
  .metrics-card-value { font-size: 20px; }
  #metrics-title { font-size: 20px; }
  #metrics-search-wrap { min-width: 0; width: 100%; }

  #sleep-stats-grid { grid-template-columns: 1fr 1fr; }
  .sleep-stat-value { font-size: 20px; }
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
.sec-icon-recovery  { background: #fef3c7; color: #d97706; }
.sec-icon-sleep     { background: #ede9fe; color: #7c3aed; }
.sec-icon-activity  { background: #d1fae5; color: #059669; }
.sec-icon-cardio    { background: #dbeafe; color: #0284c7; }
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
.admin-section { margin-bottom: 32px; }
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
.admin-actions { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 12px; }
.admin-action-card {
  background: var(--card-bg); border: 1px solid var(--card-border);
  border-radius: 14px; padding: 18px 20px;
}
.admin-action-title { font-size: 15px; font-weight: 700; margin-bottom: 6px; }
.admin-action-desc { font-size: 13px; color: var(--muted); margin-bottom: 14px; line-height: 1.5; }
.admin-btn {
  display: inline-flex; align-items: center; gap: 7px;
  padding: 9px 18px; border: none; border-radius: 10px;
  font-size: 14px; font-weight: 600; cursor: pointer; transition: opacity .15s;
}
.admin-btn:disabled { opacity: .5; cursor: not-allowed; }
.admin-btn.primary { background: var(--accent); color: #fff; }
.admin-btn.primary:hover:not(:disabled) { opacity: .85; }
.admin-btn.danger { background: #fee2e2; color: #b91c1c; }
.admin-btn.danger:hover:not(:disabled) { background: #fecaca; }
#admin-msg {
  margin-top: 14px; padding: 10px 14px; border-radius: 10px; font-size: 14px;
}
.admin-msg-ok { background: #dcfce7; color: #166534; }
.admin-msg-err { background: #fee2e2; color: #b91c1c; }
/* Data integrity section */
.admin-section-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
.admin-section-header .section-title { margin-bottom: 0; }
/* Data gaps */
.admin-gaps-checking { font-size: 13px; color: #78716c; padding: 8px 0; }
.admin-gaps-ok { font-size: 13px; color: #166534; padding: 8px 0; }
.admin-gap-partial { font-size: 11px; color: #b45309; background: #fef3c7; border-radius: 4px; padding: 1px 5px; margin-left: 4px; }
.admin-gaps-section { background: #fffbeb; border: 1px solid #fcd34d !important; }
.admin-gaps-title { font-weight: 600; font-size: 14px; color: #92400e; margin-bottom: 10px; display: flex; align-items: center; gap: 6px; }
.admin-gaps-icon { font-size: 16px; }
.admin-gaps-list { display: flex; flex-direction: column; gap: 8px; }
.admin-gap-row { display: flex; align-items: center; gap: 10px; padding: 8px 12px; background: #fef3c7; border-radius: 8px; flex-wrap: wrap; }
.admin-gap-range { font-size: 13px; font-weight: 600; color: #78350f; flex: 1; min-width: 0; }
.admin-gap-days { font-size: 12px; color: #92400e; white-space: nowrap; }
.admin-gap-btn { padding: 4px 12px; border-radius: 6px; border: none; background: #f59e0b; color: #fff; font-size: 12px; font-weight: 600; cursor: pointer; white-space: nowrap; }
.admin-gap-btn:hover { background: #d97706; }
.admin-gap-hint { margin-bottom: 10px; padding: 8px 12px; border-radius: 8px; background: #fef3c7; border: 1px solid #fcd34d; font-size: 13px; color: #92400e; }
.admin-gap-row-today { background: #fffbeb; border: 1px dashed #fcd34d; }
.admin-gap-today { font-size: 13px; font-weight: 500; color: #92400e; }
.admin-unconfigured { padding: 12px 16px; border-radius: 8px; font-size: 13px; color: #94a3b8; background: #1a1d2e; border: 1px dashed #2a2d3e; margin-top: 8px; }
.admin-settings-form { display: flex; flex-direction: column; gap: 10px; }
.admin-field-row { display: flex; flex-direction: column; gap: 4px; }
.admin-field-row-pair { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.admin-field-half { display: flex; flex-direction: column; gap: 4px; }
.admin-field-label { font-size: 12px; color: var(--muted); }
.admin-field-input { background: var(--surface); border: 1px solid var(--border); color: var(--text); padding: 8px 12px; border-radius: 8px; font-size: 14px; outline: none; width: 100%; }
.admin-field-input:focus { border-color: var(--accent); }
.admin-field-group-title { font-size: 12px; font-weight: 600; color: #94a3b8; text-transform: uppercase; letter-spacing: 0.05em; margin-top: 6px; }
.admin-settings-actions { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 4px; }
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
`
