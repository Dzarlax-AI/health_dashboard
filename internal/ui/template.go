package ui

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Health Dashboard</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4/dist/chart.umd.min.js"></script>
<style>
*, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
:root {
  --bg: #0d0f18; --surface: #13151f; --surface2: #1a1d2e; --border: #252836;
  --text: #e2e8f0; --muted: #64748b; --accent: #4f8ef7;
  --heart: #f87171; --activity: #34d399; --mobility: #fbbf24;
  --sleep: #a78bfa; --env: #22d3ee;
  --up: #34d399; --down: #f87171;
}
*, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; background: var(--bg); color: var(--text); display: flex; height: 100vh; overflow: hidden; font-size: 13px; }

/* ── Sidebar ── */
#sidebar { width: 240px; min-width: 240px; background: var(--surface); border-right: 1px solid var(--border); display: flex; flex-direction: column; overflow: hidden; z-index: 50; transition: transform 0.2s; }
#sidebar-header { padding: 16px; display: flex; align-items: center; gap: 8px; border-bottom: 1px solid var(--border); cursor: pointer; flex-shrink: 0; }
#sidebar-header svg { color: var(--accent); flex-shrink: 0; }
#sidebar-header span { font-size: 15px; font-weight: 700; letter-spacing: -0.3px; }
#metric-nav { overflow-y: auto; flex: 1; padding: 6px 0; }
.category { }
.category-label { padding: 8px 16px 4px; font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: 1px; color: var(--muted); display: flex; align-items: center; gap: 6px; cursor: pointer; user-select: none; }
.category-label:hover { color: var(--text); }
.category-label .cat-arrow { margin-left: auto; font-size: 9px; transition: transform 0.15s; }
.category.collapsed .cat-arrow { transform: rotate(-90deg); }
.category.collapsed .metric-item { display: none; }
.category-dot { width: 6px; height: 6px; border-radius: 50%; flex-shrink: 0; }
.metric-item { padding: 6px 16px 6px 28px; cursor: pointer; border-left: 2px solid transparent; display: flex; justify-content: space-between; align-items: center; transition: background 0.1s; color: var(--muted); }
.metric-item:hover { background: var(--surface2); color: var(--text); }
.metric-item.active { background: var(--surface2); border-left-color: var(--accent); color: var(--text); }
.metric-item .item-name { font-size: 12px; }
.metric-item .item-count { font-size: 10px; color: var(--muted); }

/* ── Main ── */
#main { flex: 1; display: flex; flex-direction: column; overflow: hidden; min-width: 0; }

