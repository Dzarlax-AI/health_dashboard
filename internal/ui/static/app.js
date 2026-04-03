// app.js — shared utilities for Health Dashboard
'use strict';

function $(id) { return document.getElementById(id); }

function todayStr() {
  var d = new Date();
  return d.getFullYear() + '-' + String(d.getMonth()+1).padStart(2,'0') + '-' + String(d.getDate()).padStart(2,'0');
}

function daysAgoStr(n) {
  var d = new Date();
  d.setDate(d.getDate()-n);
  return d.getFullYear() + '-' + String(d.getMonth()+1).padStart(2,'0') + '-' + String(d.getDate()).padStart(2,'0');
}

function fmt2(v) { return v == null ? '—' : Number(v).toFixed(v < 10 ? 1 : 0); }

function fmtVal(v, unit) {
  if (v == null) return '—';
  if (unit === 'min' || unit === 'minutes') {
    var h = Math.floor(v / 60), m = Math.round(v % 60);
    return h > 0 ? h + 'h ' + m + 'm' : m + 'm';
  }
  if (unit === '%' || unit === 'percent') return Number(v).toFixed(1) + '%';
  if (v >= 10000) return (v/1000).toFixed(1) + 'k';
  if (v >= 100) return Math.round(v).toString();
  return Number(v).toFixed(v < 10 ? 1 : 0);
}

function fmtUnit(u) {
  if (!u) return '';
  var map = { 'count': '', 'min': 'min', 'bpm': 'bpm', 'ms': 'ms', '%': '%',
              'kcal': 'kcal', 'km': 'km', 'mg': 'mg', 'g': 'g', 'mcg': 'mcg',
              'dB': 'dB', 'C': '°C', 'mL/kg/min': 'mL/kg/min', 'm': 'm' };
  return map[u] || u;
}

function fmtAxisDate(ts, includeWeekday) {
  var d = new Date(ts.includes('T') ? ts : ts + 'T12:00:00');
  var now = new Date();
  var opts = { month: 'short', day: 'numeric' };
  if (includeWeekday) opts.weekday = 'short';
  if (d.getFullYear() !== now.getFullYear()) {
    opts.year = 'numeric';
  }
  var lang = document.documentElement.lang || 'en';
  var localeCode = lang === 'ru' ? 'ru' : lang === 'sr' ? 'sr-Latn' : 'en';
  return d.toLocaleDateString(localeCode, opts);
}

// Keyboard shortcuts
document.addEventListener('keydown', function(e) {
  if (e.target.tagName === 'INPUT' || e.target.tagName === 'SELECT' || e.target.tagName === 'TEXTAREA') return;
  if (e.key === '/') {
    e.preventDefault();
    window.location.href = '/metrics';
  }
  if (e.key === 'Escape') {
    history.back();
  }
});

// Language switcher — set cookie and reload
function setLang(lang) {
  document.cookie = 'lang=' + lang + ';path=/;max-age=31536000';
  var url = new URL(window.location);
  url.searchParams.set('lang', lang);
  window.location.href = url.toString();
}
