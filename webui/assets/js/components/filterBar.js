/* ==========================================================================
 * RockSys 管理控制台 - components/filterBar.js 筛选栏组件
 * 数据列表页的业务无关筛选栏：字段声明 → HTML + 收集 + 重置 + 即改即查（防抖）。
 * 不感知任何 API 与业务语义，查询/重置按钮留在视图（data-act 回调）。
 *   Rock.comp.filterBar.create({ ns, live, onQuery, fields })
 *     - ns：id/act 前缀，防多实例冲突；
 *     - live：true 即改即查（内置防抖）；dateRange 仅完整区间（四输入齐）才触发；
 *     - onQuery(state)：live 触发与 reset 时的查询回调（视图前置校验在回调内做）；
 *     - fields：字段声明，四类：
 *         { type: 'dateRange', key: 'from' }              → 状态键 fromDate/fromTime/toDate/toTime
 *         { type: 'select', key, options: [[v,label]] }    → options 复用 Rock.comp.select.options
 *         { type: 'text', key, placeholder?, width? }
 *         { type: 'check', key, label }
 * 实例接口：html() / bind(host) / collect() / state() / reset()。
 * 挂载到全局命名空间 window.Rock.comp.filterBar。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const esc = Rock.util.esc;

  // dateRange 的四个输入键（key 为前缀：from → fromDate/fromTime/toDate/toTime）
  function drKeys(key) { return [key + 'fromDate', key + 'fromTime', key + 'toDate', key + 'toTime']; }

  function create(opts) {
    const ns = opts.ns;
    const live = !!opts.live;
    const onQuery = opts.onQuery || function () {};
    const fields = opts.fields || [];
    const debounce = Rock.util.debounce;

    // 内部状态：初始值取字段 default；视图可在渲染前经 state() 预置
    const state = {};
    fields.forEach(function (f) {
      if (f.type === 'dateRange') {
        drKeys('').forEach(function (k) { state[f.key + k] = (f.default && f.default[k]) || ''; });
      } else {
        state[f.key] = f.default != null ? f.default : '';
      }
    });

    // ── HTML ──────────────────────────────────────────────────────────
    function fieldHTML(f) {
      if (f.type === 'dateRange') {
        const v = function (k) { return esc(state[f.key + k] || ''); };
        return '<div class="tool-group"><span class="muted">开始</span>' +
          '<input type="date" class="input input-sm" data-fb="' + f.key + 'fromDate" value="' + v('fromDate') + '">' +
          '<input type="time" class="input input-sm" data-fb="' + f.key + 'fromTime" value="' + v('fromTime') + '">' +
          '</div>' +
          '<div class="tool-group"><span class="muted">结束</span>' +
          '<input type="date" class="input input-sm" data-fb="' + f.key + 'toDate" value="' + v('toDate') + '">' +
          '<input type="time" class="input input-sm" data-fb="' + f.key + 'toTime" value="' + v('toTime') + '">' +
          '</div>';
      }
      if (f.type === 'select') {
        return '<select class="select select-sm" data-fb="' + f.key + '">' +
          Rock.comp.select.options(f.options, state[f.key]) + '</select>';
      }
      if (f.type === 'check') {
        return '<label class="chk"><input type="checkbox" data-fb="' + f.key + '"' +
          (state[f.key] ? ' checked' : '') + '> ' + esc(f.label || '') + '</label>';
      }
      // text（缺省）
      return '<input class="input input-sm" data-fb="' + f.key + '" placeholder="' + esc(f.placeholder || '') + '"' +
        (f.width ? ' style="width:' + esc(f.width) + '"' : '') +
        ' value="' + esc(state[f.key] || '') + '">';
    }

    function html() {
      return '<div class="log-toolbar" id="' + esc(ns) + '-filterbar">' +
        fields.map(fieldHTML).join('') + '</div>';
    }

    // ── 收集 / 状态 ───────────────────────────────────────────────────
    // 从 DOM 读回全部输入到内部状态并返回
    function collect() {
      const host = document.getElementById(ns + '-filterbar');
      if (!host) return state;
      host.querySelectorAll('[data-fb]').forEach(function (el) {
        const k = el.getAttribute('data-fb');
        state[k] = el.type === 'checkbox' ? !!el.checked : (el.value || '');
      });
      return state;
    }

    function stateObj() { return state; }

    // 重置回字段默认值，同步 DOM，并触发查询
    function reset() {
      fields.forEach(function (f) {
        if (f.type === 'dateRange') {
          drKeys(f.key).forEach(function (k) { state[k] = (f.default && f.default[k]) || ''; });
        } else {
          state[f.key] = f.default != null ? f.default : '';
        }
      });
      const host = document.getElementById(ns + '-filterbar');
      if (host) {
        host.querySelectorAll('[data-fb]').forEach(function (el) {
          const k = el.getAttribute('data-fb');
          if (el.type === 'checkbox') el.checked = !!state[k];
          else el.value = state[k] || '';
        });
      }
      onQuery(state);
    }

    // ── 即改即查绑定（live 模式）───────────────────────────────────────
    function bind(host) {
      if (!host || !live) return;
      const bar = host.querySelector('#' + ns + '-filterbar') || document.getElementById(ns + '-filterbar');
      if (!bar) return;
      const trigger = debounce(function () { onQuery(collect()); }, 300);
      bar.addEventListener('change', function (e) {
        const el = e.target.closest('[data-fb]');
        if (!el) return;
        if (el.type === 'date' || el.type === 'time') {
          // dateRange：四输入齐才触发，避免半区间闪现错误结果
          triggerIfRangeComplete();
        } else {
          trigger();
        }
      });
      bar.addEventListener('input', function (e) {
        const el = e.target.closest('[data-fb]');
        if (!el || el.type === 'date' || el.type === 'time') return;
        if (el.tagName === 'INPUT' && el.type === 'text') trigger();
      });
    }

    // dateRange 完整区间（全部 date/time 输入非空）才触发查询；半区间仅收集不查询
    function triggerIfRangeComplete() {
      const host = document.getElementById(ns + '-filterbar');
      if (!host) return;
      let complete = true;
      host.querySelectorAll('[data-fb]').forEach(function (el) {
        if (el.type !== 'date' && el.type !== 'time') return;
        if (!el.value) complete = false;
      });
      if (complete) trigger();
      else collect();
    }

    return { html, bind, collect, state: stateObj, reset };
  }

  window.Rock.comp.filterBar = { create };
})();
