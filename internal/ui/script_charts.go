package ui

const jsCharts = `
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

// ---- Sleep stacked chart ----
function loadSleepChart(from, to) {
  if (bySourceEnabled) { return loadSleepBySourceChart(from, to); }
  setLoading(true);
  Promise.all(SLEEP_PHASES.map(function(ph) {
    return fetch('/api/metrics/data?metric=' + ph.metric + '&from=' + from + '&to=' + to + '&bucket=day&agg=AVG').then(function(r){return r.json()});
  })).then(function(results) {
    setLoading(false);
    var labelSet = new Set();
    results.forEach(function(r) { (r.points || []).forEach(function(p) { labelSet.add(p.date); }); });
    var labels = Array.from(labelSet).sort();
    if (!labels.length) {
      $('stats-row').innerHTML = '<div style="color:var(--muted);padding:8px">' + t('no_sleep_data') + '</div>';
      if (chart) { chart.destroy(); chart = null; }
      return;
    }
    var ptMap = results.map(function(r) {
      var m = {}; (r.points||[]).forEach(function(p) { m[p.date] = p.qty; }); return m;
    });
    var datasets = SLEEP_PHASES.map(function(ph, i) {
      return { label: t(ph.labelKey), data: labels.map(function(l) { return ptMap[i][l] || 0; }), backgroundColor: ph.color + 'cc', borderColor: ph.color, borderWidth: 1, stack: 'sleep', borderRadius: 3 };
    });
    function avg(arr) { return arr.length ? arr.reduce(function(a,b){return a+b},0)/arr.length : 0; }
    $('stats-row').innerHTML =
      chip(t('nights'), labels.length, '') +
      chip(t('avg_total'), fmt2(avg(labels.map(function(l) { return SLEEP_PHASES.reduce(function(s,_,i) { return s+(ptMap[i][l]||0); },0); }))), 'h') +
      chip(t('avg_deep'), fmt2(avg((results[0].points||[]).map(function(p){return p.qty}))), 'h') +
      chip(t('avg_rem'), fmt2(avg((results[1].points||[]).map(function(p){return p.qty}))), 'h');

    if (chart) { chart.destroy(); chart = null; }
    chart = new Chart($('chart').getContext('2d'), {
      type: 'bar',
      data: { labels: labels.map(fmtAxisDate), datasets: datasets },
      options: {
        responsive: true, maintainAspectRatio: false,
        interaction: { mode: 'index', intersect: false },
        plugins: {
          legend: { display: true, labels: { color:'#78716c', boxWidth: 12, font: { size: 12 } } },
          tooltip: { backgroundColor:'#fff', borderColor:'#e7e5e4', borderWidth:1, titleColor:'#78716c', bodyColor:'#1c1917', callbacks: { label: function(ctx) { return ' ' + ctx.dataset.label + ': ' + fmt2(ctx.parsed.y) + ' h'; } } }
        },
        scales: {
          x: { stacked:true, ticks:{ color:'#78716c', font:{size:11} }, grid:{ color:'#f0efed' } },
          y: { stacked:true, ticks:{ color:'#78716c', font:{size:11}, callback: function(v) { return v+'h'; } }, grid:{ color:'#f0efed' } }
        }
      }
    });
    pushHash();
  });
}

// ---- Readiness history chart ----
function loadReadinessChart(from, to) {
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
        $('stats-row').innerHTML = '<div style="color:var(--muted);padding:8px">' + t('no_data_range') + '</div>';
        if (chart) { chart.destroy(); chart = null; }
        return;
      }
      var labels = pts.map(function(p){ return p.date; });
      var vals = pts.map(function(p){ return p.score; });
      var avg = vals.reduce(function(a,b){return a+b;},0) / vals.length;
      $('stats-row').innerHTML =
        '<span>' + t('points') + ': ' + pts.length + '</span>' +
        '<span>' + t('avg') + ': ' + Math.round(avg) + '%</span>' +
        '<span>' + t('trend_readiness') + ': ' + Math.round(vals[vals.length-1]) + '%</span>';
      if (chart) { chart.destroy(); chart = null; }
      chart = new Chart($('chart'), {
        type: 'line',
        data: {
          labels: labels,
          datasets: [{
            label: t('trend_readiness'),
            data: vals,
            borderColor: '#0ea5e9',
            backgroundColor: '#0ea5e915',
            fill: true,
            borderWidth: 2,
            pointRadius: 2,
            tension: 0.35
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
                label: function(ctx) { return ' ' + t('trend_readiness') + ': ' + Math.round(ctx.parsed.y) + '%'; }
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
    .catch(function() { setLoading(false); });
}

// ---- Main chart ----
function loadChart() {
  if (!currentMetric) return;
  var from = $('from').value, to = $('to').value;
  var bucket = $('bucket').value, agg = $('agg').value;
  if (currentMetric === 'readiness') return loadReadinessChart(from, to);
  if (bySourceEnabled && SLEEP_METRICS.has(currentMetric) && !bucket && !agg) return loadSleepBySourceChart(from, to);
  if (SLEEP_METRICS.has(currentMetric) && !bucket && !agg) return loadSleepChart(from, to);
  setLoading(true);
  var url = '/api/metrics/data?metric=' + encodeURIComponent(currentMetric) + '&from=' + from + '&to=' + to;
  if (bucket) url += '&bucket=' + bucket;
  if (agg) url += '&agg=' + agg;
  if (bySourceEnabled) url += '&by_source=1';
  var mainPromise = fetch(url).then(function(r){return r.json()});
  var prevPromise = Promise.resolve({ points: [] });
  if (compareEnabled) {
    var fromD = new Date($('from').value + 'T12:00:00');
    var toD = new Date($('to').value + 'T12:00:00');
    var span = Math.round((toD - fromD) / 86400000) + 1;
    var prevFrom = new Date(fromD); prevFrom.setDate(prevFrom.getDate() - span);
    var prevTo = new Date(toD); prevTo.setDate(prevTo.getDate() - span);
    var prevUrl = url.replace('from='+$('from').value, 'from='+prevFrom.toISOString().slice(0,10)).replace('to='+$('to').value, 'to='+prevTo.toISOString().slice(0,10));
    prevPromise = fetch(prevUrl).then(function(r){return r.json()}).catch(function(){return {points:[]};});
  }
  Promise.all([mainPromise, prevPromise]).then(function(res) {
    var data = res[0], prevData = res[1];
    setLoading(false);
    if (data.by_source && data.points_by_source && data.points_by_source.length > 0) {
      lastPoints = [];
      renderMultiSourceChart(data.points_by_source);
      return;
    }
    var pts = data.points || [];
    var prevPts = prevData.points || [];
    lastPoints = pts;
    if (pts.length) {
      var vals = pts.map(function(p){return p.qty});
      var avgV = vals.reduce(function(a,b){return a+b},0) / vals.length;
      var unit = fmtUnit(unitsMap[currentMetric] || '');
      $('stats-row').innerHTML = chip(t('points'), pts.length.toLocaleString(), '') + chip(t('avg'), fmt2(avgV), unit) + chip(t('min'), fmt2(Math.min.apply(null,vals)), unit) + chip(t('max'), fmt2(Math.max.apply(null,vals)), unit);
    } else {
      $('stats-row').innerHTML = '<div style="color:var(--muted);padding:8px">' + t('no_data_range') + '</div>';
    }
    if (!pts.length) {
      if (chart) { chart.destroy(); chart = null; }
      // Auto-expand: if the current range has no data, find the full available range and reload.
      fetch('/api/metrics/range?metric=' + encodeURIComponent(currentMetric))
        .then(function(r){return r.json();})
        .then(function(range){
          if (range.min && range.min < $('from').value) {
            $('from').value = range.min;
            $('to').value = todayStr();
            document.querySelectorAll('.preset-btn').forEach(function(b){b.classList.remove('active');});
            loadChart();
          } else {
            pushHash();
          }
        }).catch(function(){ pushHash(); });
      return;
    }
    var labels = pts.map(function(p){return p.date});
    var avgVals = pts.map(function(p){return p.qty});
    var minVals = pts.map(function(p){return p.min});
    var maxVals = pts.map(function(p){return p.max});
    var isBar = BAR_METRICS.has(currentMetric);
    var hasRange = !isBar && pts.length > 1 && (maxVals[0] - minVals[0]) > 0.01;
    var sparse = pts.length > 200;
    var cat = catOf(currentMetric);
    var cmap = { heart:'#e11d48', activity:'#059669', mobility:'#f59e0b', sleep:'#7c3aed', env:'#06b6d4' };
    var lineColor = cat ? (cmap[cat.cat] || '#2563eb') : '#2563eb';
    if (chart) { chart.destroy(); chart = null; }
    var ctx = $('chart').getContext('2d');
    var prevVals = prevPts.length ? (function() { var step = prevPts.length / pts.length; return pts.map(function(_,i) { var pi = Math.round(i*step); return pi < prevPts.length ? prevPts[pi].qty : null; }); })() : [];
    var datasets = [];
    if (hasRange && !compareEnabled) {
      datasets.push({ data: maxVals, borderWidth:0, pointRadius:0, fill:'+1', backgroundColor: lineColor+'15', tension:0.2, label:'_max' });
      datasets.push({ data: minVals, borderWidth:0, pointRadius:0, fill:false, tension:0.2, label:'_min' });
    }
    datasets.push({ label: name(currentMetric), data: avgVals, borderColor: lineColor, backgroundColor: isBar ? lineColor+'77' : lineColor+'12', borderWidth: isBar ? 0 : 2, pointRadius: sparse ? 0 : 2, tension: 0.2, fill: isBar ? false : (!hasRange && !compareEnabled), type: isBar ? 'bar' : 'line', order: 1, borderRadius: isBar ? 4 : 0 });
    if (prevVals.length) {
      datasets.push({ label: t('previous_period'), data: prevVals, borderColor: lineColor+'55', backgroundColor: 'transparent', borderWidth: 1.5, borderDash:[4,3], pointRadius:0, tension:0.2, fill:false, type:'line', order:2 });
    }
    chart = new Chart(ctx, {
      type: isBar ? 'bar' : 'line',
      data: { labels: labels, datasets: datasets },
      options: {
        responsive: true, maintainAspectRatio: false,
        interaction: { mode:'index', intersect:false },
        plugins: {
          legend: { display: compareEnabled, labels: { color:'#78716c', boxWidth:12, font:{size:12}, filter: function(item) { return !item.text.startsWith('_'); } } },
          tooltip: { backgroundColor:'#fff', borderColor:'#e7e5e4', borderWidth:1, titleColor:'#78716c', bodyColor:'#1c1917', callbacks: { title: function(items) { return fmtAxisDate(items[0].label); }, label: function(ctx) { if (ctx.dataset.label && ctx.dataset.label.startsWith('_')) return null; var u = fmtUnit(unitsMap[currentMetric]||''); return ' ' + ctx.dataset.label + ': ' + fmt2(ctx.parsed.y) + (u ? ' '+u : ''); } } }
        },
        scales: {
          x: { ticks: { color:'#78716c', maxTicksLimit:10, font:{size:11}, callback: function(_,i) { return fmtAxisDate(labels[i]); } }, grid: { color:'#f0efed' } },
          y: { beginAtZero: isBar, ticks:{ color:'#78716c', font:{size:11} }, grid:{ color:'#f0efed' } }
        }
      }
    });
    pushHash();
  });
}

function renderMultiSourceChart(pointsBySource) {
  var dateSet = new Set();
  pointsBySource.forEach(function(sp) {
    sp.points.forEach(function(p) { dateSet.add(p.date); });
  });
  var labels = Array.from(dateSet).sort();
  var isBar = BAR_METRICS.has(currentMetric) || SLEEP_METRICS.has(currentMetric);

  var datasets = pointsBySource.map(function(sp, i) {
    var color = SOURCE_PALETTE[i % SOURCE_PALETTE.length];
    var ptMap = {};
    sp.points.forEach(function(p) { ptMap[p.date] = p.qty; });
    return {
      label: sp.source,
      data: labels.map(function(d) { return ptMap[d] != null ? ptMap[d] : null; }),
      borderColor: color,
      backgroundColor: isBar ? color + 'aa' : color + '20',
      borderWidth: isBar ? 0 : 2,
      type: isBar ? 'bar' : 'line',
      pointRadius: 2,
      tension: 0.2,
      fill: false,
      borderRadius: isBar ? 4 : 0
    };
  });

  var unit = fmtUnit(unitsMap[currentMetric] || '');
  $('stats-row').innerHTML = pointsBySource.map(function(sp, i) {
    var color = SOURCE_PALETTE[i % SOURCE_PALETTE.length];
    var vals = sp.points.map(function(p) { return p.qty; });
    var last = vals.length ? vals[vals.length-1] : null;
    return '<div class="stat-chip" style="border-left:3px solid '+color+'"><div class="s-label">'+sp.source+'</div><div class="s-value">'+(last != null ? fmt2(last) : '\u2014')+(unit ? ' <span style="font-size:12px;color:var(--muted)">'+unit+'</span>' : '')+'</div></div>';
  }).join('');

  if (chart) { chart.destroy(); chart = null; }
  var ctx = $('chart').getContext('2d');
  chart = new Chart(ctx, {
    type: isBar ? 'bar' : 'line',
    data: { labels: labels.map(fmtAxisDate), datasets: datasets },
    options: {
      responsive: true, maintainAspectRatio: false,
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: { display: true, labels: { color:'#78716c', boxWidth:12, font:{size:12} } },
        tooltip: { backgroundColor:'#fff', borderColor:'#e7e5e4', borderWidth:1, titleColor:'#78716c', bodyColor:'#1c1917',
          callbacks: {
            title: function(items) { return fmtAxisDate(items[0].label); },
            label: function(ctx) { return ' '+ctx.dataset.label+': '+fmt2(ctx.parsed.y)+(unit?' '+unit:''); }
          }
        }
      },
      scales: {
        x: { ticks:{ color:'#78716c', maxTicksLimit:10, font:{size:11}, callback: function(_,i){ return fmtAxisDate(labels[i]); } }, grid:{ color:'#f0efed' } },
        y: { beginAtZero: isBar, ticks:{ color:'#78716c', font:{size:11} }, grid:{ color:'#f0efed' } }
      }
    }
  });
  pushHash();
}

function loadSleepBySourceChart(from, to) {
  setLoading(true);
  fetch('/api/metrics/data?metric=sleep_total&from='+from+'&to='+to+'&bucket=day&agg=SUM&by_source=1')
    .then(function(r){return r.json()})
    .then(function(data) {
      setLoading(false);
      if (!data.points_by_source || !data.points_by_source.length) {
        $('stats-row').innerHTML = '<div style="color:var(--muted);padding:8px">'+t('no_sleep_data')+'</div>';
        if (chart) { chart.destroy(); chart = null; }
        return;
      }
      lastPoints = [];
      renderMultiSourceChart(data.points_by_source);
    });
}

function setLoading(on) { $('chart-loading').style.display = on ? 'flex' : 'none'; }
`
