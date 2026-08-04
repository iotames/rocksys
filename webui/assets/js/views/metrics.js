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
  const esc = Rock.util.esc;
  const fmtInt = Rock.util.fmtInt;
  const pad2 = Rock.util.pad2;
  const toDate = Rock.util.toDate;
  const store = Rock.state.store;
  const normalizeMetrics = Rock.state.normalizeMetrics;
  const fmtRate = Rock.state.fmtRate;
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
      pushSample(store.metrics);
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

  // 趋势采样累积（上限 240 点）
  function pushSample(m) {
    if (!m) return;
    store.metricsHistory.push({
      t: Date.now(),
      qps: m.qps,
      p50: m.p50_ms,
      p95: m.p95_ms,
      p99: m.p99_ms,
      err: m.error_rate,
    });
    if (store.metricsHistory.length > 240) store.metricsHistory.shift();
  }

  // 请求量环比变化（相对趋势窗口首条）
  function delta() {
    const h = store.metricsHistory;
    if (h.length < 2) return { delta: null };
    const first = h[0].qps;
    const last = h[h.length - 1].qps;
    if (first <= 0) return { delta: null };
    const pct = ((last - first) / first) * 100;
    const txt = (pct >= 0 ? '▲ +' : '▼ ') + Math.abs(pct).toFixed(1) + '%';
    const cls = Math.abs(pct) < 0.05 ? 'delta-flat' : (pct >= 0 ? 'delta-up' : 'delta-down');
    return { delta: { txt, cls } };
  }

  // QPS 展示（大数值千分位，小数值保留两位小数）
  function fmtQps(qps) {
    qps = Number(qps) || 0;
    if (qps >= 1) return fmtInt(qps);
    return qps.toFixed(2);
  }

  function metricTilesHTML(obsOff) {
    if (obsOff) {
      const labels = ['每秒请求', '延迟 50%', '延迟 95%', '延迟 99%', '错误率'];
      return labels.map(l =>
        '<div class="metric-tile"><div class="metric-label">' + esc(l) + '</div><div class="metric-value">—</div></div>'
      ).join('');
    }
    const m = store.metrics;
    if (!m) return '<div class="empty" style="padding:24px 8px">暂无指标数据</div>';
    const d = delta();
    const tiles = [
      { label: '每秒请求', value: fmtQps(m.qps), unit: '请求/秒', delta: d.delta },
      { label: '延迟 50%', value: fmtInt(m.p50_ms), unit: '毫秒', delta: null },
      { label: '延迟 95%', value: fmtInt(m.p95_ms), unit: '毫秒', delta: null },
      { label: '延迟 99%', value: fmtInt(m.p99_ms), unit: '毫秒', delta: null },
      { label: '错误率', value: fmtRate(m.error_rate), unit: '', delta: null },
    ].map(t =>
      '<div class="metric-tile"><div class="metric-label">' + esc(t.label) + '</div>' +
      '<div class="metric-value">' + esc(t.value) + (t.unit ? '<span class="metric-unit">' + esc(t.unit) + '</span>' : '') + '</div>' +
      (t.delta ? '<div class="metric-delta ' + t.delta.cls + '">' + t.delta.txt + '</div>' : '') +
      '</div>'
    ).join('');
    return '<div class="metric-grid">' + tiles + '</div>';
  }

  function render() {
    const host = $('#page-metrics');
    if (!host) return;
    if (store.metricsFailed && !store.metrics && store.metricsError !== 'obs') {
      host.innerHTML =
        '<div class="page-head"><div><div class="page-title">指标</div><div class="page-desc">实时运行指标与趋势</div></div>' +
        '<button class="btn btn-sm" data-act="metrics-reload">⟳ 刷新</button></div>' +
        '<div class="card"><div class="empty">管理接口不可达，无法加载指标。' +
        '<br><button class="btn btn-sm btn-primary" data-act="metrics-reload">重试</button></div></div>';
      return;
    }
    if (!store.metrics && store.metricsError !== 'obs') { skeleton(); return; }
    const obsOff = store.metricsError === 'obs';
    host.innerHTML =
      '<div class="page-head">' +
      '<div><div class="page-title">指标</div><div class="page-desc">实时运行指标与趋势（按刷新周期累积采样）</div></div>' +
      '<button class="btn btn-sm" data-act="metrics-reload">⟳ 刷新</button>' +
      '</div>' +
      (obsOff
        ? '<div class="alert alert-warning">观测组件未开启，无法获取运行指标。<button class="btn btn-sm btn-primary" data-act="go-obs" style="margin-left:8px">去组件页开启观测</button></div>'
        : '') +
      '<div class="card"><div class="card-title">实时指标</div>' +
      '<div class="' + (obsOff ? 'metric-grid is-off' : '') + '">' + metricTilesHTML(obsOff) + '</div></div>' +
      '<div class="card"><div class="card-title">每秒请求趋势 <span class="card-sub">近 ' + store.metricsHistory.length + ' 次采样</span></div>' +
      '<div class="chart-box"><canvas id="metrics-chart"></canvas></div>' +
      (store.metricsHistory.length < 2 ? '<div class="empty">等待采样数据…（开启自动刷新后趋势自动累积）</div>' : '') +
      '</div>' +
      '<div class="form-hint">数据为当前网关的实时统计；观测未开启时引导开启。</div>';
    drawChart();
  }

  // Canvas 自绘趋势折线图
  function drawChart() {
    const canvas = $('#metrics-chart');
    if (!canvas) return;
    const hist = store.metricsHistory;
    const container = canvas.parentElement;
    const W = container.clientWidth;
    const H = container.clientHeight;
    if (!W || !H) return;
    const dpr = window.devicePixelRatio || 1;
    canvas.width = W * dpr;
    canvas.height = H * dpr;
    canvas.style.width = W + 'px';
    canvas.style.height = H + 'px';
    const ctx = canvas.getContext('2d');
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, W, H);
    if (hist.length < 2) {
      ctx.fillStyle = '#8b949e';
      ctx.font = '12px sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText('等待采样数据…', W / 2, H / 2);
      return;
    }
    const padL = 46, padR = 12, padT = 14, padB = 26;
    const iw = W - padL - padR;
    const ih = H - padT - padB;
    if (iw <= 0 || ih <= 0) return;
    let max = 10;
    hist.forEach(p => { if (p.qps > max) max = p.qps; });
    max = max * 1.15;
    const n = hist.length;
    const xAt = i => padL + (n === 1 ? 0 : (i / (n - 1)) * iw);
    const yAt = v => padT + ih - (v / max) * ih;

    // 横向网格 + Y 轴刻度
    ctx.strokeStyle = 'rgba(48,54,61,.55)';
    ctx.fillStyle = '#8b949e';
    ctx.font = '11px sans-serif';
    ctx.textAlign = 'right';
    ctx.lineWidth = 1;
    for (let i = 0; i <= 4; i++) {
      const y = padT + ih - (ih * i) / 4;
      ctx.beginPath();
      ctx.moveTo(padL, y);
      ctx.lineTo(W - padR, y);
      ctx.stroke();
      const v = (max * i) / 4;
      ctx.fillText(v >= 1000 ? fmtInt(v) : v.toFixed(1), padL - 6, y + 4);
    }
    // X 轴时间刻度
    ctx.textAlign = 'center';
    [0, 0.5, 1].forEach(f => {
      const idx = Math.min(n - 1, Math.round((n - 1) * f));
      const x = xAt(idx);
      ctx.fillText(fmtClock(hist[idx].t), x, H - 8);
    });
    // 面积渐变
    const grad = ctx.createLinearGradient(0, padT, 0, padT + ih);
    grad.addColorStop(0, 'rgba(47,129,247,.3)');
    grad.addColorStop(1, 'rgba(47,129,247,0)');
    ctx.beginPath();
    hist.forEach((p, i) => {
      if (i === 0) ctx.moveTo(xAt(i), yAt(p.qps));
      else ctx.lineTo(xAt(i), yAt(p.qps));
    });
    ctx.lineTo(xAt(n - 1), padT + ih);
    ctx.lineTo(xAt(0), padT + ih);
    ctx.closePath();
    ctx.fillStyle = grad;
    ctx.fill();
    // 折线
    ctx.beginPath();
    hist.forEach((p, i) => {
      if (i === 0) ctx.moveTo(xAt(i), yAt(p.qps));
      else ctx.lineTo(xAt(i), yAt(p.qps));
    });
    ctx.strokeStyle = '#2f81f7';
    ctx.lineWidth = 1.8;
    ctx.stroke();
    // 最新点
    const last = hist[n - 1];
    ctx.beginPath();
    ctx.arc(xAt(n - 1), yAt(last.qps), 3, 0, Math.PI * 2);
    ctx.fillStyle = '#2f81f7';
    ctx.fill();
  }

  function fmtClock(ts) {
    const d = toDate(ts);
    return d ? pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds()) : '';
  }

  window.Rock.views.metrics = {
    load,
    render,
    skeleton,
    pushSample,
    delta,
    fmtQps,
    metricTilesHTML,
    drawChart,
  };
})();
