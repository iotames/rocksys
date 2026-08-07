/* ==========================================================================
 * RockSys 管理控制台 - views/metrics.js 指标页
 * 实时指标卡（QPS / 延迟分位 / 错误率）+ 原生 Canvas 自绘趋势折线图。
 * 挂载到全局命名空间 window.Rock.views.metrics。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const store = Rock.state.store;
  const normalizeMetrics = Rock.state.normalizeMetrics;
  const api = Rock.api;
  const toast = Rock.ui.toast;
  const skeletonHTML = Rock.ui.skeletonHTML;
  const noteUpdated = Rock.ui.noteUpdated;

  // 加载指标（观测 503 时置 metricsError='obs' 展示引导）
  async function load(opts) {
    const first = !store.metrics && !opts.silent;
    if (first) skeleton();
    try {
      const m = await api.get('/admin/metrics');
      store.metrics = normalizeMetrics(m);
      store.metricsError = null;
      Rock.comp.metrics.pushSample(store.metrics);
      noteUpdated();
    } catch (e) {
      store.metricsFailed = !store.metrics;
      if (e.obsDisabled) { store.metricsError = 'obs'; }
      else if (opts.manual && e.status !== 0) { toast('指标加载失败：' + e.message, 'error'); }
    }
    render();
  }

  function skeleton() {
    const host = $('#page-metrics');
    if (host) host.innerHTML = skeletonHTML(5);
  }

  function render() {
    const host = $('#page-metrics');
    if (!host) return;
    if (store.metricsFailed && !store.metrics && store.metricsError !== 'obs') {
      host.innerHTML =
        Rock.comp.head.headHTML({
          title: '指标',
          desc: '实时运行指标与趋势',
          actions: '<button class="btn btn-sm" data-act="metrics-reload">⟳ 刷新</button>',
        }) +
        Rock.comp.empty.emptyCard({
          text: '管理接口不可达，无法加载指标。',
          action: '<button class="btn btn-sm btn-primary" data-act="metrics-reload">重试</button>',
          br: true,
        });
      return;
    }
    if (!store.metrics && store.metricsError !== 'obs') { skeleton(); return; }
    const obsOff = store.metricsError === 'obs';
    host.innerHTML =
      Rock.comp.head.headHTML({
        title: '指标',
        desc: '实时运行指标与趋势（按刷新周期累积采样）',
        actions: '<button class="btn btn-sm" data-act="metrics-reload">⟳ 刷新</button>',
      }) +
      (obsOff
        ? '<div class="alert alert-warning">观测组件未开启，无法获取运行指标。<button class="btn btn-sm btn-primary" data-act="go-obs" style="margin-left:8px">去组件页开启观测</button></div>'
        : '') +
      '<div class="card"><div class="card-title">实时指标</div>' +
      '<div class="' + (obsOff ? 'metric-grid is-off' : '') + '">' + Rock.comp.metrics.metricTiles({ obsOff: obsOff }) + '</div></div>' +
      '<div class="card"><div class="card-title">每秒请求趋势 <span class="card-sub">近 ' + store.metricsHistory.length + ' 次采样</span></div>' +
      '<div class="chart-box"><canvas id="metrics-chart"></canvas></div>' +
      (store.metricsHistory.length < 2 ? '<div class="empty">等待采样数据…（开启自动刷新后趋势自动累积）</div>' : '') +
      '</div>' +
      '<div class="form-hint">数据为当前网关的实时统计；观测未开启时引导开启。</div>';
    Rock.comp.chart.line($('#metrics-chart'), { data: store.metricsHistory, value: p => p.qps });
  }

  // Canvas 趋势折线图薄壳（转调 comp.chart.line，供 main.js resize 钩子调用）
  function drawChart() {
    Rock.comp.chart.line($('#metrics-chart'), { data: store.metricsHistory, value: p => p.qps });
  }

  window.Rock.views.metrics = {
    load,
    render,
    skeleton,
    drawChart,
  };
})();
