// charts.js — Chart.js wrappers for Health Dashboard
'use strict';

var SOURCE_PALETTE = ['#2563eb','#e11d48','#059669','#d97706','#7c3aed','#06b6d4','#ea580c','#0891b2'];

// Track chart instances per canvas to destroy before reuse
var _chartInstances = {};
function _createChart(canvasId, config) {
  if (_chartInstances[canvasId]) {
    _chartInstances[canvasId].destroy();
  }
  var el = document.getElementById(canvasId);
  if (!el) return null;
  var c = new Chart(el.getContext('2d'), config);
  _chartInstances[canvasId] = c;
  return c;
}

// ---- Time bands plugin ----
var TIME_BANDS = [
  { start:0, end:6, color:'rgba(100,80,140,0.06)', label:'Night' },
  { start:6, end:12, color:'rgba(255,190,60,0.05)', label:'Morning' },
  { start:12, end:18, color:'rgba(100,180,255,0.04)', label:'Day' },
  { start:18, end:24, color:'rgba(255,120,40,0.05)', label:'Evening' }
];
Chart.register({
  id: 'timeBands',
  beforeDraw: function(chart) {
    var labels = chart.data.labels;
    if (!labels || labels.length < 2 || labels[0].length <= 10) return;
    var ctx = chart.ctx, x = chart.scales.x, y = chart.scales.y;
    var top = y.top, bottom = y.bottom;
    var half = (x.getPixelForValue(1) - x.getPixelForValue(0)) / 2;
    function hourOf(lbl) { return parseInt(lbl.slice(11,13), 10); }
    function bandOf(h) { return TIME_BANDS.find(function(b) { return h >= b.start && h < b.end; }); }
    ctx.save();
    ctx.beginPath(); ctx.rect(x.left, top, x.right - x.left, bottom - top); ctx.clip();
    var cur = null, gStart = 0;
    function flush(endIdx) {
      if (!cur || endIdx < gStart) return;
      var x1 = x.getPixelForValue(gStart) - half;
      var x2 = x.getPixelForValue(endIdx) + half;
      ctx.fillStyle = cur.color;
      ctx.fillRect(x1, top, x2 - x1, bottom - top);
    }
    for (var i = 0; i < labels.length; i++) {
      var b = bandOf(hourOf(labels[i]));
      if (b !== cur) { flush(i - 1); cur = b; gStart = i; }
    }
    flush(labels.length - 1);
    ctx.restore();
  }
});

// ---- Readiness sparkline (hero block) ----
var sparklineChart = null;
function loadReadinessSparkline(canvasId) {
  fetch('/api/readiness-history?days=30')
    .then(function(r){return r.json()})
    .then(function(d) {
      var pts = d.points || [];
      if (pts.length < 3) return;
      var el = document.getElementById(canvasId);
      if (!el) return;
      el.parentElement.parentElement.style.display = '';
      var labels = pts.map(function(p){return p.date;});
      var vals = pts.map(function(p){return p.score;});
      var ptStart = vals[0];
      var ptEnd = vals[vals.length - 1];
      var diff = ptEnd - ptStart;
      var lineColor = 'gray';
      var fillColor = 'rgba(128,128,128,0.15)';
      if (diff >= 3) {
        lineColor = '#059669'; // var(--good)
        fillColor = 'rgba(5, 150, 105, 0.15)';
      } else if (diff <= -3) {
        lineColor = '#e11d48'; // var(--low)
        fillColor = 'rgba(225, 29, 72, 0.15)';
      }

      if (sparklineChart) { sparklineChart.destroy(); sparklineChart = null; }
      sparklineChart = new Chart(el, {
        type: 'line',
        data: {
          labels: labels,
          datasets: [{
            data: vals,
            borderColor: lineColor,
            backgroundColor: fillColor,
            fill: true, borderWidth: 2, pointRadius: 0, tension: 0.4
          }]
        },
        options: {
          responsive: true, maintainAspectRatio: false,
          animation: { duration: 600 },
          plugins: {
            legend: { display: false },
            tooltip: {
              backgroundColor: 'var(--surface)',
              titleColor: 'var(--text)', bodyColor: 'var(--text)', borderColor: 'var(--border)', borderWidth: 1, padding: 6,
              callbacks: {
                title: function(items) { return fmtAxisDate(items[0].label); },
                label: function(ctx) { return ' ' + Math.round(ctx.parsed.y) + '%'; }
              }
            }
          },
          scales: { x: { display: false }, y: { display: false, min: 0, max: 100 } },
          elements: { point: { radius: 0, hoverRadius: 4 } }
        }
      });
    })
    .catch(function(){});
}

