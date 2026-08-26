/* ==========================================================================
 * RockSys 管理控制台 - views/overview.js 概览页
 * 网关信息卡 + 运行指标卡（含趋势图） + HTTP 数据流图 + 组件状态总览（卡片带开关）
 * + 服务状态总览 + 降级链可视化。
 * 依赖 Rock.state / Rock.util / Rock.ui / Rock.api / Rock.comp.{metrics,componentState,dataflow,chart}。
 * 挂载到全局命名空间 window.Rock.views.overview。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const store = Rock.state.store;
  const COMPONENT_ORDER = Rock.state.COMPONENT_ORDER;
  const SERVICE_ORDER = Rock.state.SERVICE_ORDER;
  const normalizeSwitches = Rock.state.normalizeSwitches;
  const normalizeMetrics = Rock.state.normalizeMetrics;
  const api = Rock.api;
  const toast = Rock.ui.toast;
  const skeletonHTML = Rock.ui.skeletonHTML;
  const noteUpdated = Rock.ui.noteUpdated;

  // 加载概览：底座信息 + 组件状态 + 指标（指标失败单独容错）
  async function load(opts) {
    const first = !store.baseLoaded && !opts.silent;
    if (first) skeleton();
    let baseOk = false;
    try {
      const [base, switches] = await Promise.all([
        api.get('/admin/config'),
        api.get('/admin/switch/list'),
      ]);
      store.base = base || store.base || {};
      store.baseLoaded = true;
      store.switches = normalizeSwitches(switches);
      store.switchesLoaded = true;
      baseOk = true;
      noteUpdated();
      // 顶部管理地址
      const addr = $('#gw-addr');
      if (addr) addr.textContent = '管理地址：' + (store.base.admin || '—');
    } catch (e) {
      store.overviewFailed = !store.baseLoaded && !store.switchesLoaded;
      if (opts.manual && e.status !== 0 && !e.obsDisabled) {
        toast('概览加载失败：' + e.message, 'error');
      }
    }
    if (baseOk) {
      try {
        const m = await api.get('/admin/metrics');
        store.metrics = normalizeMetrics(m);
        store.metricsError = null;
        Rock.comp.metrics.pushSample(store.metrics);
        noteUpdated();
      } catch (e) {
        if (e.obsDisabled) { store.metricsError = 'obs'; }
        else if (opts.manual && e.status !== 0) { toast('指标加载失败：' + e.message, 'error'); }
      }
    }
    render();
  }

  function skeleton() {
    const host = $('#page-overview');
    if (!host) return;
    host.innerHTML = skeletonHTML(6);
  }

  // 降级链：L1 防护 / L2 分发 / L3 结果
  function degradeState(switches) {
    const get = name => {
      const s = switches.find(x => x.name === name);
      return !!(s && s.state === 'enabled');
    };
    const sOn = get('shield');
    const dOn = get('dispatch');
    const rOn = get('result');
    const closed = [];
    if (!rOn) closed.push('L3 结果处理');
    if (!dOn) closed.push('L2 分发');
    if (!sOn) closed.push('L1 防护');
    let currentIdx;   // 当前能力节点下标
    let mode;
    let modeCls = 'tag-green';
    if (closed.length === 0) {
      currentIdx = 0; mode = '全量能力：防护 → 分发 → 结果处理全链路开启';
    } else if (closed.length === 3) {
      currentIdx = 4; mode = '裸转发兜底：降级链已全部关闭，转发永不中断';
      modeCls = 'tag-orange';
    } else {
      // 降级运行：当前能力边界 = 最深的仍开启保护环
      if (sOn) { currentIdx = 3; mode = '降级运行：已关闭 ' + closed.join('、'); }
      else if (dOn) { currentIdx = 2; mode = '降级运行：已关闭 ' + closed.join('、'); }
      else { currentIdx = 4; mode = '降级运行：已关闭 ' + closed.join('、'); }
      modeCls = 'tag-orange';
    }
    return { sOn, dOn, rOn, currentIdx, mode, modeCls, closed };
  }

  function renderDegradeChain(switches) {
    const d = degradeState(switches);
    const nodes = [
      { label: '全量能力', sub: '激活', on: d.closed.length === 0, fallback: false, badge: d.currentIdx === 0 ? '当前' : null, badgeCls: '', current: d.currentIdx === 0 },
      { label: 'L3 结果', sub: d.rOn ? '开' : '关', on: d.rOn, fallback: false, badge: null, badgeCls: '', current: d.currentIdx === 1 },
      { label: 'L2 分发', sub: d.dOn ? '开' : '关', on: d.dOn, fallback: false, badge: null, badgeCls: '', current: d.currentIdx === 2 },
      { label: 'L1 防护', sub: d.sOn ? '开' : '关', on: d.sOn, fallback: false, badge: null, badgeCls: '', current: d.currentIdx === 3 },
      { label: '裸转发', sub: '兜底', on: true, fallback: true, badge: '永不中断', badgeCls: 'badge-red', current: d.currentIdx === 4 },
    ];
    let html = '<div class="chain">';
    nodes.forEach((n, i) => {
      if (i > 0) html += '<div class="chain-link"></div>';
      let cls = '';
      if (n.fallback) cls += ' fallback';
      else if (n.on) cls += ' on';
      if (n.current) cls += ' current';
      html += '<div class="chain-node">' +
        '<div class="chain-pill' + cls + '">' + esc(n.label) +
        (n.badge ? '<span class="badge ' + (n.badgeCls || '') + '">' + esc(n.badge) + '</span>' : '') +
        '</div>' +
        '<div class="chain-state">' + esc(n.sub) + '</div>' +
        '</div>';
    });
    html += '</div>';
    html += '<div class="chain-mode"><span class="tag ' + d.modeCls + '">' + esc(d.closed.length === 0 ? '全量' : (d.closed.length === 3 ? '裸转发' : '降级')) + '</span> ' +
      esc(d.mode) + '　<span class="muted">关闭只是降级，转发永不中断</span></div>';
    return html;
  }

  // 组件/服务总览大卡片：左上 switch 直启 + 名称点击跳转 + 环节标签 + 状态
  function ovCardHTML(s, routeBase) {
    const meta = Rock.comp.componentState.meta(s.name, s.kind);
    const st = Rock.comp.componentState.stateMeta(s.state);
    const slot = s.kind === 'component' ? '独立服务' : (meta.slotLabel || '链中间件');
    return '<div class="ov-card' + (s.state === 'draining' ? ' is-draining' : '') + '">' +
      '<div class="ov-head">' +
      '<label class="el-switch" title="' + esc(st.text) + '">' +
      '<input type="checkbox" data-act="detail-toggle" data-name="' + esc(s.name) + '" data-type="' + (routeBase === 'services' ? 'service' : 'component') + '"' +
      (s.state === 'enabled' ? ' checked' : '') +
      (s.state === 'draining' ? ' disabled' : '') + '>' +
      '<span class="el-switch-core"></span></label>' +
      '<div class="ov-name" data-act="nav-detail" data-route="' + routeBase + '/' + esc(s.name) + '"' +
      ' title="点击进入 ' + esc(meta.title) + ' ' + esc(s.name) + ' 页">' +
      '<b>' + esc(meta.title) + '</b><i>' + esc(s.name) + '</i></div>' +
      '<span class="tag tag-blue">' + esc(slot) + '</span>' +
      '</div>' +
      '<div class="ov-foot"><span class="dot ' + st.dot + '"></span>' +
      '<span class="ov-state">' + esc(st.text) + '</span>' +
      '</div>' +
      '</div>';
  }

  // 按固定顺序渲染总览卡片
  function overviewGridHTML(switches, order, routeBase) {
    const list = switches.slice().sort((a, x) => {
      const ia = order.indexOf(a.name);
      const ix = order.indexOf(x.name);
      return (ia < 0 ? 999 : ia) - (ix < 0 ? 999 : ix);
    });
    if (!list.length) return Rock.comp.empty.message({ text: '暂无数据' });
    return '<div class="ov-grid">' + list.map(s => ovCardHTML(s, routeBase)).join('') + '</div>';
  }

  function render() {
    const host = $('#page-overview');
    if (!host) return;
    if (store.overviewFailed && !store.baseLoaded && !store.switchesLoaded) {
      host.innerHTML = Rock.comp.empty.emptyCard({
        text: '管理接口不可达，无法加载概览数据。',
        action: '<button class="btn btn-sm btn-primary" data-act="overview-reload">重试</button>',
        br: true,
      });
      return;
    }
    if (!store.baseLoaded && !store.switchesLoaded) { skeleton(); return; }

    // ---- 网关信息卡 ----
    const b = store.base || {};
    const gwItems = [
      ['监听端口', b.listen || '—'],
      ['默认后端', b.upstream || '—'],
      ['转发超时', (b.timeout != null ? b.timeout : '—') + ' 秒'],
      ['管理地址', b.admin || '—'],
      ['配置文件', b.config_file || '—'],
      ['日志级别', b.log_level || '—'],
    ].map(it =>
      '<div class="gw-item"><span class="k">' + esc(it[0]) + '</span><span class="v">' + esc(it[1]) + '</span></div>'
    ).join('');

    // ---- 运行指标卡（含趋势图，指标页合并至此）----
    const metricsOff = store.metricsError === 'obs';
    let metricsBody;
    if (metricsOff) {
      metricsBody =
        '<div class="empty" style="padding:24px 8px">' +
        '<div>观测组件未开启，无法获取运行指标</div>' +
        '<button class="btn btn-sm btn-primary" data-act="go-obs">去组件页开启观测</button>' +
        '</div>';
    } else if (!store.metrics) {
      metricsBody = Rock.comp.empty.message({ text: '暂无指标数据', padding: '24px 8px' });
    } else {
      metricsBody = Rock.comp.metrics.metricTiles({ obsOff: false });
    }
    const chartBody = metricsOff
      ? ''
      : '<div class="chart-box" style="height:150px;margin-top:12px"><canvas id="overview-chart"></canvas></div>' +
        (store.metricsHistory.length < 2 ? '<div class="empty">等待采样数据…（开启自动刷新后趋势自动累积）</div>' : '');

    // ---- 组件与服务 ----
    const comps = store.switches.filter(s => s.kind !== 'component');
    const services = store.switches.filter(s => s.kind === 'component');

    host.innerHTML =
      Rock.comp.head.headHTML({
        title: '概览',
        desc: '30 秒完成巡检：网关状态 · 数据流 · 指标 · 组件 · 服务',
        actions: '<button class="btn btn-sm" data-act="overview-reload">⟳ 刷新</button>',
      }) +

      '<div class="grid grid-2">' +
      '<div class="card hoverable" data-act="goto-config" style="cursor:pointer">' +
      '<div class="card-title">网关信息 <span class="card-sub">点击进入全局配置</span></div>' + gwItems +
      '</div>' +
      '<div class="card"><div class="card-title">运行指标 <span class="card-sub">实时 · 趋势</span></div>' + metricsBody + chartBody + '</div>' +
      '</div>' +

      '<div class="card"><div class="card-title">HTTP 数据流 <span class="card-sub">组件按链路顺序执行，关闭即降级（点击节点进入组件页）</span></div>' +
      Rock.comp.dataflow.renderHTML(store.switches) +
      '</div>' +

      '<div class="grid grid-2">' +
      '<div class="card"><div class="card-title">组件状态总览 <span class="card-sub">开关即启停 · 点击名称进入详情</span></div>' +
      overviewGridHTML(comps, COMPONENT_ORDER, 'components') +
      '</div>' +
      '<div class="card"><div class="card-title">服务状态总览 <span class="card-sub">独立服务 · 点击名称进入详情</span></div>' +
      overviewGridHTML(services, SERVICE_ORDER, 'services') +
      (services.length ? '' : '<div class="form-hint">服务按配置装配，当前未装配独立服务。</div>') +
      '</div>' +
      '</div>' +

      '<div class="card"><div class="card-title">降级链 <span class="card-sub">核心概念：关闭只是降级，转发永不中断</span></div>' +
      renderDegradeChain(store.switches) +
      '</div>';

    if (!metricsOff && store.metrics) drawChart();
  }

  // 趋势折线图（main.js resize 钩子调用）
  function drawChart() {
    Rock.comp.chart.line($('#overview-chart'), { data: store.metricsHistory, value: p => p.qps });
  }

  window.Rock.views.overview = {
    load,
    render,
    skeleton,
    degradeState,
    renderDegradeChain,
    drawChart,
    ovCardHTML,
  };
})();
