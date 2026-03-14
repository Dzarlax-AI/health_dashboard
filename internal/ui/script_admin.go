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
  loadAdminSettings();
}

function hideAdminView() {
  $('admin-view').style.display = 'none';
  showDashboard();
}

function loadAdminSettings() {
  fetch('/api/admin/settings')
    .then(function(r) { return r.json(); })
    .then(function(s) {
      renderNotifySettings(s);
      $('admin-loading').style.display = 'none';
      $('admin-body').style.display = 'block';
      checkImportResume();
    })
    .catch(function(e) {
      $('admin-loading').innerHTML = '<div style="color:var(--danger);padding:16px">' + e + '</div>';
    });
}

function loadAdminStatus() {
  var btn = $('btn-refresh-status');
  if (btn) btn.disabled = true;
  fetch('/api/admin/status')
    .then(function(r) { return r.json(); })
    .then(function(d) {
      renderAdminStatus(d);
      if (btn) btn.disabled = false;
    })
    .catch(function(e) {
      $('admin-cache-table').innerHTML = '<div style="color:var(--danger);font-size:13px">' + e + '</div>';
      if (btn) btn.disabled = false;
    });
}

function renderNotifySettings(s) {
  $('cfg-telegram-token').value    = s.telegram_token || '';
  $('cfg-telegram-chat-id').value  = s.telegram_chat_id || '';
  $('cfg-report-lang').value       = s.report_lang || 'en';
  $('cfg-timezone').value          = s.timezone || '';
  $('cfg-morning-weekday').value   = s.report_morning_weekday != null ? s.report_morning_weekday : 8;
  $('cfg-morning-weekend').value   = s.report_morning_weekend != null ? s.report_morning_weekend : 9;
  $('cfg-evening-weekday').value   = s.report_evening_weekday != null ? s.report_evening_weekday : 20;
  $('cfg-evening-weekend').value   = s.report_evening_weekend != null ? s.report_evening_weekend : 21;
}

function saveNotifySettings() {
  var btn = $('btn-settings-save');
  btn.disabled = true;
  var msg = $('admin-notify-msg');
  msg.style.display = 'none';

  var payload = {
    telegram_token:          $('cfg-telegram-token').value.trim(),
    telegram_chat_id:        $('cfg-telegram-chat-id').value.trim(),
    report_lang:             $('cfg-report-lang').value,
    timezone:                $('cfg-timezone').value.trim(),
    report_morning_weekday:  $('cfg-morning-weekday').value,
    report_morning_weekend:  $('cfg-morning-weekend').value,
    report_evening_weekday:  $('cfg-evening-weekday').value,
    report_evening_weekend:  $('cfg-evening-weekend').value,
  };

  fetch('/api/admin/settings', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload)
  })
    .then(function(r) { return r.json(); })
    .then(function(d) {
      msg.textContent = d.status === 'ok' ? t('admin_notify_saved') : (d.message || 'Error');
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

function renderAdminStatus(d) {

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

function checkDataGaps() {
  var btn = $('btn-check-gaps');
  var el = $('admin-gaps-section');
  btn.disabled = true;
  btn.textContent = '…';
  el.style.display = '';
  el.innerHTML = '<div class="admin-gaps-checking">' + t('admin_gaps_checking') + '</div>';
  fetch('/api/admin/gaps')
    .then(function(r) { return r.json(); })
    .then(function(d) { renderDataGaps(d); btn.disabled = false; btn.setAttribute('data-i18n','admin_gaps_check'); btn.textContent = t('admin_gaps_check'); })
    .catch(function(e) { el.innerHTML = '<div style="color:var(--danger);font-size:13px">' + e + '</div>'; btn.disabled = false; });
}

function renderDataGaps(d) {
  var el = $('admin-gaps-section');
  if (!el) return;
  var gaps = d.gaps || [];
  if (!gaps.length) {
    el.innerHTML = '<div class="admin-gaps-ok">&#10003; ' + t('admin_gaps_ok') + '</div>';
    return;
  }
  var html = '<div class="admin-gaps-title"><span class="admin-gaps-icon">&#9888;</span>' + t('admin_gaps_title') + ' (' + gaps.length + ')</div>';
  html += '<div class="admin-gaps-list">';
  gaps.forEach(function(g) {
    var label, rowClass = '';
    if (g.today_missing) {
      label = '<span class="admin-gap-today">' + t('admin_gaps_today') + '</span>';
      rowClass = ' admin-gap-row-today';
    } else if (g.partial) {
      label = g.from + ' <span class="admin-gap-partial">' + t('admin_gaps_partial') + '</span>';
    } else {
      label = g.from + (g.from !== g.to ? ' &rarr; ' + g.to : '') + ' <span class="admin-gap-days">' + g.days + ' ' + t('admin_gaps_days') + '</span>';
    }
    html += '<div class="admin-gap-row' + rowClass + '">'
      + '<span class="admin-gap-range">' + label + '</span>'
      + (g.today_missing ? '' : '<button class="admin-gap-btn" onclick="startImportForGap(\'' + g.from + '\',\'' + g.to + '\')">' + t('admin_gaps_fill') + '</button>')
      + '</div>';
  });
  html += '</div>';
  el.innerHTML = html;
}

function startImportForGap(from, to) {
  // Scroll to the import section and show a hint about the gap.
  var imp = $('admin-import-section');
  if (imp) {
    imp.scrollIntoView({ behavior: 'smooth' });
    var hint = $('import-gap-hint');
    if (hint) {
      hint.textContent = t('admin_gaps_hint') + ' ' + from + ' → ' + to;
      hint.style.display = 'block';
    }
  }
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
