/* ==========================================================================
 * RockSys 管理控制台 - util.js 通用工具函数
 * 纯函数：格式化、DOM 快捷、防抖、光标插入等，不依赖其他模块。
 * 挂载到全局命名空间 window.Rock.util。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};

  // DOM 快捷查询
  const $ = (sel, root) => (root || document).querySelector(sel);
  const $$ = (sel, root) => Array.prototype.slice.call((root || document).querySelectorAll(sel));

  // HTML 转义（防止 XSS 与属性注入）
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  // 千分位数字（请求量等整数）
  function fmtInt(v) {
    const n = Math.round(Number(v) || 0);
    const neg = n < 0;
    const s = String(Math.abs(n)).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
    return (neg ? '-' : '') + s;
  }

  // 日期时间解析：兼容时间戳（秒/毫秒）与 RFC3339 字符串
  function toDate(ts) {
    if (ts === null || ts === undefined || ts === '') return null;
    if (ts instanceof Date) return isNaN(ts.getTime()) ? null : ts;
    if (typeof ts === 'number') {
      const d = new Date(ts < 1e12 ? ts * 1000 : ts);
      return isNaN(d.getTime()) ? null : d;
    }
    const d = new Date(ts);
    return isNaN(d.getTime()) ? null : d;
  }

  const pad2 = n => String(n).padStart(2, '0');

  function fmtTime(ts) {
    const d = toDate(ts);
    return d ? pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds()) : '--:--:--';
  }

  function fmtDate(ts) {
    const d = toDate(ts);
    return d ? d.getFullYear() + '-' + pad2(d.getMonth() + 1) + '-' + pad2(d.getDate()) : '';
  }

  function fmtDateTime(ts) {
    return fmtDate(ts) + ' ' + fmtTime(ts);
  }

  // 字节数格式化
  function fmtBytes(b) {
    b = Number(b) || 0;
    if (b < 1024) return b + ' B';
    if (b < 1048576) return (b / 1024).toFixed(1) + ' KB';
    return (b / 1048576).toFixed(2) + ' MB';
  }

  // 截断字符串
  function truncate(s, n) {
    s = String(s || '');
    return s.length > n ? s.slice(0, n) + '…' : s;
  }

  // 防抖
  function debounce(fn, wait) {
    let t = null;
    return function () {
      const args = arguments, self = this;
      clearTimeout(t);
      t = setTimeout(() => fn.apply(self, args), wait);
    };
  }

  // 在光标处插入文本（编辑器 Tab 等）
  function insertAtCursor(input, text) {
    const s = input.selectionStart, e = input.selectionEnd;
    const v = input.value;
    input.value = v.slice(0, s) + text + v.slice(e);
    input.selectionStart = input.selectionEnd = s + text.length;
    input.dispatchEvent(new Event('input'));
    input.focus();
  }

  window.Rock.util = {
    $,
    $$,
    esc,
    fmtInt,
    toDate,
    pad2,
    fmtTime,
    fmtDate,
    fmtDateTime,
    fmtBytes,
    truncate,
    debounce,
    insertAtCursor,
  };
})();
