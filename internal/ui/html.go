package ui

const htmlBody = `
<header id="top-bar">
  <div id="top-bar-left" onclick="showDashboard()" style="cursor:pointer">
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M20.84 4.61a5.5 5.5 0 0 0-7.78 0L12 5.67l-1.06-1.06a5.5 5.5 0 0 0-7.78 7.78l1.06 1.06L12 21.23l7.78-7.78 1.06-1.06a5.5 5.5 0 0 0 0-7.78z"></path></svg>
    <span id="top-bar-title" data-i18n="app_title">Health</span>
  </div>
  <div id="top-bar-right">
    <button class="top-btn" onclick="showMetricsView()">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
      <span data-i18n="all_metrics">All metrics</span>
    </button>
    <button class="top-btn" onclick="showAdminView()" title="Settings">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
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
        <div id="hero-sparkline-block" onclick="selectMetric('readiness')" style="display:none">
          <div id="hero-sparkline-label"><span data-i18n="trend_readiness">Readiness</span> · 30d</div>
          <div id="hero-sparkline-wrap"><canvas id="readiness-sparkline"></canvas></div>
        </div>
        <div id="hero-date-strip"></div>
      </div>

      <!-- Metric cards -->
      <div id="metric-cards-area">
        <div class="section-title" data-i18n="at_a_glance">At a glance</div>
        <div id="metric-cards-grid"></div>
      </div>

      <!-- Section detail cards -->
      <div id="sections-area">
        <div class="section-title" data-i18n="health_sections">Health overview</div>
        <div id="section-cards"></div>
      </div>

      <!-- Weekly section: Correlation + Insights + Sleep -->
      <div id="weekly-section">
        <div class="section-title" data-i18n="this_week">This week</div>
        <div id="correlation-insights-row">
          <div id="correlation-section" style="display:none">
            <div class="section-header">
              <div class="section-subtitle" data-i18n="activity_vs_recovery">Activity vs Recovery</div>
              <div class="section-sub2" data-i18n="activity_recovery_subtitle">How physical load affects your HRV</div>
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
            <ul id="insights-list"></ul>
          </div>
        </div>

        <div id="sleep-section" style="display:none">
          <div class="section-header">
            <div class="section-subtitle" data-i18n="sleep_section">Sleep</div>
            <div class="section-sub2" data-i18n="sleep_subtitle">Average over last 3 nights</div>
          </div>
          <div id="sleep-stats-grid"></div>
          <div id="sleep-sources" style="display:none"></div>
        </div>
      </div>

      <!-- Trend sparklines -->
      <div id="trends-section">
        <div class="section-title" data-i18n="your_trends">Your trends</div>
        <div id="trend-charts"></div>
      </div>

    </div>
  </div>

  <!-- Section detail view -->
  <div id="section-view" style="display:none">
    <button id="section-back" onclick="hideSectionView()">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="15 18 9 12 15 6"/></svg>
      <span data-i18n="back">Back</span>
    </button>
    <div id="section-content"></div>
  </div>

  <!-- Metrics view -->
  <div id="metrics-view" style="display:none">
    <div id="metrics-header">
      <button id="metrics-back" onclick="hideMetricsView()">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="15 18 9 12 15 6"/></svg>
        <span data-i18n="back">Back</span>
      </button>
      <span id="metrics-title" data-i18n="all_metrics">All metrics</span>
      <div id="metrics-search-wrap">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
        <input id="metrics-search" type="text" oninput="renderMetricsView()" autocomplete="off">
      </div>
    </div>
    <div id="metrics-content"></div>
  </div>

  <!-- Admin / Settings view -->
  <div id="admin-view" style="display:none">
    <div id="admin-header">
      <button class="back-btn" onclick="hideAdminView()">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="15 18 9 12 15 6"/></svg>
        <span data-i18n="back">Back</span>
      </button>
      <span class="view-title" data-i18n="admin_title">Settings</span>
    </div>
    <div id="admin-content">
      <div id="admin-loading"><div class="spinner"></div></div>
      <div id="admin-body" style="display:none">

        <div class="admin-section">
          <div class="admin-section-header">
            <div class="section-title" data-i18n="admin_cache_status">Cache status</div>
            <button class="admin-btn" id="btn-refresh-status" onclick="loadAdminStatus()" data-i18n="admin_refresh">Refresh</button>
          </div>
          <div id="admin-cache-table"></div>
        </div>

        <div class="admin-section">
          <div class="section-title" data-i18n="admin_actions">Actions</div>
          <div class="admin-actions">
            <div class="admin-action-card">
              <div class="admin-action-title" data-i18n="admin_incremental_title">Update cache</div>
              <div class="admin-action-desc" data-i18n="admin_incremental_desc">Fill missing entries since last run. Fast, safe to run anytime.</div>
              <button class="admin-btn primary" onclick="triggerBackfill(false)" id="btn-backfill">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/></svg>
                <span data-i18n="admin_run">Run</span>
              </button>
            </div>
            <div class="admin-action-card">
              <div class="admin-action-title" data-i18n="admin_force_title">Rebuild all</div>
              <div class="admin-action-desc" data-i18n="admin_force_desc">Clear and recompute all caches from raw data. Use after formula changes.</div>
              <button class="admin-btn danger" onclick="triggerBackfill(true)" id="btn-backfill-force">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="1 4 1 10 7 10"/><path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10"/></svg>
                <span data-i18n="admin_force_run">Rebuild</span>
              </button>
            </div>
          </div>
          <div id="admin-msg" style="display:none"></div>
        </div>

        <div class="admin-section">
          <div class="admin-section-header">
            <div class="section-title" data-i18n="admin_gaps_section_title">Data Integrity</div>
            <button class="admin-btn" id="btn-check-gaps" onclick="checkDataGaps()" data-i18n="admin_gaps_check">Check for gaps</button>
          </div>
          <div id="admin-gaps-section"></div>
        </div>

        <div class="admin-section" id="admin-import-section">
          <div class="section-title" data-i18n="admin_import_title">Apple Health Import</div>
          <div class="admin-import-body">
            <div class="admin-import-desc" data-i18n="admin_import_desc">Upload your Apple Health export.zip (or export.xml) to import historical data. Duplicates are skipped automatically.</div>
            <div id="import-gap-hint" style="display:none" class="admin-gap-hint"></div>
            <div class="admin-import-form">
              <label class="admin-import-file-label" id="import-file-label" for="import-file-input" data-i18n="admin_import_choose">Choose file…</label>
              <input type="file" id="import-file-input" accept=".zip,.xml" style="display:none" onchange="importFileChosen(this)">
              <div class="admin-import-options">
                <label class="admin-import-opt-label">
                  <input type="number" id="import-batch" value="500" min="100" max="5000" style="width:70px"> <span data-i18n="admin_import_batch">points/batch</span>
                </label>
                <label class="admin-import-opt-label">
                  <input type="number" id="import-pause" value="150" min="0" max="5000" style="width:70px"> <span data-i18n="admin_import_pause">ms pause</span>
                </label>
              </div>
              <button class="admin-btn primary" onclick="startImport()" id="btn-import-start" disabled>
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>
                <span data-i18n="admin_import_start">Start import</span>
              </button>
            </div>
            <div id="import-progress" style="display:none">
              <div class="import-progress-bar-track"><div class="import-progress-bar-fill" id="import-bar"></div></div>
              <div id="import-status-text" class="import-status-text"></div>
            </div>
          </div>
        </div>

        <div class="admin-section" id="admin-notify-section">
          <div class="section-title" data-i18n="admin_notify_title">Telegram reports</div>
          <div class="admin-settings-form">
            <div class="admin-field-row">
              <label class="admin-field-label" data-i18n="admin_notify_token">Bot token</label>
              <input class="admin-field-input" type="password" id="cfg-telegram-token" autocomplete="off" placeholder="123456:ABC-DEF...">
            </div>
            <div class="admin-field-row">
              <label class="admin-field-label" data-i18n="admin_notify_chat_id">Chat ID</label>
              <input class="admin-field-input" type="text" id="cfg-telegram-chat-id" autocomplete="off" placeholder="123456789">
            </div>
            <div class="admin-field-row">
              <label class="admin-field-label" data-i18n="admin_notify_lang">Language</label>
              <select class="admin-field-input" id="cfg-report-lang">
                <option value="en">English</option>
                <option value="ru">Русский</option>
                <option value="sr">Srpski</option>
              </select>
            </div>
            <div class="admin-field-row">
              <label class="admin-field-label" data-i18n="admin_notify_timezone">Timezone</label>
              <input class="admin-field-input" type="text" id="cfg-timezone" autocomplete="off" placeholder="Europe/Belgrade">
            </div>
            <div class="admin-field-group-title" data-i18n="admin_notify_schedule_morning">Morning report</div>
            <div class="admin-field-row-pair">
              <div class="admin-field-half">
                <label class="admin-field-label" data-i18n="admin_notify_weekday">Weekdays</label>
                <input class="admin-field-input" type="number" id="cfg-morning-weekday" min="0" max="23" placeholder="8">
              </div>
              <div class="admin-field-half">
                <label class="admin-field-label" data-i18n="admin_notify_weekend">Weekends</label>
                <input class="admin-field-input" type="number" id="cfg-morning-weekend" min="0" max="23" placeholder="9">
              </div>
            </div>
            <div class="admin-field-group-title" data-i18n="admin_notify_schedule_evening">Evening report</div>
            <div class="admin-field-row-pair">
              <div class="admin-field-half">
                <label class="admin-field-label" data-i18n="admin_notify_weekday">Weekdays</label>
                <input class="admin-field-input" type="number" id="cfg-evening-weekday" min="0" max="23" placeholder="20">
              </div>
              <div class="admin-field-half">
                <label class="admin-field-label" data-i18n="admin_notify_weekend">Weekends</label>
                <input class="admin-field-input" type="number" id="cfg-evening-weekend" min="0" max="23" placeholder="21">
              </div>
            </div>
            <div class="admin-settings-actions">
              <button class="admin-btn primary" onclick="saveNotifySettings()" id="btn-settings-save">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/><polyline points="17 21 17 13 7 13 7 21"/><polyline points="7 3 7 8 15 8"/></svg>
                <span data-i18n="admin_notify_save">Save</span>
              </button>
              <button class="admin-btn" onclick="triggerTestNotify('morning')" id="btn-notify-morning">
                🌅 <span data-i18n="admin_notify_test_morning">Test morning</span>
              </button>
              <button class="admin-btn" onclick="triggerTestNotify('evening')" id="btn-notify-evening">
                🌆 <span data-i18n="admin_notify_test_evening">Test evening</span>
              </button>
            </div>
          </div>
          <div id="admin-notify-msg" style="display:none"></div>
        </div>

      </div>
    </div>
  </div>

  <!-- Chart detail view -->
  <div id="chart-view">
    <button id="chart-back" onclick="goBack()">
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
        <button class="preset-btn" onclick="applyPreset(365)">1y</button>
        <button class="preset-btn" onclick="applyPresetAll()" data-i18n="preset_all">All</button>
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
        <button id="by-source-btn" class="toolbar-btn" onclick="toggleBySource()" data-i18n="by_source">Sources</button>
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
`
