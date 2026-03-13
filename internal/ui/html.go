package ui

const htmlBody = `
<header id="top-bar">
  <div id="top-bar-left" onclick="showDashboard()" style="cursor:pointer">
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M20.84 4.61a5.5 5.5 0 0 0-7.78 0L12 5.67l-1.06-1.06a5.5 5.5 0 0 0-7.78 7.78l1.06 1.06L12 21.23l7.78-7.78 1.06-1.06a5.5 5.5 0 0 0 0-7.78z"></path></svg>
    <span id="top-bar-title" data-i18n="app_title">Health</span>
  </div>
  <div id="top-bar-right">
    <button class="top-btn" onclick="openSearch()">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
      <span data-i18n="explore">Explore</span>
    </button>
    <button id="lang-btn" class="top-btn lang-toggle" onclick="cycleLang()">EN</button>
  </div>
</header>
<div id="app">
  <div id="dashboard-view">
    <div id="briefing-loading">
      <div style="font-size:28px;margin-bottom:12px"><span data-i18n="loading">Loading your health data</span><span class="loading-dots"></span></div>
    </div>
    <div id="briefing-content" style="display:none">

      <!-- Hero: Readiness Score -->
      <div id="hero-section">
        <div id="hero-bg-glow-1"></div>
        <div id="hero-bg-glow-2"></div>
        <div id="hero-score-block">
          <div id="readiness-label-top" data-i18n="readiness">Readiness</div>
          <div id="readiness-score">--</div>
          <div id="readiness-status"></div>
        </div>
        <div id="hero-right-block">
          <div id="readiness-tip"></div>
          <div id="readiness-recovery">
            <div id="recovery-bar-labels">
              <span data-i18n="recovery">Recovery</span>
              <span id="recovery-pct-label"></span>
            </div>
            <div id="recovery-bar-track">
              <div id="recovery-bar-fill"></div>
            </div>
          </div>
        </div>
        <div id="hero-date-strip"></div>
      </div>

      <!-- Metric cards -->
      <div id="metric-cards-grid"></div>

      <!-- Overall status -->
      <div id="overall-status" style="display:none">
        <span class="status-dot"></span>
        <span id="overall-label"></span>
      </div>

      <!-- Correlation chart + Insights -->
      <div id="correlation-insights-row">
        <div id="correlation-section" style="display:none">
          <div class="section-header">
            <div class="section-title" data-i18n="activity_vs_recovery">Activity vs Recovery</div>
            <div class="section-subtitle" data-i18n="activity_recovery_subtitle">How physical load affects your HRV</div>
            <div id="corr-legend">
              <span class="legend-item"><span class="legend-dot" style="background:var(--activity)"></span><span data-i18n="activity_load">Activity load</span></span>
              <span class="legend-item"><span class="legend-dot" style="background:var(--heart)"></span>HRV</span>
            </div>
          </div>
          <div id="corr-chart-wrap">
            <canvas id="corr-chart"></canvas>
          </div>
        </div>
        <div id="insights-panel" style="display:none">
          <div class="panel-header">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="23 6 13.5 15.5 8.5 10.5 1 18"/><polyline points="17 6 23 6 23 12"/></svg>
            <span class="panel-title" data-i18n="this_week">This week</span>
          </div>
          <ul id="insights-list"></ul>
        </div>
      </div>

      <!-- Sleep section -->
      <div id="sleep-section" style="display:none">
        <div class="section-header">
          <div class="section-title" data-i18n="sleep_section">Sleep</div>
          <div class="section-subtitle" data-i18n="sleep_subtitle">Average over last 3 nights</div>
        </div>
        <div id="sleep-stats-grid"></div>
      </div>

      <!-- Section detail cards -->
      <div id="section-cards"></div>

      <!-- Trend sparklines -->
      <div id="trends-section">
        <div class="section-title" data-i18n="your_trends">Your trends</div>
        <div id="trend-charts"></div>
      </div>

      <!-- Explore all metrics -->
      <div id="explore-section">
        <button id="explore-toggle" onclick="toggleExplore()">
          <span data-i18n="all_metrics">All metrics</span>
          <span class="arrow">&#9662;</span>
        </button>
        <div id="explore-content"></div>
      </div>
    </div>
  </div>

  <!-- Chart detail view -->
  <div id="chart-view">
    <button id="chart-back" onclick="showDashboard()">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="15 18 9 12 15 6"/></svg>
      <span data-i18n="back">Back</span>
    </button>
    <div id="chart-title-row">
      <span id="chart-metric-name"></span>
      <span id="chart-period"></span>
    </div>
    <div id="chart-controls">
      <div class="presets" id="presets">
        <button class="preset-btn" onclick="applyPreset(1)">1d</button>
        <button class="preset-btn" onclick="applyPreset(7)">7d</button>
        <button class="preset-btn active" onclick="applyPreset(30)">30d</button>
        <button class="preset-btn" onclick="applyPreset(90)">90d</button>
      </div>
      <div class="ctrl-group">
        <input type="date" id="from" onchange="onDateChange()">
        <span class="ctrl-label">&mdash;</span>
        <input type="date" id="to" onchange="onDateChange()">
      </div>
      <div class="ctrl-group">
        <span class="ctrl-label" data-i18n="bucket">Bucket</span>
        <select id="bucket" onchange="loadChart()">
          <option value="" data-i18n="auto">Auto</option>
          <option value="minute" data-i18n="minute">Minute</option>
          <option value="hour" data-i18n="hour">Hour</option>
          <option value="day" data-i18n="day">Day</option>
        </select>
        <span class="ctrl-label" style="margin-left:4px" data-i18n="agg">Agg</span>
        <select id="agg" onchange="loadChart()">
          <option value="" data-i18n="auto">Auto</option>
          <option value="AVG" data-i18n="avg">Avg</option>
          <option value="SUM" data-i18n="sum">Sum</option>
          <option value="MAX" data-i18n="max">Max</option>
          <option value="MIN" data-i18n="min">Min</option>
        </select>
      </div>
      <div class="ctrl-group">
        <div class="shift-btns">
          <button onclick="shiftRange(-1)">&#8249;</button>
          <button onclick="shiftRange(1)">&#8250;</button>
        </div>
        <button id="compare-btn" class="toolbar-btn" onclick="toggleCompare()" data-i18n="compare">Compare</button>
        <button class="toolbar-btn" onclick="downloadCSV()">CSV</button>
      </div>
    </div>
    <div id="stats-row"></div>
    <div id="chart-wrap">
      <div id="chart-loading"><div class="spinner"></div></div>
      <canvas id="chart"></canvas>
    </div>
  </div>
</div>
<div id="search-overlay" onclick="closeSearch()"></div>
<div id="search-modal">
  <div id="search-input-wrap">
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" style="color:var(--muted);flex-shrink:0"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
    <input id="search-input" type="text" placeholder="Search metrics..." oninput="filterSearch(this.value)" autocomplete="off">
    <span id="search-hint" data-i18n="esc_hint">ESC to close</span>
  </div>
  <div id="search-results"></div>
</div>
`
