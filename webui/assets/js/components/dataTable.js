/* ==========================================================================
 * RockSys 管理控制台 - components/dataTable.js 通用数据表格组件
 * 有状态实例 create()：列声明 + 行渲染 + 客户端/服务端分页 + 行点击详情钩子，
 * 配置与事件接线规范见 docs/WEBUI_DATATABLE_PLAN.md 4.3/4.4。
 * XSS 约定：组件渲染的值默认经 Rock.util.esc；列 render / rowActions 回调返回 HTML
 * 属视图显式信任边界，review 只盯 render 回调。
 * 依赖 Rock.util.esc / Rock.util.fmtInt / Rock.comp.detailModal。挂载 window.Rock.comp.dataTable。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const esc = Rock.util.esc;
  const fmtInt = Rock.util.fmtInt;

  // ── 有状态实例（新版）───────────────────────────────────────────────
  // cfg：{ ns, columns, rowKey?, rowActions?, detail?, paging?, emptyText?, rowClass?, onPaging? }
  // 分页交互接线说明：全局事件委托只覆盖 click，select/input 的 change 无委托通道，
  // 故翻页按钮/跳页输入/每页条数由实例 bind(host) 内部委托并回调 cfg.onPaging(state)，
  // 视图只需提供 onPaging 重新拉数/渲染；行详情仍走 data-act="<ns>-detail"（click 委托）。
  function create(cfg) {
    const ns = cfg.ns;
    const pagingCfg = cfg.paging || {};
    const mode = pagingCfg.mode || 'client';
    const sizeOptions = pagingCfg.pageSizeOptions || [20, 50, 100];
    let page = 1;
    let pageSize = pagingCfg.pageSize || 20;
    let total = 0; // server 模式由 html(opts.total) 喂入；client 模式 = rows.length

    function pages() { return Math.max(1, Math.ceil(total / pageSize)); }

    // 状态查询：server 模式视图拼 limit/offset 用
    function state() {
      return { page: page, pageSize: pageSize, offset: (page - 1) * pageSize };
    }

    function go(n) {
      n = Math.floor(Number(n) || 0);
      page = Math.min(Math.max(1, n), pages());
    }

    function setPageSize(n) {
      pageSize = Number(n) || 20;
      page = 1;
    }

    // ── 渲染（纯函数，分页状态内聚）────────────────────────────────────
    function cellHTML(col, row) {
      if (typeof col.render === 'function') return col.render(row); // 显式信任边界
      const v = row[col.key];
      return esc(v == null ? '' : String(v));
    }

    function rowHTML(row) {
      const cls = 'dt-row' + (cfg.rowClass ? ' ' + cfg.rowClass(row) : '');
      let tr;
      if (cfg.detail) {
        tr = '<tr class="' + cls + ' dt-row-clickable" data-act="' + esc(ns) + '-detail" data-key="' +
          esc(cfg.rowKey ? String(cfg.rowKey(row)) : '') + '">';
      } else {
        tr = '<tr class="' + cls + '">';
      }
      tr += cfg.columns.map(function (col) {
        return '<td' + (col.cls ? ' class="' + esc(col.cls) + '"' : '') +
          (col.width ? ' style="width:' + esc(col.width) + '"' : '') + '>' + cellHTML(col, row) + '</td>';
      }).join('');
      if (cfg.rowActions) tr += '<td class="dt-row-actions">' + cfg.rowActions(row) + '</td>';
      return tr + '</tr>';
    }

    function pagingHTML() {
      const left = '<div class="dt-paging-left">共 ' + fmtInt(total) + ' 条 · 每页 ' +
        '<select class="select select-sm" data-dt-size>' +
        sizeOptions.map(function (n) {
          return '<option value="' + n + '"' + (n === pageSize ? ' selected' : '') + '>' + n + '</option>';
        }).join('') +
        '</select></div>';
      if (pages() <= 1) return '<div class="dt-paging">' + left + '</div>';
      const cur = Math.min(page, pages());
      return '<div class="dt-paging">' + left +
        '<div class="dt-paging-right">' +
        '<button class="btn btn-sm" data-dt-page="' + (cur - 1) + '"' + (cur <= 1 ? ' disabled' : '') + '>‹ 上一页</button>' +
        '<span class="muted">第</span>' +
        '<input type="number" class="input input-sm dt-page-input" data-dt-page-input min="1" max="' + pages() + '" value="' + cur + '">' +
        '<span class="muted">/ ' + pages() + ' 页</span>' +
        '<button class="btn btn-sm" data-dt-page="' + (cur + 1) + '"' + (cur >= pages() ? ' disabled' : '') + '>下一页 ›</button>' +
        '</div></div>';
    }

    // rows：当前页数据（client 模式传全量，组件内切片）；opts.total 仅 server 模式；
    // opts.cap / opts.capText：结果触顶提示（如拦截明细 10000 / 访问日志 2000）
    function html(rows, opts) {
      rows = rows || [];
      opts = opts || {};
      if (mode === 'server') total = Number(opts.total) || 0;
      else total = rows.length;
      if (page > pages()) page = pages();

      let body;
      if (!rows.length) {
        body = '<div class="empty">' + esc(cfg.emptyText || '暂无数据') + '</div>';
      } else {
        const shown = mode === 'client' ? rows.slice((page - 1) * pageSize, page * pageSize) : rows;
        const head = cfg.columns.map(function (col) {
          return '<th' + (col.width ? ' style="width:' + esc(col.width) + '"' : '') + '>' + esc(col.label) + '</th>';
        }).join('') + (cfg.rowActions ? '<th style="width:' + esc(cfg.rowActionsWidth || '80px') + '">操作</th>' : '');
        body = '<div class="table-wrap"' + (opts.maxHeight ? ' style="max-height:' + esc(opts.maxHeight) + '"' : '') + '>' +
          '<table class="table"><thead><tr>' + head + '</tr></thead><tbody>' +
          shown.map(rowHTML).join('') + '</tbody></table></div>';
        if (opts.cap && rows.length >= opts.cap) {
          body += '<div class="form-hint" style="margin-top:8px">' +
            esc(opts.capText || '已达单次查询上限，请收窄时间范围或筛选条件') + '</div>';
        }
      }
      return body + pagingHTML();
    }

    // 行详情：视图可注入 table.onDetail = fn；缺省打开 detailModal（detail.fields 必填）
    let onDetail = null;
    if (cfg.detail) {
      onDetail = function (row) {
        const d = cfg.detail;
        Rock.comp.detailModal.show({
          title: typeof d.title === 'function' ? d.title(row) : (d.title || ns),
          fields: d.fields,
          row: row,
          width: d.width,
        });
      };
    }

    // 分页控件事件绑定（host 上委托；host 重渲染 innerHTML 不影响委托；host 须为持久元素，勿重复 bind）
    function bind(host) {
      if (!host) return;
      host.addEventListener('click', function (e) {
        const btn = e.target.closest('[data-dt-page]');
        if (!btn || btn.disabled) return;
        go(btn.getAttribute('data-dt-page'));
        if (cfg.onPaging) cfg.onPaging(state());
      });
      host.addEventListener('change', function (e) {
        const sizeSel = e.target.closest('[data-dt-size]');
        if (sizeSel) {
          setPageSize(sizeSel.value);
          if (cfg.onPaging) cfg.onPaging(state());
          return;
        }
        const input = e.target.closest('[data-dt-page-input]');
        if (input) {
          go(input.value);
          if (cfg.onPaging) cfg.onPaging(state());
        }
      });
      host.addEventListener('keydown', function (e) {
        const input = e.target.closest('[data-dt-page-input]');
        if (input && e.key === 'Enter') {
          go(input.value);
          if (cfg.onPaging) cfg.onPaging(state());
        }
      });
    }

    const inst = { html, bind, state, go, setPageSize };
    Object.defineProperty(inst, 'onDetail', {
      get: function () { return onDetail; },
      set: function (fn) { onDetail = fn; },
    });
    return inst;
  }

  window.Rock.comp.dataTable = { create };
})();
