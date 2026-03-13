package ui

const cssStyle = `
*, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
:root {
  --bg: #f5f4f0;
  --surface: #ffffff;
  --surface2: #f0eeea;
  --border: #e8e5e0;
  --border-light: #f0eeea;
  --text: #1a1714;
  --text-secondary: #6b6560;
  --muted: #b0aaa4;
  --accent: #2563eb;
  --good: #16a34a; --good-bg: #f0fdf4;
  --fair: #d97706; --fair-bg: #fffbeb;
  --low: #dc2626;  --low-bg: #fef2f2;
  --heart: #e11d48; --activity: #059669; --sleep: #7c3aed; --cardio: #0284c7;
  --shadow: 0 2px 8px rgba(0,0,0,0.06);
  --shadow-lg: 0 8px 32px rgba(0,0,0,0.08);
  --radius: 24px; --radius-sm: 16px; --radius-xs: 12px;
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
  background: linear-gradient(135deg, #4f46e5 0%, #7c3aed 50%, #a855f7 100%);
  border-radius: 32px;
  padding: 48px 56px;
  color: #fff;
  position: relative;
  overflow: hidden;
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 64px;
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
  max-width: 420px; margin-bottom: 28px;
}
#readiness-recovery {}
#recovery-bar-labels {
  display: flex; justify-content: space-between;
  font-size: 13px; opacity: 0.7; margin-bottom: 8px; font-weight: 500;
}
#recovery-bar-track {
  width: 100%; height: 8px; background: rgba(255,255,255,0.2);
  border-radius: 4px; overflow: hidden;
}
#recovery-bar-fill {
  height: 100%; background: #fff; border-radius: 4px;
  transition: width 0.8s cubic-bezier(0.4,0,0.2,1);
}
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

/* ── Overall status ── */
#overall-status {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 6px 16px; border-radius: 20px;
  font-size: 13px; font-weight: 600; margin-bottom: 28px;
}
#overall-status.good { background: var(--good-bg); color: var(--good); }
#overall-status.fair { background: var(--fair-bg); color: var(--fair); }
#overall-status.low  { background: var(--low-bg);  color: var(--low); }
.status-dot { width: 8px; height: 8px; border-radius: 50%; background: currentColor; }

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
.section-subtitle { font-size: 13px; color: var(--muted); }
#corr-legend { display: flex; gap: 20px; font-size: 12px; color: var(--muted); margin-top: 8px; }
.legend-item { display: flex; align-items: center; gap: 6px; font-weight: 500; }
.legend-dot { width: 10px; height: 10px; border-radius: 50%; }
#corr-chart-wrap { height: 220px; position: relative; overflow: hidden; max-width: 100%; }
#corr-chart-wrap canvas { max-width: 100%; }

/* ── Insights panel ── */
#insights-panel {
  background: var(--surface); border-radius: var(--radius);
  padding: 32px; box-shadow: var(--shadow);
  min-width: 0; overflow: hidden;
}
.panel-header { display: flex; align-items: center; gap: 10px; margin-bottom: 24px; }
.panel-header svg { color: var(--sleep); }
.panel-title { font-size: 18px; font-weight: 700; letter-spacing: -0.3px; }
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
.insight-header { display: flex; align-items: center; gap: 14px; margin-bottom: 12px; }
.insight-icon {
  width: 44px; height: 44px; border-radius: 14px;
  display: flex; align-items: center; justify-content: center; flex-shrink: 0;
}
.insight-card[data-key="recovery"] .insight-icon { background: #fef3c7; }
.insight-card[data-key="sleep"]    .insight-icon { background: #ede9fe; }
.insight-card[data-key="activity"] .insight-icon { background: #d1fae5; }
.insight-card[data-key="cardio"]   .insight-icon { background: #dbeafe; }
.insight-title { font-size: 16px; font-weight: 700; flex: 1; }
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

/* ── Explore ── */
#explore-section { margin-bottom: 40px; }
#explore-toggle {
  background: var(--surface); border: 1px solid var(--border); color: var(--text);
  padding: 16px 28px; border-radius: var(--radius); cursor: pointer; font-size: 15px;
  font-weight: 600; width: 100%; text-align: left; display: flex;
  align-items: center; justify-content: space-between; transition: all 0.15s; box-shadow: var(--shadow);
}
#explore-toggle:hover { background: var(--surface2); }
#explore-toggle .arrow { font-size: 18px; color: var(--muted); transition: transform 0.2s; }
#explore-toggle.open .arrow { transform: rotate(180deg); }
#explore-content { display: none; margin-top: 16px; }
#explore-content.open { display: block; }
.explore-category { margin-bottom: 24px; }
.explore-cat-label { font-size: 12px; font-weight: 700; color: var(--muted); margin-bottom: 12px; display: flex; align-items: center; gap: 8px; text-transform: uppercase; letter-spacing: 0.5px; }
.explore-cat-dot { width: 8px; height: 8px; border-radius: 50%; }
.explore-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(160px, 1fr)); gap: 12px; }
.explore-card {
  background: var(--surface); border: 1px solid var(--border-light);
  border-radius: var(--radius-sm); padding: 16px; cursor: pointer; transition: all 0.15s;
}
.explore-card:hover { border-color: var(--accent); box-shadow: var(--shadow); }
.explore-card-label { font-size: 11px; color: var(--muted); margin-bottom: 6px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.3px; }
.explore-card-value { font-size: 24px; font-weight: 800; color: var(--text); letter-spacing: -0.5px; }
.explore-card-unit { font-size: 11px; color: var(--muted); margin-top: 2px; }
.explore-card-trend { font-size: 12px; margin-top: 8px; font-weight: 600; }
.explore-card-trend.up      { color: var(--good); }
.explore-card-trend.down    { color: var(--low); }
.explore-card-trend.neutral { color: var(--muted); }

/* ── Chart view ── */
#chart-view { display: none; }
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

  .explore-grid { grid-template-columns: 1fr 1fr; gap: 8px; }
  .explore-card { padding: 12px; }
  .explore-card-value { font-size: 20px; }

  #sleep-stats-grid { grid-template-columns: 1fr 1fr; }
  .sleep-stat-value { font-size: 20px; }
}

/* ── Search modal ── */
#search-overlay {
  display: none; position: fixed; inset: 0;
  background: rgba(0,0,0,0.25); z-index: 200;
  backdrop-filter: blur(2px);
}
#search-modal {
  display: none; position: fixed;
  top: 12%; left: 50%; transform: translateX(-50%);
  width: 92%; max-width: 640px;
  background: var(--surface);
  border-radius: var(--radius-sm);
  box-shadow: 0 24px 80px rgba(0,0,0,0.18);
  z-index: 210; overflow: hidden;
}
#search-input-wrap {
  display: flex; align-items: center; gap: 12px;
  padding: 14px 18px;
  border-bottom: 1px solid var(--border);
}
#search-input {
  flex: 1; border: none; outline: none;
  font-size: 16px; color: var(--text);
  background: transparent;
  font-family: inherit;
}
#search-input::placeholder { color: var(--muted); }
#search-hint {
  font-size: 11px; color: var(--muted);
  background: var(--surface2);
  padding: 3px 8px; border-radius: 6px;
  white-space: nowrap;
  border: 1px solid var(--border);
}
#search-results {
  max-height: 440px; overflow-y: auto; padding: 8px;
}
.search-cat-header {
  display: flex; align-items: center; gap: 7px;
  font-size: 11px; font-weight: 700; color: var(--muted);
  text-transform: uppercase; letter-spacing: 0.5px;
  padding: 10px 10px 6px;
}
.search-cat-dot { width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0; }
.search-result-row {
  display: flex; align-items: center; justify-content: space-between;
  padding: 9px 10px; border-radius: 10px;
  cursor: pointer; gap: 12px;
  transition: background 0.1s;
}
.search-result-row:hover, .search-result-row.selected {
  background: var(--surface2);
}
.search-result-name { font-size: 14px; color: var(--text); }
.search-result-right { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }
.search-result-value {
  font-size: 14px; font-weight: 700; color: var(--text);
}
.search-result-unit { font-size: 11px; font-weight: 400; color: var(--muted); margin-left: 2px; }
.search-result-trend { font-size: 13px; font-weight: 700; }
.search-result-trend.up   { color: var(--good); }
.search-result-trend.down { color: var(--low); }
.search-empty { padding: 24px; color: var(--muted); text-align: center; font-size: 14px; }
`
