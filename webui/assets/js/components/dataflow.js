/* ==========================================================================
 * RockSys 管理控制台 - components/dataflow.js HTTP 数据流图组件
 * 纯 CSS 渲染的请求/响应数据流图（依据 docs/HTTP_DATAFLOW.md）：
 *   下行（请求）：Client → 入口 → L1 防护(shield→trace→auth) → L2 决策(dispatch→rewrite→script)
 *                 → 转发引擎 → 后端
 *   上行（响应）：后端 → L3 结果(result→copy→obs) → Client
 * 组件节点内嵌开关（即启停）+ 中文/英文名（点击跳转组件页），关闭的组件
 * 灰化 + 虚线直观看出"链路缺口"；状态由开关直接体现，不再单独展示色点。
 * 依赖 Rock.state（componentMeta）/ Rock.comp.componentState。挂载 window.Rock.comp.dataflow。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const esc = Rock.util.esc;

  // 环节定义：name / 组件列表 / 组标签 / 组内箭头方向
  // （上行响应组整行 row-reverse：后端在右、Client 在左，组内箭头统一向左）
  const GROUPS = [
    { label: 'L1 防护', sub: '入口环节', names: ['shield', 'trace', 'auth'], arrow: '→' },
    { label: 'L2 决策', sub: '分发环节', names: ['dispatch', 'rewrite', 'script'], arrow: '→' },
  ];
  // 上行（响应）执行顺序：result → copy → obs（Tail 槽位逆序：result 先改写、obs 最后记录）；
  // df-up 整行 row-reverse（视觉后端在右、Client 在左），组内自左向右为 obs→copy→result，
  // 配合向左箭头还原真实流向（后端 → result → copy → obs → Client）
  const RESP_GROUP = { label: 'L3 结果', sub: '响应环节', names: ['obs', 'copy', 'result'], arrow: '←' };

  function stateOf(switches, name) {
    const s = (switches || []).find(x => x.name === name);
    return s ? s.state : 'disabled';
  }

  // 组件节点：启停开关 + 中文名 + 英文名；关闭灰化虚线；点击名称跳组件页
  function nodeHTML(switches, name) {
    const st = stateOf(switches, name);
    const meta = Rock.comp.componentState.meta(name, 'middleware');
    const sm = Rock.comp.componentState.stateMeta(st);
    const off = st !== 'enabled';
    return '<div class="df-node' + (off ? ' off' : '') + '"' +
      ' data-tip="' + esc(meta.title) + ' · ' + esc(meta.slotLabel || '') + ' · ' + esc(sm.text) + '"' +
      '>' +
      '<label class="el-switch" title="' + esc(sm.text) + '">' +
      '<input type="checkbox" data-act="detail-toggle" data-name="' + esc(name) + '" data-type="component"' +
      (st === 'enabled' ? ' checked' : '') +
      (st === 'draining' ? ' disabled' : '') + '>' +
      '<span class="el-switch-core"></span></label>' +
      '<div class="df-name" data-act="nav-detail" data-route="components/' + esc(name) + '"' +
      ' title="点击进入 ' + esc(meta.title) + ' ' + esc(name) + ' 页">' +
      '<b>' + esc(meta.title) + '</b><i>' + esc(name) + '</i></div>' +
      '</div>';
  }

  // 环节分组：组标签 + 组内组件横排
  function groupHTML(switches, g) {
    const arrow = g.arrow || '→';
    return '<div class="df-group">' +
      '<div class="df-group-label">' + esc(g.label) + ' <span>' + esc(g.sub) + '</span></div>' +
      '<div class="df-group-body">' + g.names.map(n => nodeHTML(switches, n)).join('<div class="df-arrow">' + arrow + '</div>') + '</div>' +
      '</div>';
  }

  // 整图渲染（两行：请求下行 / 响应上行）
  function renderHTML(switches) {
    const ext = (label, sub) =>
      '<div class="df-node df-ext"><b>' + esc(label) + '</b><i>' + esc(sub) + '</i></div>';
    const down =
      '<div class="df-row">' +
      ext('Client', 'HTTP 请求') + '<div class="df-arrow">→</div>' +
      '<div class="df-node df-base" data-tip="入口：trace_id 生成 · activeCount+1"><b>入口</b><i>Adapter</i></div>' + '<div class="df-arrow">→</div>' +
      groupHTML(switches, GROUPS[0]) + '<div class="df-arrow">→</div>' +
      groupHTML(switches, GROUPS[1]) + '<div class="df-arrow">→</div>' +
      '<div class="df-node df-base" data-tip="转发引擎：确定 Target · 超时 18s"><b>转发引擎</b><i>Forward</i></div>' + '<div class="df-arrow">→</div>' +
      ext('后端', '目标服务') +
      '</div>';
    const up =
      '<div class="df-row df-up">' +
      ext('后端', '目标服务') + '<div class="df-arrow">←</div>' +
      groupHTML(switches, RESP_GROUP) + '<div class="df-arrow">←</div>' +
      ext('Client', 'HTTP 响应') +
      '</div>';
    return '<div class="dataflow">' + down + up + '</div>';
  }

  window.Rock.comp.dataflow = { renderHTML, nodeHTML, stateOf };
})();