/* ── Toolbar ── */
#toolbar { padding: 12px 20px; background: var(--surface); border-bottom: 1px solid var(--border); display: flex; align-items: center; gap: 8px; flex-wrap: wrap; min-height: 52px; flex-shrink: 0; }
#hamburger { display: none; background: none; border: none; color: var(--muted); cursor: pointer; padding: 4px; }
#hamburger:hover { color: var(--text); }
#toolbar-title { font-size: 15px; font-weight: 600; flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
.presets { display: flex; gap: 3px; }
.preset-btn { background: var(--surface2); border: 1px solid var(--border); color: var(--muted); padding: 4px 9px; border-radius: 5px; cursor: pointer; font-size: 12px; transition: all 0.1s; }
.preset-btn:hover, .preset-btn.active { background: var(--accent); border-color: var(--accent); color: #fff; }
.ctrl-group { display: flex; align-items: center; gap: 5px; }
.ctrl-label { font-size: 11px; color: var(--muted); }
select, input[type=date] { background: var(--surface2); border: 1px solid var(--border); color: var(--text); padding: 4px 7px; border-radius: 5px; font-size: 12px; }

/* ── Dashboard ── */
#dashboard-view { flex: 1; overflow-y: auto; padding: 20px; }
#dashboard-header { display: flex; align-items: baseline; gap: 12px; margin-bottom: 16px; }
#dashboard-header h2 { font-size: 13px; text-transform: uppercase; letter-spacing: 1px; color: var(--muted); }
#dashboard-date { font-size: 12px; color: var(--muted); }
#last-updated { font-size: 11px; color: var(--muted); margin-left: auto; opacity: 0.7; }
#cards-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: 10px; }
.card { background: var(--surface); border: 1px solid var(--border); border-radius: 10px; padding: 14px; cursor: pointer; transition: border-color 0.15s, transform 0.1s; }
.card:hover { border-color: var(--accent); transform: translateY(-1px); }
.card-label { font-size: 10px; color: var(--muted); margin-bottom: 6px; text-transform: uppercase; letter-spacing: 0.5px; }
.card-value { font-size: 26px; font-weight: 700; line-height: 1; }
.card-unit { font-size: 11px; color: var(--muted); margin-top: 3px; }
.card-trend { font-size: 11px; margin-top: 8px; font-weight: 500; }
.card-spark { margin-top: 10px; height: 36px; width: 100%; opacity: 0.7; }
.card-trend.up { color: var(--up); }
.card-trend.down { color: var(--down); }
.card-trend.neutral { color: var(--muted); }
.card[data-cat="heart"] .card-value { color: var(--heart); }
.card[data-cat="activity"] .card-value { color: var(--activity); }
.card[data-cat="sleep"] .card-value { color: var(--sleep); }
.card[data-cat="env"] .card-value { color: var(--env); }

/* ── Featured charts ── */
#featured-charts { margin-top: 20px; display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }
.feat-chart { background: var(--surface); border: 1px solid var(--border); border-radius: 10px; padding: 14px; cursor: pointer; transition: border-color 0.15s; }
.feat-chart:hover { border-color: var(--accent); }
.feat-chart-title { font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px; color: var(--muted); margin-bottom: 10px; }
.feat-chart-canvas-wrap { position: relative; height: 140px; }
.feat-chart canvas { display: block; }

/* ── Chart view ── */
#chart-view { flex: 1; display: flex; flex-direction: column; overflow: hidden; padding: 16px 20px; gap: 12px; }
#stats-row { display: flex; gap: 8px; flex-wrap: wrap; flex-shrink: 0; }
.stat-chip { background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 9px 14px; }
.stat-chip .s-label { font-size: 10px; color: var(--muted); text-transform: uppercase; letter-spacing: 0.5px; }
.stat-chip .s-value { font-size: 18px; font-weight: 600; color: var(--accent); margin-top: 2px; }
#chart-wrap { flex: 1; position: relative; min-height: 0; background: var(--surface); border: 1px solid var(--border); border-radius: 10px; padding: 14px; }
#chart-loading { position: absolute; inset: 0; display: none; align-items: center; justify-content: center; background: var(--surface); border-radius: 10px; z-index: 10; }
.spinner { width: 24px; height: 24px; border: 2px solid var(--border); border-top-color: var(--accent); border-radius: 50%; animation: spin 0.7s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

/* ── Mobile overlay ── */
#sidebar-overlay { display: none; position: fixed; inset: 0; background: rgba(0,0,0,0.5); z-index: 40; }

/* ── Mobile ── */
@media (max-width: 700px) {
  #sidebar { position: fixed; top: 0; bottom: 0; left: 0; transform: translateX(-100%); }
  #sidebar.open { transform: translateX(0); box-shadow: 4px 0 20px rgba(0,0,0,0.4); }
  #sidebar-overlay.open { display: block; }
  #hamburger { display: flex; }
  #chart-view { padding: 10px 12px; gap: 8px; }
  #dashboard-view { padding: 12px; }
  #cards-grid { grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); }
}
</style>
</head>
<body>

<div id="sidebar-overlay" onclick="closeSidebar()"></div>

<aside id="sidebar">
  <div id="sidebar-header" onclick="showDashboard()">
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M20.84 4.61a5.5 5.5 0 0 0-7.78 0L12 5.67l-1.06-1.06a5.5 5.5 0 0 0-7.78 7.78l1.06 1.06L12 21.23l7.78-7.78 1.06-1.06a5.5 5.5 0 0 0 0-7.78z"></path></svg>
    <span>Health</span>
  </div>
  <nav id="metric-nav"><div style="padding:16px;color:var(--muted)">Loading…</div></nav>
</aside>

<div id="main">
  <div id="toolbar">
    <button id="hamburger" onclick="openSidebar()">
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="3" y1="6" x2="21" y2="6"/><line x1="3" y1="12" x2="21" y2="12"/><line x1="3" y1="18" x2="21" y2="18"/></svg>
    </button>
    <span id="toolbar-title">Dashboard</span>
    <div class="presets" id="presets" style="display:none">
      <button class="preset-btn" onclick="applyPreset(1)">1d</button>
      <button class="preset-btn active" onclick="applyPreset(7)">7d</button>
      <button class="preset-btn" onclick="applyPreset(30)">30d</button>
      <button class="preset-btn" onclick="applyPreset(90)">90d</button>
    </div>
    <div class="ctrl-group" id="date-ctrls" style="display:none">
      <input type="date" id="from" onchange="onDateChange()">
      <span class="ctrl-label">—</span>
      <input type="date" id="to" onchange="onDateChange()">
    </div>
    <div class="ctrl-group" id="agg-ctrls" style="display:none">
      <span class="ctrl-label">Bucket</span>
      <select id="bucket" onchange="loadChart()">
        <option value="">Auto</option>
        <option value="minute">Minute</option>
        <option value="hour">Hour</option>
        <option value="day">Day</option>
      </select>
      <span class="ctrl-label" style="margin-left:6px">Agg</span>
      <select id="agg" onchange="loadChart()">
        <option value="">Auto</option>
        <option value="AVG">Avg</option>
        <option value="SUM">Sum</option>
        <option value="MAX">Max</option>
        <option value="MIN">Min</option>
      </select>
    </div>
  </div>

  <div id="dashboard-view">
    <div id="dashboard-header">
      <h2>Today</h2>
      <span id="dashboard-date"></span>
      <span id="last-updated"></span>
    </div>
    <div id="cards-grid"><div style="color:var(--muted)">Loading…</div></div>
    <div id="featured-charts"></div>
  </div>

  <div id="chart-view" style="display:none">
    <div id="stats-row"></div>
    <div id="chart-wrap">
      <div id="chart-loading"><div class="spinner"></div></div>
      <canvas id="chart"></canvas>
    </div>
  </div>
</div>

<script>
// ── Meta ───────────────────────────────────────────────────────────────────
const NAMES = {
  sleep_total: 'Total Sleep', sleep_deep: 'Deep Sleep', sleep_rem: 'REM Sleep',
  sleep_core: 'Core Sleep', sleep_awake: 'Awake (during sleep)',
  heart_rate: 'Heart Rate', resting_heart_rate: 'Resting HR',
  walking_heart_rate_average: 'Walking HR', heart_rate_variability: 'HRV',
  blood_oxygen_saturation: 'Blood Oxygen (SpO₂)', respiratory_rate: 'Respiratory Rate',
  step_count: 'Steps', walking_running_distance: 'Distance',
  active_energy: 'Active Calories', basal_energy_burned: 'Resting Calories',
  apple_exercise_time: 'Exercise Time', apple_stand_time: 'Stand Time',
  apple_stand_hour: 'Stand Hours', physical_effort: 'Physical Effort',
  flights_climbed: 'Flights Climbed', stair_speed_up: 'Stair Speed',
  walking_speed: 'Walking Speed', walking_step_length: 'Step Length',
  walking_double_support_percentage: 'Double Support %', walking_asymmetry_percentage: 'Walking Asymmetry',
  apple_sleeping_wrist_temperature: 'Wrist Temp', breathing_disturbances: 'Breathing Disturbances',
  environmental_audio_exposure: 'Environmental Noise', headphone_audio_exposure: 'Headphone Volume',
  time_in_daylight: 'Time in Daylight', vo2_max: 'VO₂ Max',
  six_minute_walking_test_distance: '6-min Walk Test',
};
const name = k => NAMES[k] || k.replace(/_/g,' ');

const CATEGORIES = [
  { label: 'Heart',       color: 'var(--heart)',    cat: 'heart',    metrics: ['heart_rate','resting_heart_rate','walking_heart_rate_average','heart_rate_variability','blood_oxygen_saturation','respiratory_rate'] },
  { label: 'Activity',    color: 'var(--activity)', cat: 'activity', metrics: ['step_count','walking_running_distance','active_energy','basal_energy_burned','apple_exercise_time','apple_stand_time','apple_stand_hour','physical_effort','flights_climbed','stair_speed_up'] },
  { label: 'Fitness',     color: 'var(--mobility)', cat: 'mobility', metrics: ['vo2_max','six_minute_walking_test_distance','walking_speed','walking_step_length','walking_double_support_percentage','walking_asymmetry_percentage'] },
  { label: 'Sleep',       color: 'var(--sleep)',    cat: 'sleep',    metrics: ['sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake','apple_sleeping_wrist_temperature','breathing_disturbances'] },
  { label: 'Environment', color: 'var(--env)',      cat: 'env',      metrics: ['environmental_audio_exposure','headphone_audio_exposure','time_in_daylight'] },
];
const catOf = m => CATEGORIES.find(c => c.metrics.includes(m)) || null;

const BAR_METRICS = new Set(['step_count','active_energy','basal_energy_burned','apple_exercise_time','apple_stand_time','flights_climbed','walking_running_distance','time_in_daylight','apple_stand_hour','breathing_disturbances']);
const SLEEP_PHASES = [
  { metric: 'sleep_deep',  label: 'Deep',  color: '#6366f1' },
  { metric: 'sleep_rem',   label: 'REM',   color: '#a78bfa' },
  { metric: 'sleep_core',  label: 'Core',  color: '#93c5fd' },
  { metric: 'sleep_awake', label: 'Awake', color: '#fbbf24' },
];
const SLEEP_METRICS = new Set(['sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake']);

// ── State ──────────────────────────────────────────────────────────────────
let chart = null;
let currentMetric = null;
let unitsMap = {};
const featuredCharts = [];
const FEATURED = [
  { metric: 'step_count',    label: 'Steps (7d)',      color: '#34d399', type: 'bar'  },
  { metric: 'heart_rate',    label: 'Heart Rate (7d)', color: '#f87171', type: 'line' },
  { metric: 'sleep_total',   label: 'Sleep (7d)',      color: '#a78bfa', type: 'bar'  },
  { metric: 'active_energy', label: 'Active Cal (7d)', color: '#fbbf24', type: 'bar'  },
];
let pendingMetric = null;

const todayStr  = () => new Date().toISOString().slice(0,10);
const daysAgoStr = n => { const d = new Date(); d.setDate(d.getDate()-n); return d.toISOString().slice(0,10); };

// ── URL hash state ─────────────────────────────────────────────────────────
function pushHash() {
  if (!currentMetric) { history.replaceState(null,'', location.pathname); return; }
  const p = new URLSearchParams({ metric: currentMetric, from: $('from').value, to: $('to').value });
  const b = $('bucket').value, a = $('agg').value;
  if (b) p.set('bucket', b);
  if (a) p.set('agg', a);
  history.replaceState(null,'', '#' + p.toString());
}
function readHash() {
  if (!location.hash) return;
  const p = new URLSearchParams(location.hash.slice(1));
  const m = p.get('metric'); if (!m) return;
  const from = p.get('from'), to = p.get('to'), bucket = p.get('bucket'), agg = p.get('agg');
  if (from) $('from').value = from;
  if (to)   $('to').value   = to;
  if (bucket) $('bucket').value = bucket;
  if (agg)    $('agg').value    = agg;
  pendingMetric = m;
}

// ── Init ───────────────────────────────────────────────────────────────────
$('from').value = daysAgoStr(6);
$('to').value   = todayStr();
readHash();
loadDashboard();
loadFeaturedCharts();
loadMetrics();

// ── Helpers ────────────────────────────────────────────────────────────────
function $(id) { return document.getElementById(id); }

function fmtVal(metric, v) {
  if (['walking_running_distance'].includes(metric)) return v.toFixed(2);
  if (SLEEP_METRICS.has(metric)) return v.toFixed(1);
  if (v >= 1000) return Math.round(v).toLocaleString();
  if (v % 1 === 0) return v;
  return v.toFixed(1);
}
function fmtUnit(u) {
  const map = {'count/min':'bpm','count':'','kcal':'kcal','km':'km','%':'%','ms':'ms','min':'min','hr':'h','degC':'°C','dBASPL':'dB','ml/(kg·min)':'ml/kg/min','m':'m','m/s':'m/s'};
  return map[u] !== undefined ? map[u] : (u || '');
}
function fmtCount(n) { return n >= 1000 ? (n/1000).toFixed(1)+'k' : n; }
function fmt2(v) {
  if (v == null || isNaN(v)) return '—';
  if (v >= 1000) return Math.round(v).toLocaleString();
  return v % 1 === 0 ? String(v) : v.toFixed(1);
}

// Format axis date label
function fmtAxisDate(label) {
  if (!label) return '';
  const dateStr = label.slice(0,10);
  const timeStr = label.length > 10 ? label.slice(11,16) : '';
  const d = new Date(dateStr + 'T12:00:00'); // noon to avoid TZ issues
  const weekday = d.toLocaleDateString('en', { weekday: 'short' });
  const md = d.toLocaleDateString('en', { month:'short', day:'numeric' });
  if (!timeStr) return weekday + ' ' + md;          // day bucket: "Mon Mar 4"
  if (timeStr === '00:00') return weekday + ' ' + md; // midnight: mark new day
  return md + ' ' + timeStr;                         // intraday: "Mar 4 14:00"
}

function chip(label, value, unit) {
  return '<div class="stat-chip"><div class="s-label">' + label + '</div><div class="s-value">' + value +
    (unit ? ' <span style="font-size:12px;color:var(--muted)">' + unit + '</span>' : '') + '</div></div>';
}

// ── Mobile sidebar ─────────────────────────────────────────────────────────
function openSidebar() {
  $('sidebar').classList.add('open');
  $('sidebar-overlay').classList.add('open');
}
function closeSidebar() {
  $('sidebar').classList.remove('open');
  $('sidebar-overlay').classList.remove('open');
}

// ── Dashboard ──────────────────────────────────────────────────────────────
async function loadDashboard() {
  const res  = await fetch('/api/dashboard');
  const data = await res.json();
  const grid = $('cards-grid');

  if (data.date) $('dashboard-date').textContent = fmtAxisDate(data.date);
  if (data.last_updated) {
    $('last-updated').textContent = 'Updated ' + data.last_updated.slice(0,16).replace('T',' ');
  }

  const cards = data.cards || [];
  if (!cards.length) { grid.innerHTML = '<div style="color:var(--muted)">No data yet</div>'; return; }
  grid.innerHTML = '';

  cards.forEach(c => {
    const cat = catOf(c.metric);
    const div = document.createElement('div');
    div.className = 'card';
    div.dataset.cat = cat ? cat.cat : '';
    div.dataset.metric = c.metric;

    // Trend
    let trendHtml = '';
    if (c.prev > 0) {
      const pct = ((c.value - c.prev) / c.prev * 100);
      const cls = pct > 1 ? 'up' : pct < -1 ? 'down' : 'neutral';
      const arrow = pct > 1 ? '↑' : pct < -1 ? '↓' : '→';
      trendHtml = '<div class="card-trend ' + cls + '">' + arrow + ' ' + Math.abs(pct).toFixed(1) + '% vs yesterday</div>';
    }

    const aggLabel = BAR_METRICS.has(c.metric) ? 'Daily total' : 'Daily avg';
    div.innerHTML =
      '<div class="card-label">' + name(c.metric) + '</div>' +
      '<div class="card-value">' + fmtVal(c.metric, c.value) + '</div>' +
      '<div class="card-unit">'  + fmtUnit(c.unit) + ' · <span style="opacity:0.6">' + aggLabel + '</span></div>' +
      trendHtml +
      '<svg class="card-spark" data-metric="' + c.metric + '" viewBox="0 0 100 36" preserveAspectRatio="none"></svg>';
    div.onclick = () => { selectMetric(c.metric); closeSidebar(); };
    grid.appendChild(div);
  });

  // Load sparklines in parallel
  const from7 = daysAgoStr(6);
  const to7   = todayStr();
  cards.forEach(c => {
    fetch('/api/metrics/data?metric=' + encodeURIComponent(c.metric) + '&from=' + from7 + '&to=' + to7 + '&bucket=day')
      .then(r => r.json())
      .then(d => {
        const pts = (d.points || []).filter(p => p.qty > 0);
        if (pts.length < 2) return;
        const svg = document.querySelector('svg[data-metric="' + c.metric + '"]');
        if (!svg) return;
        const vals = pts.map(p => p.qty);
        const mn = Math.min(...vals), mx = Math.max(...vals);
        const range = mx - mn || 1;
        const W = 100, H = 36, pad = 3;
        const points = vals.map((v, i) => {
          const x = pad + (i / (vals.length - 1)) * (W - pad * 2);
          const y = H - pad - ((v - mn) / range) * (H - pad * 2);
          return x.toFixed(1) + ',' + y.toFixed(1);
        }).join(' ');
        const cat = catOf(c.metric);
        const color = cat ? 'var(--' + cat.cat.replace('mobility','mobility') + ')' : 'var(--accent)';
        const colorMap = { heart: 'var(--heart)', activity: 'var(--activity)', mobility: 'var(--mobility)', sleep: 'var(--sleep)', env: 'var(--env)' };
        const strokeColor = cat ? (colorMap[cat.cat] || 'var(--accent)') : 'var(--accent)';
        svg.innerHTML =
          '<polyline fill="none" stroke="' + strokeColor + '" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" points="' + points + '"/>';
      }).catch(() => {});
  });
}

// ── Featured charts on dashboard ───────────────────────────────────────────
async function loadFeaturedCharts() {
  const container = $('featured-charts');
  if (!container) return;
  container.innerHTML = '';
  featuredCharts.forEach(c => c.destroy());
  featuredCharts.length = 0;

  const from7 = daysAgoStr(6);
  const to7   = todayStr();

  await Promise.all(FEATURED.map(async f => {
    const res = await fetch('/api/metrics/data?metric=' + encodeURIComponent(f.metric) + '&from=' + from7 + '&to=' + to7 + '&bucket=day');
    const d   = await res.json();
    const pts = (d.points || []).filter(p => p.qty > 0);
    if (!pts.length) return;

    const wrap = document.createElement('div');
    wrap.className = 'feat-chart';
    wrap.onclick = () => { selectMetric(f.metric); closeSidebar(); };
    wrap.innerHTML = '<div class="feat-chart-title">' + f.label + '</div><div class="feat-chart-canvas-wrap"><canvas></canvas></div>';
    container.appendChild(wrap);

    const canvas = wrap.querySelector('canvas');
    const labels = pts.map(p => fmtAxisDate(p.date));
    const values = pts.map(p => p.qty);

    const c = new Chart(canvas, {
      type: f.type,
      data: {
        labels,
        datasets: [{
          data: values,
          borderColor: f.color,
          backgroundColor: f.type === 'bar' ? f.color + '99' : f.color + '22',
          fill: f.type === 'line',
          borderWidth: f.type === 'line' ? 2 : 1,
          pointRadius: 0,
          tension: 0.3,
        }]
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: { legend: { display: false }, tooltip: { callbacks: {
          label: ctx => ' ' + fmtVal(f.metric, ctx.parsed.y)
        }}},
        scales: {
          x: { grid: { color: '#252836' }, ticks: { color: '#64748b', font: { size: 10 }, maxTicksLimit: 7 } },
          y: { grid: { color: '#252836' }, ticks: { color: '#64748b', font: { size: 10 }, maxTicksLimit: 4 }, beginAtZero: f.type === 'bar' },
        }
      }
    });
    featuredCharts.push(c);
  }));
}

// ── Sidebar ────────────────────────────────────────────────────────────────
async function loadMetrics() {
  const res  = await fetch('/api/metrics');
  const data = await res.json();
  if (!data) return;
  data.forEach(m => { unitsMap[m.Name] = m.Units; });

  const byName = {};
  data.forEach(m => { byName[m.Name] = m; });

  const nav = $('metric-nav');
  nav.innerHTML = '';

  CATEGORIES.forEach(cat => {
    const items = cat.metrics.filter(m => byName[m]);
    if (!items.length) return;

    const section = document.createElement('div');
    section.className = 'category';

    const labelEl = document.createElement('div');
    labelEl.className = 'category-label';
    labelEl.innerHTML =
      '<span class="category-dot" style="background:' + cat.color + '"></span>' +
      cat.label +
      '<span class="cat-arrow">▾</span>';
    labelEl.onclick = () => section.classList.toggle('collapsed');
    section.appendChild(labelEl);

    items.forEach(m => {
      const mi = byName[m];
      const el = document.createElement('div');
      el.className = 'metric-item';
      el.id = 'mi-' + m;
      el.innerHTML = '<span class="item-name">' + name(m) + '</span><span class="item-count">' + fmtCount(mi.Count) + '</span>';
      el.onclick = () => { selectMetric(m); closeSidebar(); };
      section.appendChild(el);
    });
    nav.appendChild(section);
  });

  // Other
  const known = new Set(CATEGORIES.flatMap(c => c.metrics));
  const others = data.filter(m => !known.has(m.Name));
  if (others.length) {
    const section = document.createElement('div');
    section.className = 'category';
    const labelEl = document.createElement('div');
    labelEl.className = 'category-label';
    labelEl.innerHTML = '<span class="category-dot" style="background:var(--muted)"></span>Other<span class="cat-arrow">▾</span>';
    labelEl.onclick = () => section.classList.toggle('collapsed');
    section.appendChild(labelEl);
    others.forEach(m => {
      const el = document.createElement('div');
      el.className = 'metric-item';
      el.id = 'mi-' + m.Name;
      el.innerHTML = '<span class="item-name">' + name(m.Name) + '</span><span class="item-count">' + fmtCount(m.Count) + '</span>';
      el.onclick = () => { selectMetric(m.Name); closeSidebar(); };
      section.appendChild(el);
    });
    nav.appendChild(section);
  }

  // Restore pending metric from URL hash
  if (pendingMetric) { selectMetric(pendingMetric); pendingMetric = null; }
}

// ── Select / navigate ──────────────────────────────────────────────────────
function selectMetric(metric) {
  currentMetric = metric;
  document.querySelectorAll('.metric-item').forEach(el => el.classList.remove('active'));
  const el = $('mi-' + metric);
  if (el) el.classList.add('active');

  $('toolbar-title').textContent   = name(metric);
  $('presets').style.display       = '';
  $('date-ctrls').style.display    = '';
  $('agg-ctrls').style.display     = '';
  $('bucket').value                = '';
  $('agg').value                   = '';
  $('dashboard-view').style.display = 'none';
  $('chart-view').style.display     = '';

  loadChart();
}

function showDashboard() {
  currentMetric = null;
  document.querySelectorAll('.metric-item').forEach(el => el.classList.remove('active'));
  $('toolbar-title').textContent     = 'Dashboard';
  $('presets').style.display         = 'none';
  $('date-ctrls').style.display      = 'none';
  $('agg-ctrls').style.display       = 'none';
  $('dashboard-view').style.display  = '';
  $('chart-view').style.display      = 'none';
  history.replaceState(null,'', location.pathname);
}

// ── Presets ────────────────────────────────────────────────────────────────
function applyPreset(days) {
  document.querySelectorAll('.preset-btn').forEach(b => b.classList.remove('active'));
  event.target.classList.add('active');
  $('from').value   = daysAgoStr(days - 1);
  $('to').value     = todayStr();
  $('bucket').value = '';
  loadChart();
}

function onDateChange() {
  document.querySelectorAll('.preset-btn').forEach(b => b.classList.remove('active'));
  loadChart();
}

// ── Time-of-day bands plugin ───────────────────────────────────────────────
const TIME_BANDS = [
  { start:  0, end:  6, color: 'rgba(15,10,40,0.55)',    label: 'Night'   },
  { start:  6, end: 12, color: 'rgba(255,190,60,0.07)',  label: 'Morning' },
  { start: 12, end: 18, color: 'rgba(100,180,255,0.05)', label: 'Day'     },
  { start: 18, end: 24, color: 'rgba(255,120,40,0.08)',  label: 'Evening' },
];
const timeBandsPlugin = {
  id: 'timeBands',
  beforeDraw(chart) {
    const labels = chart.data.labels;
    if (!labels || labels.length < 2) return;
    if (labels[0].length <= 10) return;

    const { ctx, scales: { x, y } } = chart;
    const top = y.top, bottom = y.bottom;
    const half = (x.getPixelForValue(1) - x.getPixelForValue(0)) / 2;
    const hourOf = lbl => parseInt(lbl.slice(11,13), 10);
    const bandOf = h => TIME_BANDS.find(b => h >= b.start && h < b.end);

    const dateOf = lbl => lbl.slice(0, 10);
    const weekdayOf = ds => {
      const d = new Date(ds + 'T12:00:00');
      return d.toLocaleDateString('en', { weekday: 'short' });
    };

    ctx.save();
    ctx.beginPath(); ctx.rect(x.left, top, x.right - x.left, bottom - top); ctx.clip();

    // Pass 1: time-of-day bands
    let cur = null, gStart = 0;
    const flush = (endIdx) => {
      if (!cur || endIdx < gStart) return;
      const x1 = x.getPixelForValue(gStart) - half;
      const x2 = x.getPixelForValue(endIdx)  + half;
      const w  = x2 - x1;
      ctx.fillStyle = cur.color;
      ctx.fillRect(x1, top, w, bottom - top);
      if (w > 40) {
        ctx.fillStyle = 'rgba(255,255,255,0.18)';
        ctx.font = '10px -apple-system,sans-serif';
        ctx.textAlign = 'center';
        ctx.fillText(cur.label, x1 + w / 2, top + 26);
      }
    };
    for (let i = 0; i < labels.length; i++) {
      const b = bandOf(hourOf(labels[i]));
      if (b !== cur) { flush(i - 1); cur = b; gStart = i; }
    }
    flush(labels.length - 1);

    // Pass 2: weekday header strip at top
    const DAY_H = 16;
    let curDay = null, dayStart = 0;
    const flushDay = (endIdx) => {
      if (!curDay || endIdx < dayStart) return;
      const x1 = x.getPixelForValue(dayStart) - half;
      const x2 = x.getPixelForValue(endIdx)   + half;
      const w  = x2 - x1;
      ctx.fillStyle = 'rgba(255,255,255,0.06)';
      ctx.fillRect(x1, top, w, DAY_H);
      if (w > 24) {
        ctx.fillStyle = 'rgba(255,255,255,0.55)';
        ctx.font = 'bold 9px -apple-system,sans-serif';
        ctx.textAlign = 'center';
        ctx.fillText(weekdayOf(curDay), x1 + w / 2, top + DAY_H - 4);
      }
      // day separator line
      ctx.strokeStyle = 'rgba(255,255,255,0.1)';
      ctx.lineWidth = 1;
      ctx.beginPath(); ctx.moveTo(x1, top); ctx.lineTo(x1, bottom); ctx.stroke();
    };
    for (let i = 0; i < labels.length; i++) {
      const d = dateOf(labels[i]);
      if (d !== curDay) { flushDay(i - 1); curDay = d; dayStart = i; }
    }
    flushDay(labels.length - 1);

    ctx.restore();
  }
};
Chart.register(timeBandsPlugin);

// ── Sleep stacked chart ────────────────────────────────────────────────────
async function loadSleepChart(from, to) {
  setLoading(true);
  const results = await Promise.all(SLEEP_PHASES.map(ph =>
    fetch('/api/metrics/data?metric=' + ph.metric + '&from=' + from + '&to=' + to + '&bucket=day&agg=AVG')
      .then(r => r.json())
  ));
  setLoading(false);

  const labelSet = new Set();
  results.forEach(r => (r.points || []).forEach(p => labelSet.add(p.date)));
  const labels = [...labelSet].sort();
  const statsRow = $('stats-row');

  if (!labels.length) {
    statsRow.innerHTML = '<div style="color:var(--muted);padding:8px">No sleep data for this range</div>';
    if (chart) { chart.destroy(); chart = null; }
    return;
  }

  const ptMap = results.map(r => Object.fromEntries((r.points||[]).map(p => [p.date, p.qty])));
  const datasets = SLEEP_PHASES.map((ph, i) => ({
    label: ph.label,
    data: labels.map(l => ptMap[i][l] ?? 0),
    backgroundColor: ph.color + 'cc', borderColor: ph.color, borderWidth: 1, stack: 'sleep',
  }));

  const avg = (arr) => arr.length ? arr.reduce((a,b)=>a+b,0)/arr.length : 0;
  statsRow.innerHTML =
    chip('Nights', labels.length, '') +
    chip('Avg total', fmt2(avg(labels.map(l => SLEEP_PHASES.reduce((s,_,i) => s+(ptMap[i][l]??0),0)))), 'h') +
    chip('Avg deep',  fmt2(avg((results[0].points||[]).map(p=>p.qty))), 'h') +
    chip('Avg REM',   fmt2(avg((results[1].points||[]).map(p=>p.qty))), 'h');

  if (chart) { chart.destroy(); chart = null; }
  chart = new Chart($('chart').getContext('2d'), {
    type: 'bar',
    data: { labels: labels.map(fmtAxisDate), datasets },
    options: {
      responsive: true, maintainAspectRatio: false,
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: { display: true, labels: { color: '#94a3b8', boxWidth: 12, font: { size: 11 } } },
        tooltip: { backgroundColor:'#1a1d2e', borderColor:'#252836', borderWidth:1, titleColor:'#94a3b8', bodyColor:'#e2e8f0',
          callbacks: { label: ctx => ' ' + ctx.dataset.label + ': ' + fmt2(ctx.parsed.y) + ' h' } }
      },
      scales: {
        x: { stacked:true, ticks:{ color:'#64748b', font:{size:11} }, grid:{color:'#1a1d2e'} },
        y: { stacked:true, ticks:{ color:'#64748b', font:{size:11}, callback: v => v+'h' }, grid:{color:'#1a1d2e'} }
      }
    }
  });
  pushHash();
}

// ── Chart ──────────────────────────────────────────────────────────────────
async function loadChart() {
  if (!currentMetric) return;
  const from   = $('from').value;
  const to     = $('to').value;
  const bucket = $('bucket').value;
  const agg    = $('agg').value;

  if (SLEEP_METRICS.has(currentMetric) && !bucket && !agg) return loadSleepChart(from, to);

  setLoading(true);
  let url = '/api/metrics/data?metric=' + encodeURIComponent(currentMetric) + '&from=' + from + '&to=' + to;
  if (bucket) url += '&bucket=' + bucket;
  if (agg)    url += '&agg=' + agg;

  const res  = await fetch(url);
  const data = await res.json();
  setLoading(false);
  const pts = data.points || [];

  const statsRow = $('stats-row');
  if (pts.length) {
    const vals = pts.map(p => p.qty);
    const avg  = vals.reduce((a,b)=>a+b,0) / vals.length;
    const unit = fmtUnit(unitsMap[currentMetric] || '');
    statsRow.innerHTML =
      chip('Points', pts.length.toLocaleString(), '') +
      chip('Avg',    fmt2(avg),            unit) +
      chip('Min',    fmt2(Math.min(...vals)), unit) +
      chip('Max',    fmt2(Math.max(...vals)), unit);
  } else {
    statsRow.innerHTML = '<div style="color:var(--muted);padding:8px">No data for this range</div>';
  }

  if (!pts.length) { if (chart) { chart.destroy(); chart = null; } pushHash(); return; }

  const labels  = pts.map(p => p.date);
  const avgVals = pts.map(p => p.qty);
  const minVals = pts.map(p => p.min);
  const maxVals = pts.map(p => p.max);
  const isBar   = BAR_METRICS.has(currentMetric);
  const hasRange = !isBar && pts.length > 1 && (maxVals[0] - minVals[0]) > 0.01;
  const sparse  = pts.length > 200;

  const cat   = catOf(currentMetric);
  const cmap  = { '--heart':'#f87171','--activity':'#34d399','--mobility':'#fbbf24','--sleep':'#a78bfa','--env':'#22d3ee','--accent':'#4f8ef7' };
  const ckey  = cat ? cat.color.replace('var(','').replace(')','') : '--accent';
  const lineColor = cmap[ckey] || '#4f8ef7';

  if (chart) { chart.destroy(); chart = null; }
  const ctx = $('chart').getContext('2d');
  const fmtLabel = l => fmtAxisDate(l);

  const datasets = [];
  if (hasRange) {
    datasets.push({ data: maxVals, borderWidth:0, pointRadius:0, fill:'+1', backgroundColor: lineColor+'22', tension:0.2, label:'_max' });
    datasets.push({ data: minVals, borderWidth:0, pointRadius:0, fill:false, tension:0.2, label:'_min' });
  }
  datasets.push({
    label: name(currentMetric),
    data: avgVals,
    borderColor: lineColor,
    backgroundColor: isBar ? lineColor+'bb' : lineColor+'18',
    borderWidth: isBar ? 0 : 1.5,
    pointRadius: sparse ? 0 : 2,
    tension: 0.2,
    fill: isBar ? false : !hasRange,
    type: isBar ? 'bar' : 'line',
  });

  chart = new Chart(ctx, {
    type: isBar ? 'bar' : 'line',
    data: { labels, datasets },
    options: {
      responsive: true, maintainAspectRatio: false,
      interaction: { mode:'index', intersect:false },
      plugins: {
        legend: { display: false },
        tooltip: {
          backgroundColor:'#1a1d2e', borderColor:'#252836', borderWidth:1,
          titleColor:'#94a3b8', bodyColor:'#e2e8f0',
          callbacks: {
            title: items => fmtLabel(items[0].label),
            label: ctx => {
              if (ctx.dataset.label?.startsWith('_')) return null;
              return ' ' + fmt2(ctx.parsed.y) + ' ' + fmtUnit(unitsMap[currentMetric]||'');
            }
          }
        }
      },
      scales: {
        x: {
          ticks: {
            color:'#64748b', maxTicksLimit:10, font:{size:11},
            callback: (_, i) => fmtLabel(labels[i]),
          },
          grid: { color:'#1a1d2e' }
        },
        y: { beginAtZero: isBar, ticks:{ color:'#64748b', font:{size:11} }, grid:{ color:'#1a1d2e' } }
      }
    }
  });
  pushHash();
}

function setLoading(on) {
  $('chart-loading').style.display = on ? 'flex' : 'none';
}
</script>
</body>
</html>`
