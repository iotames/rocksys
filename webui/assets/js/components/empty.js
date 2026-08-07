/* ==========================================================================
 * RockSys 管理控制台 - components/empty.js 空态组件
 * 渲染空态/失败提示（.empty）与「接口不可达 + 重试」卡片（.card 包裹）。
 * 依赖 Rock.util.esc。挂载到全局命名空间 window.Rock.comp.empty。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const esc = Rock.util.esc;

  // 空态消息：text 文本 + 可选 <br> + 可选 action 按钮 HTML；
  // padding 可选透传行内样式（还原既有 <div class="empty" style="padding:…"> 布局）
  function message({ text, action, br, padding }) {
    return '<div class="empty"' + (padding ? ' style="padding:' + padding + '"' : '') + '>' +
      esc(text) + (br ? '<br>' : '') + (action || '') + '</div>';
  }

  // 空态卡片：.card 包裹 message（还原「接口不可达 + 重试」卡片）
  function emptyCard(opt) {
    return '<div class="card">' + message(opt) + '</div>';
  }

  window.Rock.comp.empty = { message, emptyCard };
})();