// ---- Energy Bank EOD sparkline (14d) ----
//
// Lives next to the live energy bar in the hero block. Hidden when fewer
// than 3 historical snapshots exist — a flat 1-2 point line tells the user
// nothing and pre-persistence days won't have rows yet. Verdict colours
// the line so e.g. a stretch of "rest" days reads at a glance.
var energySparklineChart = null;
function loadEnergySparkline(canvasId) {
  fetch('/api/energy-history?days=14')
    .then(function(r){return r.json()})
    .then(function(d) {
      var pts = d.points || [];
      if (pts.length < 3) return;
      var el = document.getElementById(canvasId);
      if (!el) return;
      var wrap = document.getElementById('energy-sparkline-wrap');
      if (wrap) wrap.hidden = false;
      var labels = pts.map(function(p){return p.date;});
      var vals = pts.map(function(p){return p.current_eod;});
      var lastVerdict = pts[pts.length - 1].verdict;
      var verdictColors = {
        rest:            { line: '#e11d48', fill: 'rgba(225,29,72,0.15)' },
        active_recovery: { line: '#f59e0b', fill: 'rgba(245,158,11,0.15)' },
        moderate:        { line: '#0ea5e9', fill: 'rgba(14,165,233,0.15)' },
        push_hard:       { line: '#059669', fill: 'rgba(5,150,105,0.15)' }
      };
      var c = verdictColors[lastVerdict] || verdictColors.moderate;

      if (energySparklineChart) { energySparklineChart.destroy(); energySparklineChart = null; }
      energySparklineChart = new Chart(el, {
        type: 'line',
        data: {
          labels: labels,
          datasets: [{
            data: vals,
            borderColor: c.line,
            backgroundColor: c.fill,
            fill: true, borderWidth: 2, pointRadius: 0, tension: 0.4
          }]
        },
        options: {
          responsive: true, maintainAspectRatio: false,
          animation: { duration: 600 },
          plugins: {
            legend: { display: false },
            tooltip: {
              backgroundColor: 'var(--surface)',
              titleColor: 'var(--text)', bodyColor: 'var(--text)', borderColor: 'var(--border)', borderWidth: 1, padding: 6,
              callbacks: {
                title: function(items) { return fmtAxisDate(items[0].label); },
                label: function(ctx) { return ' ' + Math.round(ctx.parsed.y); }
              }
            }
          },
          scales: { x: { display: false }, y: { display: false, min: 0, max: 100 } },
          elements: { point: { radius: 0, hoverRadius: 4 } }
        }
      });
    })
    .catch(function(){});
}

