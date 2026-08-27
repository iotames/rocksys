/* ==========================================================================
 * RockSys 管理控制台 - components/dataTable.js 通用数据表格组件
 * 提供三块业务无关能力，供日志 / WAF / 黑白名单等列表视图复用：
 *   - tableHTML：表格壳（表头 + 行渲染 + 条数上限提示 + 空态）
 *   - createExpander：展开状态器（以行键管理展开/收起，排序过滤后不错位）
 *   - pagingHTML：客户端分页控件（总数 / 页码 / 上一页下一页）
 * 行内容与展开详情由调用方通过 rowHTML 提供，组件不感知业务字段。
 * 依赖 Rock.util.esc / Rock.util.fmtInt。挂载 window.Rock.comp.dataTable。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const esc = Rock.util.esc;
  const fmtInt = Rock.util.fmtInt;

  // 表格壳：columns 为表头（{label,width?} 或字符串），rows 为数据，
  // rowHTML(row, idx) 返回 <tr>…</tr>（可自带展开详情行），maxRows 超限时给提示
  function tableHTML(opts) {
    opts = opts || {};
    const rows = opts.rows || [];
    if (!rows.length) {
      return opts.emptyHTML || '<div class="empty">' + esc(opts.emptyText || '暂无数据') + '</div>';
    }
    const maxRows = opts.maxRows || rows.length;
    const shown = rows.slice(0, maxRows);
    const head = (opts.columns || []).map(function (c) {
      const col = typeof c === 'string' ? { label: c } : c;
      return '<th' + (col.width ? ' style="width:' + col.width + '"' : '') + '>' + esc(col.label) + '</th>';
    }).join('');
    const body = shown.map(function (r, i) { return opts.rowHTML(r, i); }).join('');
    const hint = rows.length > maxRows
      ? '<div class="form-hint" style="margin-top:8px">已达 ' + maxRows + ' 条展示上限，请收窄时间范围或筛选条件。</div>'
      : '';
    return '<div class="table-wrap"' + (opts.maxHeight ? ' style="max-height:' + opts.maxHeight + '"' : '') + '>' +
      '<table class="table"><thead><tr>' + head + '</tr></thead><tbody>' + body + '</tbody></table></div>' + hint;
  }

  // 展开状态器：以行键管理展开/收起；reset 在数据重新加载后清空
  function createExpander() {
    const state = {};
    return {
      toggle: function (key) { state[key] = !state[key]; },
      isOpen: function (key) { return !!state[key]; },
      reset: function () { Object.keys(state).forEach(function (k) { delete state[k]; }); },
    };
  }

  // 分页控件：total/offset/limit 计算页码，act 为翻页动作名（视图事件委托处理）
  function pagingHTML(opts) {
    const total = Number(opts.total) || 0;
    const limit = Number(opts.limit) || 20;
    const offset = Number(opts.offset) || 0;
    const pages = Math.max(1, Math.ceil(total / limit));
    const cur = Math.floor(offset / limit) + 1;
    return '<div class="log-toolbar" style="justify-content:space-between">' +
      '<span class="muted">共 ' + fmtInt(total) + ' 条 · 第 ' + cur + '/' + pages + ' 页</span>' +
      '<div class="tool-group">' +
      '<button class="btn btn-sm" data-act="' + esc(opts.act) + '" data-dir="-1"' + (cur <= 1 ? ' disabled' : '') + '>上一页</button>' +
      '<button class="btn btn-sm" data-act="' + esc(opts.act) + '" data-dir="1"' + (cur >= pages ? ' disabled' : '') + '>下一页</button>' +
      '</div></div>';
  }

  window.Rock.comp.dataTable = { tableHTML, createExpander, pagingHTML };
})();
