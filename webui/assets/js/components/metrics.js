/* ==========================================================================
 * RockSys 管理控制台 - components/metrics.js 指标组件
 * 实时指标采样累积 / 请求量环比 / QPS 格式化 / 指标卡渲染。
 * 依赖 Rock.state.store / Rock.util.fmtInt / Rock.state.fmtRate / Rock.util.esc。
 * 挂载到全局命名空间 window.Rock.comp.metrics。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const esc = Rock.util.esc;
  const fmtInt = Rock.util.fmtInt;
  const store = Rock.state.store;
  const fmtRate = Rock.state.fmtRate;

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

  function metricTiles({ obsOff }) {
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

  window.Rock.comp.metrics = { pushSample, delta, fmtQps, metricTiles };
})();