// ---- Energy Bank v2 hourly chart (72h) ----
//
// Lives inside the hero <details> next to the v1 EOD sparkline. Reads
// the new /api/energy-history?granularity=hour endpoint (PR #41) which
// returns 5-minute buckets with bank, drain_delta, restore_delta,
// flags. Rendered as a continuous line with imputed segments dashed
// — visualises the trust-state distinction between sensor-backed and
// trailing-average-imputed buckets per ENERGY_BANK.md.
//
// Hidden when fewer than 3 points exist — same convention as the v1
// sparkline; a fresh tenant or a tenant that just had a stale-state
// gap shouldn't see a flat 1-2-point line.
//
// Verdict colouring intentionally NOT applied here: the v2 endpoint
// doesn't expose per-bucket verdict (it's a derived property of the
// iteration, not a stored column). The current hero verdict comes from
// the freshest same-day snapshot, while the chart stays value-only.
var energyHourlyChart = null;
function loadEnergyHourlyChart(canvasId) {
  // Guard before fetch: when the server renders the dashboard for a
  // tenant without an EnergyBank (fresh user, no metric data yet), the
  // entire hero-energy block including this canvas is omitted from the
  // template. Skipping the network call here avoids a wasted round
  // trip + needless server log line on every dashboard load for those
  // tenants.
  var el = document.getElementById(canvasId);
  if (!el) return;
  fetch('/api/energy-history?granularity=hour&hours=72')
    .then(function(r) { return r.json(); })
    .then(function(d) {
      var pts = d.points || [];
      if (pts.length < 3) return;
      var wrap = document.getElementById('energy-hourly-wrap');
      if (wrap) wrap.hidden = false;

      // Imputed flag is per-point. Chart.js segment styling uses
      // borderDash on a SEGMENT (the line between two points), and
      // we mark the segment as imputed when EITHER endpoint is
      // imputed — so a single imputed bucket produces dashes both
      // entering and leaving it. Matches the ENERGY_BANK.md UX rule
      // ("dotted-line rendering for imputed buckets").
      var imputed = pts.map(function(p) {
        var f = p.flags || [];
        return f.indexOf('imputed_sleep') !== -1 || f.indexOf('imputed_activity') !== -1;
      });

      if (energyHourlyChart) { energyHourlyChart.destroy(); energyHourlyChart = null; }
      energyHourlyChart = new Chart(el, {
        type: 'line',
        data: {
          labels: pts.map(function(p) { return p.ts; }),
          datasets: [{
            data: pts.map(function(p) { return p.bank; }),
            borderColor: '#0ea5e9',
            backgroundColor: 'rgba(14,165,233,0.10)',
            fill: true, borderWidth: 2, pointRadius: 0, tension: 0.35,
            segment: {
              borderDash: function(ctx) {
                if (imputed[ctx.p0DataIndex] || imputed[ctx.p1DataIndex]) return [4, 3];
                return undefined;
              }
            }
          }]
        },
        options: {
          responsive: true, maintainAspectRatio: false,
          animation: { duration: 400 },
          plugins: {
            legend: { display: false },
            tooltip: {
              backgroundColor: 'var(--surface)',
              titleColor: 'var(--text)', bodyColor: 'var(--text)',
              borderColor: 'var(--border)', borderWidth: 1, padding: 8,
              callbacks: {
                title: function(items) {
                  var ts = items[0].label;
                  var d = new Date(ts);
                  if (isNaN(d.getTime())) return ts;
                  return d.toLocaleString(undefined, {
                    month: 'short', day: 'numeric',
                    hour: '2-digit', minute: '2-digit'
                  });
                },
                label: function(ctx) {
                  var p = pts[ctx.dataIndex] || {};
                  var lines = ['bank ' + p.bank];
                  if (typeof p.drain_delta === 'number') lines.push('drain ' + p.drain_delta);
                  if (typeof p.restore_delta === 'number') lines.push('restore ' + p.restore_delta);
                  if (p.flags && p.flags.length) lines.push('flags: ' + p.flags.join(', '));
                  return lines;
                }
              }
            }
          },
          scales: {
            x: {
              ticks: {
                maxRotation: 0, autoSkip: true, maxTicksLimit: 6,
                color: 'var(--muted)', font: { size: 10 },
                callback: function(value) {
                  var label = this.getLabelForValue(value);
                  var d = new Date(label);
                  if (isNaN(d.getTime())) return label;
                  // Show day/hour at midnight, just hour otherwise —
                  // a 72h chart spans 3 calendar boundaries, makes
                  // them easy to spot.
                  if (d.getHours() === 0) {
                    return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
                  }
                  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
                }
              },
              grid: { display: false }
            },
            y: {
              min: 0, max: 100,
              ticks: { color: 'var(--muted)', font: { size: 10 }, stepSize: 25 },
              grid: { color: 'rgba(120,113,108,0.12)' }
            }
          },
          elements: { point: { radius: 0, hoverRadius: 4 } }
        }
      });
    })
    .catch(function() {});
}

