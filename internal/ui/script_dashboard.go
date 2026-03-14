package ui

const jsDashboard = `
// ---- Render briefing ----
function renderBriefing(data) {
  if (!data) {
    $('readiness-score').textContent = '--';
    $('readiness-status').textContent = t('no_data');
    $('readiness-tip').textContent = t('start_syncing');
    return;
  }

  // Readiness card
  $('readiness-score').textContent = data.readiness_score || '--';
  $('readiness-status').textContent = data.readiness_label || '';
  $('readiness-tip').textContent = data.readiness_tip || '';
  $('recovery-pct-label').textContent = (data.recovery_pct || 0) + '%';
  $('recovery-bar-fill').style.width = (data.recovery_pct || 0) + '%';

  // Date in hero
  if (data.date) {
    var d = new Date(data.date + 'T12:00:00');
    var localeCode = LANG === 'ru' ? 'ru' : LANG === 'sr' ? 'sr-Latn' : 'en';
    var dateLabel = d.toLocaleDateString(localeCode, { weekday:'long', month:'long', day:'numeric' });
    var heroDate = dateLabel;
    var isToday = data.date === todayStr();
    if (!isToday) {
      var daysAgo = Math.round((new Date() - d) / 86400000);
      heroDate += '<span class="stale-badge">' + t('stale_prefix') + daysAgo + t('stale_suffix') + '</span>';
    }
    $('hero-date-strip').innerHTML = heroDate;
  }

  // Metric cards
  var mcGrid = $('metric-cards-grid');
  mcGrid.innerHTML = '';
  if (data.metric_cards && data.metric_cards.length) {
    data.metric_cards.forEach(function(mc) {
      var colors = METRIC_COLORS[mc.name] || { bg: '#f5f5f4', color: '#57534e' };
      var icon = METRIC_ICONS[mc.name] || '';
      var metricKey = METRIC_TO_KEY[mc.name] || '';
      var trendCls = mc.trend_pct > 3 ? 'positive' : mc.trend_pct < -3 ? 'negative' : 'neutral';
      var trendTxt = mc.trend_pct > 0 ? '+' + mc.trend_pct.toFixed(1) + '%' : mc.trend_pct < 0 ? mc.trend_pct.toFixed(1) + '%' : t('stable');
      var card = document.createElement('div');
      card.className = 'metric-card';
      if (metricKey) { card.onclick = function() { selectMetric(metricKey); }; }
      card.innerHTML = '<div class="metric-card-top"><div class="metric-card-icon" style="background:' + colors.bg + ';color:' + colors.color + '">' + icon + '</div><span class="metric-card-trend ' + trendCls + '">' + trendTxt + '</span></div><div class="metric-card-name">' + cardName(mc.name) + '</div><div class="metric-card-value">' + mc.value + '</div><div class="metric-card-unit">' + mc.unit + '</div>';
      mcGrid.appendChild(card);
    });
  }

  // Correlation chart
  if (data.correlation && data.correlation.length > 1) {
    $('correlation-section').style.display = '';
    renderCorrelationChart(data.correlation);
  }

  // Insights
  if (data.insights && data.insights.length) {
    $('insights-panel').style.display = '';
    var list = $('insights-list');
    list.innerHTML = '';
    data.insights.forEach(function(ins) {
      var li = document.createElement('li');
      li.innerHTML = '<div class="insight-dot ' + ins.type + '"></div><span>' + ins.text + '</span>';
      list.appendChild(li);
    });
  }

  // Sleep analysis
  if (data.sleep) {
    $('sleep-section').style.display = '';
    var sg = $('sleep-stats-grid');
    sg.innerHTML = '';
    var sleepItems = [
      { label: t('deep_sleep'), value: formatHM(data.sleep.deep_avg) },
      { label: t('rem_sleep'), value: formatHM(data.sleep.rem_avg) },
      { label: t('awake_time'), value: formatHM(data.sleep.awake_avg) },
      { label: t('efficiency'), value: data.sleep.efficiency.toFixed(0) + '%', accent: data.sleep.efficiency >= 85 }
    ];
    sleepItems.forEach(function(item) {
      sg.innerHTML += '<div class="sleep-stat"><div class="sleep-stat-label">' + item.label + '</div><div class="sleep-stat-value' + (item.accent ? ' accent' : '') + '">' + item.value + '</div></div>';
    });

    // Sleep source comparison
    var ssEl = $('sleep-sources');
    if (ssEl) {
      if (data.sleep.sources && data.sleep.sources.length > 1) {
        ssEl.style.display = '';
        var srcHtml = '<div class="sleep-sources-header" data-i18n="source_comparison">'+t('source_comparison')+'</div>';
        srcHtml += '<div class="sleep-src-table"><div class="sleep-src-row sleep-src-head">';
        srcHtml += '<span></span><span>'+t('lbl_total')+'</span><span>'+t('lbl_deep')+'</span><span>REM</span><span>'+t('lbl_core')+'</span></div>';
        data.sleep.sources.forEach(function(src, i) {
          var color = SOURCE_PALETTE[i % SOURCE_PALETTE.length];
          srcHtml += '<div class="sleep-src-row">';
          srcHtml += '<span class="sleep-src-name"><span class="sleep-src-dot" style="background:'+color+'"></span>'+src.source+'</span>';
          srcHtml += '<span>'+formatHM(src.total)+'</span>';
          srcHtml += '<span>'+formatHM(src.deep)+'</span>';
          srcHtml += '<span>'+formatHM(src.rem)+'</span>';
          srcHtml += '<span>'+formatHM(src.core)+'</span>';
          srcHtml += '</div>';
        });
        srcHtml += '</div>';
        ssEl.innerHTML = srcHtml;
      } else {
        ssEl.style.display = 'none';
      }
    }
  }

  // Section detail cards
  var cardsEl = $('section-cards');
  cardsEl.innerHTML = '';
  if (data.sections && data.sections.length) {
    data.sections.forEach(function(s) {
      var card = document.createElement('div');
      card.className = 'insight-card status-' + s.status;
      card.dataset.key = s.key;
      card.style.cursor = 'pointer';
      card.onclick = (function(key) { return function() { showSection(key); }; })(s.key);
      var html = '<div class="insight-header"><div class="insight-icon">' + (ICON_MAP[s.icon] || '') + '</div><div class="insight-title-wrap"><div class="insight-title">' + s.title + '</div><div class="insight-badge">' + t('status_' + s.status) + '</div></div><svg class="sec-card-chevron" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="9 18 15 12 9 6"/></svg></div><div class="insight-summary">' + s.summary + '</div>';
      if (s.details && s.details.length) {
        html += '<div class="insight-details">';
        s.details.forEach(function(d) {
          html += '<div class="insight-detail"><span class="detail-indicator ' + (d.trend || 'stable') + '"></span><span class="detail-label">' + d.label + '</span><span class="detail-value">' + d.value + '</span><span class="detail-note">' + (d.note || '') + '</span></div>';
        });
        html += '</div>';
      }
      card.innerHTML = html;
      cardsEl.appendChild(card);
    });
  }

  // Hero sparkline
  renderReadinessSparkline();
}

function renderReadinessSparkline() {
  fetch('/api/readiness-history?days=30')
    .then(function(r){return r.json()})
    .then(function(d) {
      var pts = d.points || [];
      if (pts.length < 3) return;
      $('hero-sparkline-block').style.display = '';
      var labels = pts.map(function(p){return p.date;});
      var vals = pts.map(function(p){return p.score;});
      if (sparklineChart) { sparklineChart.destroy(); sparklineChart = null; }
      var canvas = $('readiness-sparkline');
      sparklineChart = new Chart(canvas, {
        type: 'line',
        data: {
          labels: labels,
          datasets: [{
            data: vals,
            borderColor: 'rgba(255,255,255,0.9)',
            backgroundColor: 'rgba(255,255,255,0.12)',
            fill: true,
            borderWidth: 2,
            pointRadius: 0,
            tension: 0.4
          }]
        },
        options: {
          responsive: true, maintainAspectRatio: false,
          animation: { duration: 600 },
          plugins: {
            legend: { display: false },
            tooltip: {
              backgroundColor: 'rgba(0,0,0,0.7)',
              titleColor: '#fff', bodyColor: '#fff',
              padding: 6,
              callbacks: {
                title: function(items) { return fmtAxisDate(items[0].label); },
                label: function(ctx) { return ' ' + Math.round(ctx.parsed.y) + '%'; }
              }
            }
          },
          scales: {
            x: { display: false },
            y: { display: false, min: 0, max: 100 }
          },
          elements: { point: { radius: 0, hoverRadius: 4 } }
        }
      });
    })
    .catch(function(){});
}

function formatHM(hours) {
  if (!hours) return '0m';
  var h = Math.floor(hours);
  var m = Math.round((hours - h) * 60);
  if (h > 0 && m > 0) return h + 'h ' + m + 'm';
  if (h > 0) return h + 'h';
  return m + 'm';
}

// ---- Correlation chart ----
function renderCorrelationChart(data) {
  if (corrChart) { corrChart.destroy(); corrChart = null; }
  var sorted = data.slice().sort(function(a, b) { return a.date > b.date ? 1 : -1; });
  var localeCode = LANG === 'ru' ? 'ru' : LANG === 'sr' ? 'sr-Latn' : 'en';
  var labels = sorted.map(function(p) {
    var d = new Date(p.date + 'T12:00:00');
    return d.toLocaleDateString(localeCode, { weekday: 'short', month: 'short', day: 'numeric' });
  });
  var loadVals = sorted.map(function(p) { return p.load; });
  var hrvVals = sorted.map(function(p) { return p.hrv; });

  var ctx = $('corr-chart').getContext('2d');
  corrChart = new Chart(ctx, {
    type: 'line',
    data: {
      labels: labels,
      datasets: [
        {
          label: t('activity_load'),
          data: loadVals,
          borderColor: '#059669',
          backgroundColor: 'rgba(5,150,105,0.1)',
          fill: true,
          tension: 0.4,
          borderWidth: 2.5,
          pointRadius: 4,
          pointBackgroundColor: '#059669',
          yAxisID: 'y'
        },
        {
          label: t('metric_heart_rate_variability'),
          data: hrvVals,
          borderColor: '#e11d48',
          backgroundColor: 'rgba(225,29,72,0.08)',
          fill: true,
          tension: 0.4,
          borderWidth: 2.5,
          pointRadius: 4,
          pointBackgroundColor: '#e11d48',
          yAxisID: 'y1'
        }
      ]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: { display: false },
        tooltip: {
          backgroundColor: '#fff', borderColor: '#e7e5e4', borderWidth: 1,
          titleColor: '#78716c', bodyColor: '#1c1917',
          callbacks: {
            label: function(ctx) {
              return ' ' + ctx.dataset.label + ': ' + ctx.parsed.y.toFixed(1);
            }
          }
        }
      },
      scales: {
        x: { ticks: { color: '#78716c', font: { size: 11 } }, grid: { color: '#f0efed' } },
        y: { position: 'left', ticks: { color: '#059669', font: { size: 11 } }, grid: { color: '#f0efed' }, title: { display: true, text: t('load_pct'), color: '#059669', font: { size: 11 } } },
        y1: { position: 'right', ticks: { color: '#e11d48', font: { size: 11 } }, grid: { drawOnChartArea: false }, title: { display: true, text: t('hrv_ms'), color: '#e11d48', font: { size: 11 } } }
      }
    }
  });
}

// ---- Trend charts ----
function loadTrendCharts() {
  var container = $('trend-charts');
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
      wrap.onclick = function() { if (f.virtual) { selectMetric('readiness'); } else { selectMetric(f.metric); } };
      var vals = pts.map(function(p){return p.qty});
      var latestVal = vals[vals.length-1];
      var displayVal = f.virtual ? (Math.round(latestVal) + '%') : fmtVal(f.metric, latestVal);
      wrap.innerHTML = '<div class="trend-card-header"><div class="trend-card-title">' + t(f.labelKey) + '</div><div class="trend-card-value">' + displayVal + '</div></div><div class="trend-card-canvas"><canvas></canvas></div>';
      container.appendChild(wrap);
      var canvas = wrap.querySelector('canvas');
      var labels = pts.map(function(p){return fmtAxisDate(p.date)});
      var c = new Chart(canvas, {
        type: f.type,
        data: { labels: labels, datasets: [{ data: vals, borderColor: f.color, backgroundColor: f.type === 'bar' ? f.color + '55' : f.color + '15', fill: f.type === 'line', borderWidth: f.type === 'line' ? 2 : 1, pointRadius: 0, tension: 0.35, borderRadius: f.type === 'bar' ? 3 : 0 }] },
        options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false }, tooltip: { backgroundColor: '#fff', borderColor: '#e7e5e4', borderWidth: 1, titleColor: '#78716c', bodyColor: '#1c1917', padding: 8, callbacks: { title: function(items) { return fmtAxisDate(items[0].label); }, label: function(ctx) { return ' ' + fmt2(ctx.parsed.y) + (fmtUnit(unitsMap[f.metric] || '') ? ' ' + fmtUnit(unitsMap[f.metric] || '') : ''); } } } }, scales: { x: { display: false }, y: { display: false, beginAtZero: f.type === 'bar' } }, elements: { point: { radius: 0, hoverRadius: 4 } } }
      });
      trendCharts.push(c);
    });
  });
}
`
