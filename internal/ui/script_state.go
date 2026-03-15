package ui

const jsState = `
// ---- State ----
var chart = null, corrChart = null, sparklineChart = null, currentMetric = null, currentSection = null, compareEnabled = false, bySourceEnabled = false, lastPoints = [], unitsMap = {}, allMetrics = [], dashboardData = null, briefingData = null;
var sectionCharts = [];
var SOURCE_PALETTE = ['#2563eb','#e11d48','#059669','#d97706','#7c3aed','#06b6d4','#ea580c','#0891b2'];
var trendCharts = [];
var prevView = 'dashboard';
function $(id) { return document.getElementById(id); }
function todayStr() { var d = new Date(); return d.getFullYear() + '-' + String(d.getMonth()+1).padStart(2,'0') + '-' + String(d.getDate()).padStart(2,'0'); }
function daysAgoStr(n) { var d = new Date(); d.setDate(d.getDate()-n); return d.getFullYear() + '-' + String(d.getMonth()+1).padStart(2,'0') + '-' + String(d.getDate()).padStart(2,'0'); }

// ---- Init ----
$('from').value = daysAgoStr(29);
$('to').value = todayStr();
readHash();
applyLang();
init();

function init() {
  Promise.all([
    fetch('/api/health-briefing?lang=' + LANG).then(function(r){return r.json()}).catch(function(){return null}),
    fetch('/api/dashboard').then(function(r){return r.json()}).catch(function(){return null}),
    fetch('/api/metrics').then(function(r){return r.json()}).catch(function(){return []})
  ]).then(function(results) {
    var briefingRes = results[0], dashRes = results[1], metricsRes = results[2];
    if (metricsRes) {
      allMetrics = metricsRes;
      metricsRes.forEach(function(m) { unitsMap[m.Name] = m.Units; });
    }
    briefingData = briefingRes;
    dashboardData = dashRes;
    $('briefing-loading').style.display = 'none';
    $('briefing-content').style.display = '';
    renderBriefing(briefingRes);
    loadTrendCharts();
    if (location.hash) {
      var p = new URLSearchParams(location.hash.slice(1));
      var m = p.get('metric');
      if (m) selectMetric(m);
    }
  });
}
`
