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
      if (sparklineChart) { sparklineChart.destroy(); sparklineChart = null; }
      sparklineChart = new Chart(el, {
        type: 'line',
        data: {
          labels: labels,
          datasets: [{
            data: vals,
            borderColor: 'rgba(255,255,255,0.9)',
            backgroundColor: 'rgba(255,255,255,0.12)',
            fill: true, borderWidth: 2, pointRadius: 0, tension: 0.4
          }]
        },
        options: {
          responsive: true, maintainAspectRatio: false,
          animation: { duration: 600 },
          plugins: {
            legend: { display: false },
            tooltip: {
              backgroundColor: 'rgba(0,0,0,0.7)',
              titleColor: '#fff', bodyColor: '#fff', padding: 6,
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

// ---- Correlation chart (activity load vs HRV) ----
var corrChart = null;
function loadCorrelationChart(canvasId, data) {
  if (corrChart) { corrChart.destroy(); corrChart = null; }
  var sorted = data.slice().sort(function(a, b) { return a.date > b.date ? 1 : -1; });
  var lang = document.documentElement.lang || 'en';
  var localeCode = lang === 'ru' ? 'ru' : lang === 'sr' ? 'sr-Latn' : 'en';
  var labels = sorted.map(function(p) {
    var d = new Date(p.date + 'T12:00:00');
    return d.toLocaleDateString(localeCode, { weekday: 'short', month: 'short', day: 'numeric' });
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
  { metric:'step_count', labelKey:'Steps', color:'#059669', type:'bar' },
  { metric:'heart_rate', labelKey:'Heart Rate', color:'#e11d48', type:'line' },
  { metric:'sleep_total', labelKey:'Sleep', color:'#7c3aed', type:'bar' },
  { metric:'heart_rate_variability', labelKey:'HRV', color:'#d97706', type:'line' },
  { metric:'readiness', labelKey:'Readiness', color:'#0ea5e9', type:'line', virtual:true }
];
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
      var latestVal = vals[vals.length-1];
      var displayVal = f.virtual ? (Math.round(latestVal) + '%') : fmtVal(latestVal, '');
      wrap.innerHTML = '<div class="trend-card-header"><div class="trend-card-title">' + f.labelKey + '</div><div class="trend-card-value">' + displayVal + '</div></div><div class="trend-card-canvas"><canvas></canvas></div>';
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

// ---- Sleep stacked chart ----
var SLEEP_PHASES = [
  { metric:'sleep_deep', label:'Deep', color:'#6366f1' },
  { metric:'sleep_rem',  label:'REM',  color:'#a78bfa' },
  { metric:'sleep_core', label:'Core', color:'#93c5fd' },
  { metric:'sleep_awake',label:'Awake',color:'#fbbf24' }
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
      return { label: ph.label, data: labels.map(function(l) { return ptMap[i][l] || 0; }),
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
            callbacks: { label: function(ctx) { return ' ' + ctx.dataset.label + ': ' + fmt2(ctx.parsed.y) + ' h'; } } }
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
var SLEEP_METRICS = new Set(['sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake']);

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
  return '<div class="stat-chip"><div class="s-label">' + label + '</div><div class="s-value">' + value + (unit ? ' <span style="font-size:12px;color:var(--muted)">' + unit + '</span>' : '') + '</div></div>';
}

// Helper: loading state
function setLoading(on) {
  var el = document.getElementById('chart-loading');
  if (el) el.style.display = on ? '' : 'none';
}