// ---- Correlation chart (activity load vs HRV) ----
var corrChart = null;
function loadCorrelationChart(canvasId, data) {
  if (corrChart) { corrChart.destroy(); corrChart = null; }
  var sorted = data.slice().sort(function(a, b) { return a.date > b.date ? 1 : -1; });
  var labels = sorted.map(function(p) {
    return fmtAxisDate(p.date, true);
  });
  var loadVals = sorted.map(function(p) { return p.load; });
  var hrvVals = sorted.map(function(p) { return p.hrv; });

  var el = document.getElementById(canvasId);
  if (!el) return;
  corrChart = new Chart(el.getContext('2d'), {
    type: 'line',
    data: {
      labels: labels,
      datasets: [
        {
          label: 'Activity load',
          data: loadVals,
          borderColor: '#059669', backgroundColor: 'rgba(5,150,105,0.1)',
          fill: true, tension: 0.4, borderWidth: 2.5,
          pointRadius: 4, pointBackgroundColor: '#059669', yAxisID: 'y'
        },
        {
          label: 'HRV',
          data: hrvVals,
          borderColor: '#e11d48', backgroundColor: 'rgba(225,29,72,0.08)',
          fill: true, tension: 0.4, borderWidth: 2.5,
          pointRadius: 4, pointBackgroundColor: '#e11d48', yAxisID: 'y1'
        }
      ]
    },
    options: {
      responsive: true, maintainAspectRatio: false,
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: { display: false },
        tooltip: {
          backgroundColor: '#fff', borderColor: '#e7e5e4', borderWidth: 1,
          titleColor: '#78716c', bodyColor: '#1c1917',
          callbacks: { label: function(ctx) { return ' ' + ctx.dataset.label + ': ' + ctx.parsed.y.toFixed(1); } }
        }
      },
      scales: {
        x: { ticks: { color: '#78716c', font: { size: 11 } }, grid: { color: '#f0efed' } },
        y: { position: 'left', ticks: { color: '#059669', font: { size: 11 } }, grid: { color: '#f0efed' }, title: { display: true, text: 'Load %', color: '#059669', font: { size: 11 } } },
        y1: { position: 'right', ticks: { color: '#e11d48', font: { size: 11 } }, grid: { drawOnChartArea: false }, title: { display: true, text: 'HRV ms', color: '#e11d48', font: { size: 11 } } }
      }
    }
  });
}

// ---- Trend sparklines ----
var TRENDS = [
  { metric:'step_count', color:'#059669', type:'bar' },
  { metric:'heart_rate', color:'#e11d48', type:'line' },
  { metric:'sleep_total', color:'#7c3aed', type:'bar' },
  { metric:'heart_rate_variability', color:'#d97706', type:'line' },
  { metric:'readiness', color:'#0ea5e9', type:'line', virtual:true }
];
var TRENDS_I18N = {
  'en': { 'step_count': 'Steps', 'heart_rate': 'Heart Rate', 'sleep_total': 'Sleep', 'heart_rate_variability': 'HRV', 'readiness': 'Readiness' },
  'ru': { 'step_count': 'Шаги', 'heart_rate': 'Пульс', 'sleep_total': 'Сон', 'heart_rate_variability': 'ВСР', 'readiness': 'Готовность' }
};
var trendCharts = [];

