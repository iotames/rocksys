/* ==========================================================================
 * RockSys 管理控制台 - components/head.js 页头组件
 * 渲染统一的 page-head 块（标题 + 描述 + 操作按钮区），供各视图复用。
 * 依赖 Rock.util.esc。挂载到全局命名空间 window.Rock.comp.head。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const esc = Rock.util.esc;

  // 渲染 page-head：title/desc 在左侧子 div，actions（预编译按钮 HTML）在其后
  function headHTML({ title, desc, actions }) {
    return '<div class="page-head">' +
      '<div><div class="page-title">' + esc(title) + '</div><div class="page-desc">' + esc(desc) + '</div></div>' +
      (actions || '') +
      '</div>';
  }

  window.Rock.comp.head = { headHTML };
})();
