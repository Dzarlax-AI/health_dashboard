package ui

const jsAdmin = `
// ---- Admin / Settings view ----

function showAdminView() {
  $('dashboard-view').style.display = 'none';
  $('metrics-view').style.display = 'none';
  $('section-view').style.display = 'none';
  $('chart-view').style.display = 'none';
  $('admin-view').style.display = 'block';
  window.scrollTo(0, 0);
  loadAdminStatus();
}

function hideAdminView() {
  $('admin-view').style.display = 'none';
  showDashboard();
}

function loadAdminStatus() {
  $('admin-loading').style.display = 'flex';
  $('admin-body').style.display = 'none';
  fetch('/api/admin/status')
    .then(function(r) { return r.json(); })
    .then(function(d) {
      renderAdminStatus(d);
      $('admin-loading').style.display = 'none';
      $('admin-body').style.display = 'block';
    })
    .catch(function(e) {
      $('admin-loading').innerHTML = '<div style="color:var(--danger);padding:16px">' + e + '</div>';
    });
}

function renderAdminStatus(d) {
  // Show Telegram section only when configured server-side.
  if (d.telegram_enabled) {
    $('admin-notify-section').style.display = 'block';
  }

  var rows = [
    { label: t('admin_raw'),    stat: d.raw_points,  icon: '&#128190;' },
    { label: t('admin_minute'), stat: d.minute_cache, icon: '&#9201;' },
    { label: t('admin_hourly'), stat: d.hourly_cache, icon: '&#128336;' },
    { label: t('admin_daily'),  stat: d.daily_scores, icon: '&#128197;' },
  ];

  var html = '<div class="admin-stat-grid">';
  rows.forEach(function(r) {
    var s = r.stat || {};
    var rows_n = (s.rows || 0).toLocaleString();
    var range = (s.oldest && s.newest)
      ? s.oldest.slice(0,10) + ' &rarr; ' + s.newest.slice(0,10)
      : t('admin_empty');
    var metrics = s.metrics ? ' &middot; ' + s.metrics + ' ' + t('admin_metrics') : '';
    html += '<div class="admin-stat-card">'
      + '<div class="admin-stat-icon">' + r.icon + '</div>'
      + '<div class="admin-stat-info">'
      + '<div class="admin-stat-label">' + r.label + '</div>'
      + '<div class="admin-stat-rows">' + rows_n + ' rows' + metrics + '</div>'
      + '<div class="admin-stat-range">' + range + '</div>'
      + '</div>'
      + '</div>';
  });
  html += '</div>';

  html += '<div class="admin-meta-row">'
    + '<span>' + t('admin_score_version') + ': <strong>v' + (d.score_version || 1) + '</strong></span>'
    + (d.last_sync ? '<span>' + t('admin_last_sync') + ': <strong>' + fmtSyncTime(d.last_sync) + '</strong></span>' : '')
    + '</div>';

  $('admin-cache-table').innerHTML = html;
}

function fmtSyncTime(ts) {
  if (!ts) return '—';
  var d = new Date(ts.replace(' ', 'T'));
  if (isNaN(d)) return ts;
  return d.toLocaleString();
}

function triggerBackfill(force) {
  var btn = $(force ? 'btn-backfill-force' : 'btn-backfill');
  btn.disabled = true;
  var msg = $('admin-msg');
  msg.style.display = 'none';

  fetch('/api/admin/backfill' + (force ? '?force=1' : ''), { method: 'POST' })
    .then(function(r) { return r.json(); })
    .then(function(d) {
      msg.textContent = d.message || 'Done';
      msg.className = 'admin-msg-ok';
      msg.style.display = 'block';
      btn.disabled = false;
      // Refresh stats after a short delay to show updated row counts.
      setTimeout(loadAdminStatus, 3000);
    })
    .catch(function(e) {
      msg.textContent = String(e);
      msg.className = 'admin-msg-err';
      msg.style.display = 'block';
      btn.disabled = false;
    });
}

function triggerTestNotify(kind) {
  var btnId = kind === 'morning' ? 'btn-notify-morning' : 'btn-notify-evening';
  var btn = $(btnId);
  btn.disabled = true;
  var msg = $('admin-notify-msg');
  msg.style.display = 'none';

  fetch('/api/admin/test-notify?kind=' + kind, { method: 'POST' })
    .then(function(r) { return r.json(); })
    .then(function(d) {
      msg.textContent = d.message || 'Sent';
      msg.className = d.status === 'ok' ? 'admin-msg-ok' : 'admin-msg-err';
      msg.style.display = 'block';
      btn.disabled = false;
    })
    .catch(function(e) {
      msg.textContent = String(e);
      msg.className = 'admin-msg-err';
      msg.style.display = 'block';
      btn.disabled = false;
    });
}
`