function loadTrendCharts(containerId) {
  var container = document.getElementById(containerId);
  if (!container) return;
  container.innerHTML = '';
  trendCharts.forEach(function(c) { c.destroy(); });
  trendCharts.length = 0;
  var from30 = daysAgoStr(29), to30 = todayStr();
  Promise.all(TRENDS.map(function(f) {
    if (f.virtual) {
      return fetch('/api/readiness-history?days=30')
        .then(function(r){return r.json()})
        .then(function(d) { return { f: f, pts: (d.points || []).map(function(p){ return { date: p.date, qty: p.score }; }) }; })
        .catch(function() { return { f: f, pts: [] }; });
    }
    return fetch('/api/metrics/data?metric=' + encodeURIComponent(f.metric) + '&from=' + from30 + '&to=' + to30 + '&bucket=day')
      .then(function(r){return r.json()})
      .then(function(d) { return { f: f, pts: (d.points || []).filter(function(p){return p.qty > 0}) }; })
      .catch(function() { return { f: f, pts: [] }; });
  })).then(function(results) {
    results.forEach(function(r) {
      var f = r.f, pts = r.pts;
      if (!pts.length) return;
      var wrap = document.createElement('div');
      wrap.className = 'trend-card';
      wrap.style.cursor = 'pointer';
      wrap.onclick = function() { window.location.href = '/metrics/' + f.metric; };
      var vals = pts.map(function(p){return p.qty});
      var lang = document.documentElement.lang || 'en';
      var labelName = (TRENDS_I18N[lang] || TRENDS_I18N['en'])[f.metric] || f.metric;
      var latestVal = vals[vals.length-1];
      var displayVal = f.virtual ? (Math.round(latestVal) + '%') : fmtVal(latestVal, '');
      wrap.innerHTML = '<div class="trend-card-header"><div class="trend-card-title">' + labelName + '</div><div class="trend-card-value">' + displayVal + '</div></div><div class="trend-card-canvas"><canvas></canvas></div>';
      container.appendChild(wrap);
      var canvas = wrap.querySelector('canvas');
      var labels = pts.map(function(p){return fmtAxisDate(p.date)});
      var c = new Chart(canvas, {
        type: f.type,
        data: { labels: labels, datasets: [{ data: vals, borderColor: f.color, backgroundColor: f.type === 'bar' ? f.color + '55' : f.color + '15', fill: f.type === 'line', borderWidth: f.type === 'line' ? 2 : 1, pointRadius: 0, tension: 0.35, borderRadius: f.type === 'bar' ? 3 : 0 }] },
        options: {
          responsive: true, maintainAspectRatio: false,
          plugins: {
            legend: { display: false },
            tooltip: {
              backgroundColor: '#fff', borderColor: '#e7e5e4', borderWidth: 1,
              titleColor: '#78716c', bodyColor: '#1c1917', padding: 8,
              callbacks: {
                title: function(items) { return fmtAxisDate(items[0].label); },
                label: function(ctx) { return ' ' + fmt2(ctx.parsed.y); }
              }
            }
          },
          scales: { x: { display: false }, y: { display: false, beginAtZero: f.type === 'bar' } },
          elements: { point: { radius: 0, hoverRadius: 4 } }
        }
      });
      trendCharts.push(c);
    });
  });
}

// ---- Metric card mini-sparklines (Bevel-style) ----
// Replaces the standalone Trends section. Each metric card carries
// `<canvas data-metric="...">`; we fetch 14 days of data and render a
// tiny in-card chart so the snapshot + history sit side-by-side.
var METRIC_SPARKLINE_COLORS = {
  'step_count':              '#059669',
  'sleep_total':             '#7c3aed',
  'heart_rate_variability':  '#d97706',
  'resting_heart_rate':      '#e11d48',
  'respiratory_rate':        '#0ea5e9'
};
var metricCardCharts = [];

