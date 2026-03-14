package ui

const jsImport = `
// ---- Apple Health Import ----

var _importFile = null;
var _importPollTimer = null;

function importFileChosen(input) {
  if (!input.files || !input.files[0]) return;
  _importFile = input.files[0];
  var label = $('import-file-label');
  label.textContent = _importFile.name;
  $('btn-import-start').disabled = false;
}

function startImport() {
  if (!_importFile) return;
  var btn = $('btn-import-start');
  btn.disabled = true;

  var batch = $('import-batch').value || 500;
  var pause = $('import-pause').value || 150;
  var url = '/api/admin/import/upload?batch=' + batch + '&pause=' + pause
    + '&filename=' + encodeURIComponent(_importFile.name)
    + '&size=' + _importFile.size;

  $('import-progress').style.display = 'block';
  $('import-bar').style.width = '2%';
  $('import-status-text').textContent = t('admin_import_uploading');

  fetch(url, {
    method: 'POST',
    body: _importFile,
  })
    .then(function(r) { return r.json(); })
    .then(function(d) {
      if (d.status !== 'ok') {
        $('import-status-text').textContent = d.message || 'Error';
        btn.disabled = false;
        return;
      }
      $('import-status-text').textContent = t('admin_import_running');
      _pollImportStatus();
    })
    .catch(function(e) {
      $('import-status-text').textContent = String(e);
      btn.disabled = false;
    });
}

function _pollImportStatus() {
  if (_importPollTimer) clearTimeout(_importPollTimer);
  fetch('/api/admin/import/status')
    .then(function(r) { return r.json(); })
    .then(function(s) {
      _renderImportStatus(s);
      if (s.running) {
        _importPollTimer = setTimeout(_pollImportStatus, 2000);
      } else if (s.done) {
        $('btn-import-start').disabled = false;
        // Refresh cache stats
        setTimeout(loadAdminStatus, 1500);
      }
    })
    .catch(function() {
      _importPollTimer = setTimeout(_pollImportStatus, 5000);
    });
}

function _renderImportStatus(s) {
  if (!s.running && !s.done) return;
  var bar = $('import-bar');
  var txt = $('import-status-text');

  if (s.done) {
    bar.style.width = '100%';
    if (s.error) {
      txt.textContent = t('admin_import_error') + ': ' + s.error;
    } else {
      txt.textContent = t('admin_import_done')
        .replace('{inserted}', (s.inserted || 0).toLocaleString())
        .replace('{skipped}', (s.skipped || 0).toLocaleString())
        .replace('{sec}', s.elapsed_sec || 0);
    }
    return;
  }

  // Честный прогресс: bytes_read / total_bytes (распакованный XML внутри zip)
  var pct = 0;
  if (s.total_bytes > 0 && s.bytes_read > 0) {
    pct = Math.min(Math.round(s.bytes_read / s.total_bytes * 100), 99);
  }
  bar.style.width = Math.max(pct, 2) + '%';

  var mbRead = ((s.bytes_read || 0) / 1048576).toFixed(0);
  var mbTotal = ((s.total_bytes || 0) / 1048576).toFixed(0);
  var pctStr = s.total_bytes > 0 ? ' (' + pct + '%)' : '';
  txt.textContent = t('admin_import_progress')
    .replace('{parsed}', (s.parsed || 0).toLocaleString())
    .replace('{inserted}', (s.inserted || 0).toLocaleString())
    .replace('{mb}', mbRead + ' / ' + mbTotal + ' MB' + pctStr)
    .replace('{sec}', s.elapsed_sec || 0);
}

// Resume poll on page load if import is already running
function checkImportResume() {
  fetch('/api/admin/import/status')
    .then(function(r) { return r.json(); })
    .then(function(s) {
      if (s.running || s.done) {
        $('import-progress').style.display = 'block';
        _renderImportStatus(s);
        if (s.running) _pollImportStatus();
      }
    })
    .catch(function() {});
}
`
