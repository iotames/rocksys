/* ==========================================================================
 * RockSys 管理控制台 - components/select.js 下拉选项组件
 * 渲染 <option> 列表（[value, label] 二元组），保持选中态。
 * 依赖 Rock.util.esc。挂载到全局命名空间 window.Rock.comp.select。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const esc = Rock.util.esc;

  // 渲染 <option>：list 为 [value, label] 数组；selected 命中时加 selected 属性
  function options(list, selected) {
    return (list || []).map(o =>
      '<option value="' + esc(o[0]) + '"' + (selected === o[0] ? ' selected' : '') + '>' + esc(o[1]) + '</option>'
    ).join('');
  }

  window.Rock.comp.select = { options };
})();