function loadMetricCardSparklines() {
  var canvases = document.querySelectorAll('.metric-card-sparkline canvas[data-metric]');
  if (!canvases.length) return;
  metricCardCharts.forEach(function(c) { c.destroy(); });
  metricCardCharts.length = 0;
  var from = daysAgoStr(13), to = todayStr();
  canvases.forEach(function(canvas) {
    var metric = canvas.getAttribute('data-metric');
    if (!metric) return;
    fetch('/api/metrics/data?metric=' + encodeURIComponent(metric) + '&from=' + from + '&to=' + to + '&bucket=day')
      .then(function(r){return r.json()})
      .then(function(d) {
        var pts = (d.points || []).filter(function(p){ return p.qty > 0; });
        if (pts.length < 2) return;
        var color = METRIC_SPARKLINE_COLORS[metric] || '#6b7280';
        var c = new Chart(canvas, {
          type: 'line',
          data: {
            labels: pts.map(function(p){ return p.date; }),
            datasets: [{
              data: pts.map(function(p){ return p.qty; }),
              borderColor: color, backgroundColor: color + '20',
              fill: true, tension: 0.35, borderWidth: 1.5, pointRadius: 0
            }]
          },
          options: {
            responsive: true, maintainAspectRatio: false,
            plugins: { legend: { display: false }, tooltip: { enabled: false } },
            scales: { x: { display: false }, y: { display: false } },
            elements: { point: { radius: 0 } }
          }
        });
        metricCardCharts.push(c);
      })
      .catch(function() { /* silent — sparkline is decorative */ });
  });
}

// ---- Sleep stacked chart ----
// Stack order (bottom → top): deep → rem → core → unspecified → awake.
// `sleep_unspecified` (5th band, v2.3) sits between core and awake — it is
// real asleep time from sources that don't classify into stages (RingConn,
// iPhone Sleep Schedule, older Apple Watch). Pre-v2.3 this time inflated
// the Core band, so a "Core-only" night was a lie about what was measured.
// Neutral grey-blue colour distinguishes it from both stage colours and
// the warm Awake tone.
//
// `label` is the English fallback. The page template (metric_detail.html)
// injects `window.SLEEP_PHASE_LABELS` keyed by metric name for the active
// language; _sleepLabel() reads that map at chart-build time and falls
// back to the literal here when the page hasn't bridged i18n (older
// renders, embedded charts in third-party tools). Same pattern for the
// tooltip hint via `window.CHART_SLEEP_UNSPECIFIED_HINT`. Issue #80.
function _sleepLabel(metric, fallback) {
  var m = window.SLEEP_PHASE_LABELS;
  return (m && m[metric]) || fallback;
}
function _sleepUnspecifiedHint() {
  return window.CHART_SLEEP_UNSPECIFIED_HINT
    || 'source did not report deep/REM/core breakdown';
}
var SLEEP_PHASES = [
  { metric:'sleep_deep',        label:'Deep',              color:'#6366f1' },
  { metric:'sleep_rem',         label:'REM',               color:'#a78bfa' },
  { metric:'sleep_core',        label:'Core',              color:'#93c5fd' },
  { metric:'sleep_unspecified', label:'Asleep (no stages)',color:'#9ba3b0' },
  { metric:'sleep_awake',       label:'Awake',             color:'#fbbf24' }
];

