/* ==========================================================================
 * RockSys 管理控制台 - components/metrics.js 指标组件
 * 请求量环比 / QPS 格式化 / 指标卡渲染（纯计算与渲染，不持有全局状态）。
 * 数据由调用方（overview 视图）传入：metrics 为最新指标，history 为采样数组。
 * 依赖 Rock.util.fmtInt / Rock.state.fmtRate / Rock.util.esc。
 * 挂载到全局命名空间 window.Rock.comp.metrics。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const esc = Rock.util.esc;
  const fmtInt = Rock.util.fmtInt;
  const fmtRate = Rock.state.fmtRate;

  // 请求量环比变化（相对趋势窗口首条）
  function delta(history) {
    const h = history || [];
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

  // 运行时间格式化：≥1 天 → "211 天 12:47:40"（value=天数，unit=时分秒）；不足 1 天 → "12:47:40"
  function fmtUptime(sec) {
    sec = Math.max(0, Math.floor(Number(sec) || 0));
    const d = Math.floor(sec / 86400);
    const h = Math.floor(sec % 86400 / 3600);
    const m = Math.floor(sec % 3600 / 60);
    const s = sec % 60;
    const pad = n => (n < 10 ? '0' : '') + n;
    const hms = pad(h) + ':' + pad(m) + ':' + pad(s);
    return d > 0 ? { value: String(d), unit: '天 ' + hms } : { value: hms, unit: '' };
  }

  // 实时瓦片（METRICS_WINDOW：纯计数口径，延迟分位数已拆分至流量统计区）。
  // opts：{ metrics, history, uptime, windowLabel }（windowLabel 如 "1m"，缺省 1m）。
  function metricTiles({ metrics, history, uptime, windowLabel }) {
    const m = metrics;
    if (!m) return '<div class="empty" style="padding:24px 8px">暂无指标数据</div>';
    const d = delta(history);
    const tiles = [
      { label: '请求速率（' + (windowLabel || '1m') + '）', value: fmtQps(m.qps), unit: '请求/秒', delta: d.delta },
      { label: '错误率（' + (windowLabel || '1m') + '）', value: fmtRate(m.error_rate), unit: '', delta: null },
      { label: '延迟 P50（' + (windowLabel || '1m') + '）', value: fmtInt(m.p50_ms), unit: '毫秒', delta: null },
      { label: '延迟 P95（' + (windowLabel || '1m') + '）', value: fmtInt(m.p95_ms), unit: '毫秒', delta: null },
      { label: '延迟 P99（' + (windowLabel || '1m') + '）', value: fmtInt(m.p99_ms), unit: '毫秒', delta: null },
    ];
    if (uptime != null) {
      const up = fmtUptime(uptime);
      tiles.push({ label: '运行时间', value: up.value, unit: up.unit, delta: null });
    }
    const tilesHTML = tiles.map(t =>
      '<div class="metric-tile"><div class="metric-label">' + esc(t.label) + '</div>' +
      '<div class="metric-value">' + esc(t.value) + (t.unit ? '<span class="metric-unit">' + esc(t.unit) + '</span>' : '') + '</div>' +
      (t.delta ? '<div class="metric-delta ' + t.delta.cls + '">' + t.delta.txt + '</div>' : '') +
      '</div>'
    ).join('');
    return '<div class="metric-grid">' + tilesHTML + '</div>';
  }

  window.Rock.comp.metrics = { delta, fmtQps, fmtUptime, metricTiles };
})();
