/* ==========================================================================
 * RockSys 管理控制台 - components/tabs.js 页签组件
 * 渲染统一的 .tabs 结构（支持数量角标），供详情页 / 全局配置 / WAF 等复用。
 * 业务无关：items 为 [{ name, label, count }]，active 为当前选中 name。
 * 依赖 Rock.util.esc。挂载到全局命名空间 window.Rock.comp.tabs。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const esc = Rock.util.esc;

  // 渲染 tabs：opts.act 为 data-act 动作名，opts.nameAttr 可指定选中态属性（默认 data-name）
  function tabsHTML(items, active, opts) {
    opts = opts || {};
    const act = opts.act || 'tab';
    const nameAttr = opts.nameAttr || 'data-name';
    return '<div class="tabs">' + (items || []).map(function (it) {
      return '<div class="tab' + (it.name === active ? ' active' : '') + '"' +
        ' data-act="' + esc(act) + '" ' + nameAttr + '="' + esc(it.name) + '">' +
        esc(it.label) +
        (it.count ? '<span class="tab-count">' + esc(it.count) + '</span>' : '') +
        '</div>';
    }).join('') + '</div>';
  }

  window.Rock.comp.tabs = { tabsHTML };
})();
