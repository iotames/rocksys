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
  // title 默认按纯文本转义；如确需内嵌样式标签（如详情页中文名+小号英文名），
  // 传入 titleHTML（调用方自行转义用户内容），优先级高于 title。
  function headHTML({ title, titleHTML, desc, actions }) {
    return '<div class="page-head">' +
      '<div><div class="page-title">' + (titleHTML != null ? titleHTML : esc(title)) + '</div><div class="page-desc">' + esc(desc) + '</div></div>' +
      (actions || '') +
      '</div>';
  }

  window.Rock.comp.head = { headHTML };
})();