function loadSleepChart(canvasId, from, to) {
  var el = document.getElementById(canvasId);
  if (!el) return;
  setLoading(true);
  Promise.all(SLEEP_PHASES.map(function(ph) {
    return fetch('/api/metrics/data?metric=' + ph.metric + '&from=' + from + '&to=' + to + '&bucket=day&agg=AVG')
      .then(function(r){return r.json()});
  })).then(function(results) {
    setLoading(false);
    var labelSet = new Set();
    results.forEach(function(r) { (r.points || []).forEach(function(p) { labelSet.add(p.date); }); });
    var labels = Array.from(labelSet).sort();
    if (!labels.length) {
      var sr = document.getElementById('stats-row');
      if (sr) sr.innerHTML = '<div style="color:var(--muted);padding:8px">No sleep data for this range</div>';
      return;
    }
    var ptMap = results.map(function(r) {
      var m = {}; (r.points||[]).forEach(function(p) { m[p.date] = p.qty; }); return m;
    });
    var datasets = SLEEP_PHASES.map(function(ph, i) {
      return { label: _sleepLabel(ph.metric, ph.label),
               // Stash the stable metric key so the tooltip afterLabel
               // hook can check by enum instead of by localized label.
               metric: ph.metric,
               data: labels.map(function(l) { return ptMap[i][l] || 0; }),
               backgroundColor: ph.color + 'cc', borderColor: ph.color, borderWidth: 1,
               stack: 'sleep', borderRadius: 3 };
    });
    _createChart(canvasId, {
      type: 'bar',
      data: { labels: labels.map(fmtAxisDate), datasets: datasets },
      options: {
        responsive: true, maintainAspectRatio: false,
        interaction: { mode: 'index', intersect: false },
        plugins: {
          legend: { display: true, labels: { color:'#78716c', boxWidth: 12, font: { size: 12 } } },
          tooltip: { backgroundColor:'#fff', borderColor:'#e7e5e4', borderWidth:1, titleColor:'#78716c', bodyColor:'#1c1917',
            callbacks: {
              label: function(ctx) { return ' ' + ctx.dataset.label + ': ' + fmt2(ctx.parsed.y) + ' h'; },
              // Hint about what "no stages" means — appears only on the
              // sleep_unspecified band so existing tooltips stay quiet.
              // Check by stable metric key, not localized label, so the
              // hint fires correctly in every language (issue #80).
              afterLabel: function(ctx) {
                if (ctx.dataset.metric === 'sleep_unspecified' && ctx.parsed.y > 0) {
                  return '  (' + _sleepUnspecifiedHint() + ')';
                }
                return null;
              }
            } }
        },
        scales: {
          x: { stacked:true, ticks:{ color:'#78716c', font:{size:11} }, grid:{ color:'#f0efed' } },
          y: { stacked:true, ticks:{ color:'#78716c', font:{size:11}, callback: function(v) { return v+'h'; } }, grid:{ color:'#f0efed' } }
        }
      }
    });
  }).catch(function(e) { setLoading(false); console.error('loadSleepChart error:', e); });
}

// ---- Readiness history chart ----
function loadReadinessChart(canvasId, from, to) {
  var el = document.getElementById(canvasId);
  if (!el) return;
  setLoading(true);
  var fromD = new Date(from + 'T12:00:00');
  var toD = new Date(to + 'T12:00:00');
  var days = Math.round((toD - fromD) / 86400000) + 1;
  fetch('/api/readiness-history?days=' + days)
    .then(function(r){return r.json()})
    .then(function(d) {
      setLoading(false);
      var pts = (d.points || []).filter(function(p){ return p.date >= from && p.date <= to; });
      if (!pts.length) {
        var sr = document.getElementById('stats-row');
        if (sr) sr.innerHTML = '<div style="color:var(--muted);padding:8px">No data for this range</div>';
        return;
      }
      var labels = pts.map(function(p){ return p.date; });
      var vals = pts.map(function(p){ return p.score; });
      _createChart(canvasId, {
        type: 'line',
        data: {
          labels: labels,
          datasets: [{
            label: 'Readiness',
            data: vals, borderColor: '#0ea5e9', backgroundColor: '#0ea5e915',
            fill: true, borderWidth: 2, pointRadius: 2, tension: 0.35
          }]
        },
        options: {
          responsive: true, maintainAspectRatio: false,
          plugins: {
            legend: { display: false },
            tooltip: {
              backgroundColor: '#fff', borderColor: '#e7e5e4', borderWidth: 1,
              titleColor: '#78716c', bodyColor: '#1c1917', padding: 8,
              callbacks: {
                title: function(items) { return fmtAxisDate(items[0].label); },
                label: function(ctx) { return ' Readiness: ' + Math.round(ctx.parsed.y) + '%'; }
              }
            }
          },
          scales: {
            x: { ticks: { maxTicksLimit: 8, color: '#a8a29e', font: { size: 11 } }, grid: { color: '#f5f5f4' } },
            y: { min: 0, max: 100, ticks: { color: '#a8a29e', font: { size: 11 }, callback: function(v){ return v + '%'; } }, grid: { color: '#f5f5f4' } }
          }
        }
      });
    })
    .catch(function(e) { setLoading(false); console.error('loadReadinessChart error:', e); });
}

