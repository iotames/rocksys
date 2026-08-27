/* ==========================================================================
 * RockSys 管理控制台 - components/componentState.js 组件状态元数据组件
 * 组件状态（enabled/draining/disabled）的文案与色点/标签映射，
 * 以及组件展示元数据（title/desc/slotLabel）的取用（来源 /admin/meta，经 Rock.state.componentMeta）。
 * 依赖 Rock.state。挂载到全局命名空间 window.Rock.comp.componentState。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  // 状态 → 文案/色点/标签
  function stateMeta(state) {
    if (state === 'enabled') return { text: '已启用', dot: 'dot-ok', tag: 'tag-green' };
    if (state === 'draining') return { text: '切换中', dot: 'dot-warn', tag: 'tag-orange' };
    return { text: '已关闭', dot: 'dot-off', tag: 'tag-gray' };
  }

  // 组件展示元数据：kind 为 switch 数据语义（component=独立服务 / middleware=链中间件），
  // 映射到 componentMeta 的 'service'/'middleware' 取用；未命中回退英文名与默认环节。
  function meta(name, kind) {
    return Rock.state.componentMeta(name, kind === 'component' ? 'service' : 'middleware');
  }

  window.Rock.comp.componentState = { stateMeta, meta };
})();
