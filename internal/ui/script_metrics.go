package ui

const jsMetrics = `
// ---- Metrics view ----
var latestMetricValues = null; // cache

function showMetricsView() {
  $('dashboard-view').style.display = 'none';
  $('chart-view').style.display = 'none';
  $('section-view').style.display = 'none';
  $('metrics-view').style.display = 'block';
  window.scrollTo(0, 0);
  setTimeout(function() { var inp = $('metrics-search'); if (inp) inp.focus(); }, 80);
  if (latestMetricValues) {
    renderMetricsView();
  } else {
    var content = $('metrics-content');
    content.innerHTML = '<div class="metrics-empty">\u2026</div>';
    fetch('/api/metrics/latest')
      .then(function(r) { return r.json(); })
      .then(function(data) {
        latestMetricValues = {};
        (data || []).forEach(function(v) { latestMetricValues[v.metric] = v; });
        renderMetricsView();
      })
      .catch(function() { renderMetricsView(); });
  }
}

function hideMetricsView() {
  $('metrics-view').style.display = 'none';
  $('dashboard-view').style.display = 'block';
  window.scrollTo(0, 0);
}

function goBack() {
  currentMetric = null;
  compareEnabled = false;
  $('chart-view').style.display = 'none';
  if (prevView === 'metrics') {
    $('metrics-view').style.display = 'block';
  } else {
    $('dashboard-view').style.display = 'block';
    window.scrollTo(0, 0);
  }
}

function renderMetricsView() {
  var content = $('metrics-content');
  content.innerHTML = '';

  var q = ($('metrics-search').value || '').toLowerCase().trim();
  var colorMap = {
    heart: 'var(--heart)', activity: 'var(--activity)',
    mobility: '#f59e0b', sleep: 'var(--sleep)',
    body: '#10b981', env: '#06b6d4', nutrition: '#f97316', other: 'var(--muted)'
  };
  var catOrder = ['heart', 'activity', 'mobility', 'sleep', 'body', 'env', 'nutrition', 'other'];

  var valueMap = latestMetricValues || {};

  var known = CATEGORIES.reduce(function(a, c) { return a.concat(c.metrics); }, []);
  var extra = allMetrics.map(function(m) { return m.Name; })
                        .filter(function(n) { return !known.includes(n); });
  var all = known.concat(extra);

  var filtered = q ? all.filter(function(m) {
    return name(m).toLowerCase().includes(q) || m.toLowerCase().includes(q);
  }) : all;

  if (!filtered.length) {
    content.innerHTML = '<div class="metrics-empty">' + t('no_metrics_found') + '</div>';
    return;
  }

  var grouped = {};
  filtered.forEach(function(m) {
    var cat = catOf(m);
    var key = cat ? cat.cat : 'other';
    if (!grouped[key]) grouped[key] = [];
    grouped[key].push(m);
  });

  catOrder.forEach(function(key) {
    if (!grouped[key] || !grouped[key].length) return;

    var section = document.createElement('div');
    section.className = 'metrics-cat-section';
    section.innerHTML =
      '<div class="metrics-cat-label">' +
        '<span class="metrics-cat-dot" style="background:' + (colorMap[key] || 'var(--muted)') + '"></span>' +
        getCatLabel(key) +
      '</div>';

    var grid = document.createElement('div');
    grid.className = 'metrics-grid';

    grouped[key].forEach(function(m) {
      var card = valueMap[m];
      var div = document.createElement('div');
      div.className = 'metrics-card';

      var bottomHtml = '<span class="metrics-card-empty">\u2014</span>';
      if (card && card.value != null) {
        var unit = fmtUnit(card.unit || unitsMap[m] || '');
        bottomHtml =
          '<span class="metrics-card-value">' + fmtVal(m, card.value) + '</span>' +
          (unit ? '<span class="metrics-card-unit">' + unit + '</span>' : '');
      }

      div.innerHTML =
        '<div class="metrics-card-name">' + name(m) + '</div>' +
        '<div class="metrics-card-bottom">' + bottomHtml + '</div>';
      div.onclick = function() { selectMetric(m, 'metrics'); };
      grid.appendChild(div);
    });

    section.appendChild(grid);
    content.appendChild(section);
  });
}
`