// ---- Generic metric chart ----
var BAR_METRICS = new Set(['step_count','active_energy','basal_energy_burned','apple_exercise_time','apple_stand_time','flights_climbed','walking_running_distance','time_in_daylight','apple_stand_hour','breathing_disturbances']);
var SLEEP_METRICS = new Set(['sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_unspecified','sleep_awake']);

function loadMetricChart(canvasId, metric, from, to, bucket, agg, opts) {
  var el = document.getElementById(canvasId);
  if (!el) return;
  opts = opts || {};
  setLoading(true);
  var url = '/api/metrics/data?metric=' + encodeURIComponent(metric) + '&from=' + from + '&to=' + to;
  if (bucket) url += '&bucket=' + bucket;
  if (agg) url += '&agg=' + agg;
  if (opts.bySource) url += '&by_source=1';

  fetch(url)
    .then(function(r){return r.json()})
    .then(function(data) {
      setLoading(false);
      var pts = data.points || [];
      if (!pts.length) {
        var sr = document.getElementById('stats-row');
        if (sr) sr.innerHTML = '<div style="color:var(--muted);padding:8px">No data for this range</div>';
        if (_chartInstances[canvasId]) { _chartInstances[canvasId].destroy(); delete _chartInstances[canvasId]; }
        return;
      }
      var labels = pts.map(function(p){return p.date});
      var vals = pts.map(function(p){return p.qty});
      var isBar = BAR_METRICS.has(metric);
      var lineColor = opts.color || '#2563eb';

      // Stats row
      var sr = document.getElementById('stats-row');
      if (sr) {
        var avgV = vals.reduce(function(a,b){return a+b},0) / vals.length;
        sr.innerHTML = chip('Points', pts.length, '') + chip('Avg', fmt2(avgV), '') + chip('Min', fmt2(Math.min.apply(null,vals)), '') + chip('Max', fmt2(Math.max.apply(null,vals)), '');
      }

      _createChart(canvasId, {
        type: isBar ? 'bar' : 'line',
        data: {
          labels: labels,
          datasets: [{
            label: metric,
            data: vals,
            borderColor: lineColor,
            backgroundColor: isBar ? lineColor+'77' : lineColor+'12',
            borderWidth: isBar ? 0 : 2,
            pointRadius: pts.length > 200 ? 0 : 2,
            tension: 0.2,
            fill: !isBar,
            borderRadius: isBar ? 4 : 0
          }]
        },
        options: {
          responsive: true, maintainAspectRatio: false,
          interaction: { mode:'index', intersect:false },
          plugins: {
            legend: { display: false },
            tooltip: {
              backgroundColor:'#fff', borderColor:'#e7e5e4', borderWidth:1,
              titleColor:'#78716c', bodyColor:'#1c1917',
              callbacks: {
                title: function(items) { return fmtAxisDate(items[0].label); },
                label: function(ctx) { return ' ' + fmt2(ctx.parsed.y); }
              }
            }
          },
          scales: {
            x: { ticks: { color:'#78716c', maxTicksLimit:10, font:{size:11}, callback: function(_,i) { return fmtAxisDate(labels[i]); } }, grid: { color:'#f0efed' } },
            y: { beginAtZero: isBar, ticks:{ color:'#78716c', font:{size:11} }, grid:{ color:'#f0efed' } }
          }
        }
      });
    })
    .catch(function(e) { setLoading(false); console.error('loadMetricChart error:', e); });
}

// Helper: stat chip
function chip(label, value, unit) {
  return '<div class="stat-chip"><div class="stat-chip__label">' + label + '</div><div class="stat-chip__value">' + value + (unit ? ' <span style="font-size:12px;color:var(--text-tertiary)">' + unit + '</span>' : '') + '</div></div>';
}

// Helper: loading state
function setLoading(on) {
  var el = document.getElementById('chart-loading');
  if (el) el.style.display = on ? '' : 'none';
}
