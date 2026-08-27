/* ==========================================================================
 * RockSys 管理控制台 - views/overview.js 概览页
 * 网关信息卡 + 运行指标卡（含趋势图） + HTTP 数据流图（组件节点带开关）
 * + 服务状态总览。
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
        if (store.metrics) {
          store.metricsHistory.push({
            t: Date.now(),
            qps: store.metrics.qps,
            p50: store.metrics.p50_ms,
            p95: store.metrics.p95_ms,
            p99: store.metrics.p99_ms,
            err: store.metrics.error_rate,
          });
          if (store.metricsHistory.length > 240) store.metricsHistory.shift();
        }
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

  // 服务总览大卡片：左上 switch 直启 + 名称点击跳转 + 独立服务标签 + 状态
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
      metricsBody = Rock.comp.metrics.metricTiles({
        obsOff: false,
        metrics: store.metrics,
        history: store.metricsHistory,
      });
    }
    const chartBody = metricsOff
      ? ''
      : '<div class="chart-box" style="height:150px;margin-top:12px"><canvas id="overview-chart"></canvas></div>' +
        (store.metricsHistory.length < 2 ? '<div class="empty">等待采样数据…（开启自动刷新后趋势自动累积）</div>' : '');

    // ---- 独立服务 ----
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

      '<div class="card"><div class="card-title">HTTP 数据流 <span class="card-sub">组件按链路顺序执行 · 开关即启停 · 点击名称进入详情（关闭即降级）</span></div>' +
      Rock.comp.dataflow.renderHTML(store.switches) +
      '</div>' +

      '<div class="card"><div class="card-title">服务状态总览 <span class="card-sub">独立服务 · 点击名称进入详情</span></div>' +
      overviewGridHTML(services, SERVICE_ORDER, 'services') +
      (services.length ? '' : '<div class="form-hint">服务按配置装配，当前未装配独立服务。</div>') +
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
    drawChart,
    ovCardHTML,
    actions: {
      'overview-reload': function () { load({ manual: true }); },
    },
  };
})();
