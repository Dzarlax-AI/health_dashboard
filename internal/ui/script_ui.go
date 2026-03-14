package ui

const jsUI = `
// ---- Formatters ----
function fmtVal(metric, v) {
  if (metric === 'walking_running_distance') return v.toFixed(2);
  if (SLEEP_METRICS.has(metric)) return v.toFixed(1);
  if (v >= 1000) return Math.round(v).toLocaleString();
  if (v % 1 === 0) return v;
  return v.toFixed(1);
}
function fmtUnit(u) {
  var map = {'count/min':'bpm','count':'','kcal':'kcal','km':'km','%':'%','ms':'ms','min':'min','hr':'h','degC':'C','dBASPL':'dB','ml/(kg*min)':'ml/kg/min','m':'m','m/s':'m/s'};
  return map[u] !== undefined ? map[u] : (u || '');
}
function fmt2(v) {
  if (v == null || isNaN(v)) return '\u2014';
  if (v >= 1000) return Math.round(v).toLocaleString();
  return v % 1 === 0 ? String(v) : v.toFixed(1);
}
function fmtAxisDate(label) {
  if (!label) return '';
  var dateStr = label.slice(0,10);
  var timeStr = label.length > 10 ? label.slice(11,16) : '';
  var d = new Date(dateStr + 'T12:00:00');
  var localeCode = LANG === 'ru' ? 'ru' : LANG === 'sr' ? 'sr-Latn' : 'en';
  var weekday = d.toLocaleDateString(localeCode, { weekday:'short' });
  var opts = { month:'short', day:'numeric' };
  if (_fmtShowYear) opts.year = 'numeric';
  var md = d.toLocaleDateString(localeCode, opts);
  if (!timeStr || timeStr === '00:00') return weekday + ' ' + md;
  return md + ' ' + timeStr;
}
var _fmtShowYear = false;
function updateShowYear() {
  var from = $('from'), to = $('to');
  if (!from || !to || !from.value || !to.value) { _fmtShowYear = false; return; }
  _fmtShowYear = from.value.slice(0,4) !== to.value.slice(0,4);
}
function chip(label, value, unit) {
  return '<div class="stat-chip"><div class="s-label">' + label + '</div><div class="s-value">' + value + (unit ? ' <span style="font-size:12px;color:var(--muted)">' + unit + '</span>' : '') + '</div></div>';
}

// ---- Navigation ----
function selectMetric(metric, from) {
  prevView = from || 'dashboard';
  currentMetric = metric;
  compareEnabled = false;
  bySourceEnabled = false;
  $('chart-metric-name').textContent = name(metric);
  $('compare-btn').classList.remove('active');
  $('by-source-btn').classList.remove('active');
  $('compare-btn').style.display = (SLEEP_METRICS.has(metric) || metric === 'readiness') ? 'none' : '';
  $('by-source-btn').style.display = metric === 'readiness' ? 'none' : '';
  $('bucket').value = '';
  $('agg').value = '';
  $('from').value = daysAgoStr(29);
  $('to').value = todayStr();
  document.querySelectorAll('.preset-btn').forEach(function(b, i) { b.classList.toggle('active', i === 2); });
  $('dashboard-view').style.display = 'none';
  $('metrics-view').style.display = 'none';
  $('section-view').style.display = 'none';
  $('chart-view').style.display = 'block';
  window.scrollTo(0, 0);
  loadChart();
}
function showDashboard() {
  currentMetric = null;
  compareEnabled = false;
  prevView = 'dashboard';
  $('section-view').style.display = 'none';
  $('metrics-view').style.display = 'none';
  $('dashboard-view').style.display = 'block';
  $('chart-view').style.display = 'none';
  history.replaceState(null, '', location.pathname);
  window.scrollTo(0, 0);
}
function pushHash() {
  if (!currentMetric) { history.replaceState(null,'', location.pathname); return; }
  var p = new URLSearchParams({ metric: currentMetric, from: $('from').value, to: $('to').value });
  var b = $('bucket').value, a = $('agg').value;
  if (b) p.set('bucket', b);
  if (a) p.set('agg', a);
  history.replaceState(null,'', '#' + p.toString());
}
function readHash() {
  if (!location.hash) return;
  var p = new URLSearchParams(location.hash.slice(1));
  if (p.get('from')) $('from').value = p.get('from');
  if (p.get('to'))   $('to').value   = p.get('to');
  if (p.get('bucket')) $('bucket').value = p.get('bucket');
  if (p.get('agg'))    $('agg').value    = p.get('agg');
}

// ---- Controls ----
function applyPreset(days) {
  document.querySelectorAll('.preset-btn').forEach(function(b) { b.classList.remove('active'); });
  if (event && event.target) event.target.classList.add('active');
  $('from').value = daysAgoStr(days - 1);
  $('to').value = todayStr();
  $('bucket').value = '';
  loadChart();
}
function applyPresetAll() {
  document.querySelectorAll('.preset-btn').forEach(function(b) { b.classList.remove('active'); });
  if (event && event.target) event.target.classList.add('active');
  if (!currentMetric) return;
  fetch('/api/metrics/range?metric=' + encodeURIComponent(currentMetric))
    .then(function(r) { return r.json(); })
    .then(function(d) {
      if (d.min) $('from').value = d.min;
      $('to').value = todayStr();
      $('bucket').value = '';
      loadChart();
    });
}
function onDateChange() {
  document.querySelectorAll('.preset-btn').forEach(function(b) { b.classList.remove('active'); });
  loadChart();
}
function shiftRange(dir) {
  var from = new Date($('from').value + 'T12:00:00');
  var to   = new Date($('to').value + 'T12:00:00');
  var days = Math.round((to - from) / 86400000) + 1;
  from.setDate(from.getDate() + dir * days);
  to.setDate(to.getDate() + dir * days);
  $('from').value = from.toISOString().slice(0,10);
  $('to').value = to.toISOString().slice(0,10);
  document.querySelectorAll('.preset-btn').forEach(function(b) { b.classList.remove('active'); });
  loadChart();
}
function toggleCompare() {
  compareEnabled = !compareEnabled;
  $('compare-btn').classList.toggle('active', compareEnabled);
  loadChart();
}
function toggleBySource() {
  bySourceEnabled = !bySourceEnabled;
  $('by-source-btn').classList.toggle('active', bySourceEnabled);
  loadChart();
}
function downloadCSV() {
  if (!lastPoints.length) return;
  var unit = fmtUnit(unitsMap[currentMetric] || '');
  var rows = [['date', unit || 'value']];
  lastPoints.forEach(function(p) { rows.push([p.date, p.qty]); });
  var csv = rows.map(function(r) { return r.join(','); }).join('\n');
  var a = document.createElement('a');
  a.href = 'data:text/csv;charset=utf-8,' + encodeURIComponent(csv);
  a.download = currentMetric + '_' + $('from').value + '_' + $('to').value + '.csv';
  a.click();
}

// ---- Keyboard ----
document.addEventListener('keydown', function(e) {
  var tag = document.activeElement.tagName;
  if (tag === 'INPUT' || tag === 'SELECT' || tag === 'TEXTAREA') {
    if (e.key === 'Escape') { document.activeElement.blur(); return; }
    return;
  }
  switch(e.key) {
    case '/':
      e.preventDefault();
      showMetricsView();
      break;
    case 'Escape':
      if ($('chart-view').style.display !== 'none') { goBack(); }
      else if (currentSection) { hideSectionView(); }
      else if ($('metrics-view').style.display !== 'none') { hideMetricsView(); }
      else { showDashboard(); }
      break;
    case 'ArrowLeft':  if (currentMetric) { e.preventDefault(); shiftRange(-1); } break;
    case 'ArrowRight': if (currentMetric) { e.preventDefault(); shiftRange(1); } break;
    case '1': case '2': case '3': case '4': case '5':
      if (currentMetric) {
        var days = [1,7,30,90,365][+e.key-1];
        document.querySelectorAll('.preset-btn').forEach(function(b,i) { b.classList.toggle('active', i===+e.key-1); });
        $('from').value = daysAgoStr(days - 1);
        $('to').value = todayStr();
        $('bucket').value = '';
        loadChart();
      }
      break;
  }
});
`
