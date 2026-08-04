/* ==========================================================================
 * RockSys 管理控制台 - views/overview.js 概览页
 * 网关信息卡 + 运行指标卡 + 降级链可视化 + 组件状态总览。
 * 依赖 Rock.state / Rock.util / Rock.ui / Rock.api / Rock.views.metrics。
 * 挂载到全局命名空间 window.Rock.views.overview。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const fmtInt = Rock.util.fmtInt;
  const store = Rock.state.store;
  const COMPONENT_META = Rock.state.COMPONENT_META;
  const COMPONENT_ORDER = Rock.state.COMPONENT_ORDER;
  const normalizeSwitches = Rock.state.normalizeSwitches;
  const normalizeMetrics = Rock.state.normalizeMetrics;
  const fmtRate = Rock.state.fmtRate;
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
        Rock.views.metrics.pushSample(store.metrics);
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

  function render() {
    const host = $('#page-overview');
    if (!host) return;
    if (store.overviewFailed && !store.baseLoaded && !store.switchesLoaded) {
      host.innerHTML =
        '<div class="card"><div class="empty">管理接口不可达，无法加载概览数据。' +
        '<br><button class="btn btn-sm btn-primary" data-act="overview-reload">重试</button></div></div>';
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

    // ---- 运行指标卡 ----
    const metricsOff = store.metricsError === 'obs';
    let metricsBody;
    if (metricsOff) {
      metricsBody =
        '<div class="empty" style="padding:24px 8px">' +
        '<div>观测组件未开启，无法获取运行指标</div>' +
        '<button class="btn btn-sm btn-primary" data-act="go-obs">去组件页开启观测</button>' +
        '</div>';
    } else if (!store.metrics) {
      metricsBody = '<div class="empty" style="padding:24px 8px">暂无指标数据</div>';
    } else {
      const m = store.metrics;
      const delta = Rock.views.metrics.delta();
      const tiles = [
        { label: '每秒请求', value: Rock.views.metrics.fmtQps(m.qps), unit: '请求/秒', delta: delta.delta },
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
      metricsBody = '<div class="metric-grid">' + tiles + '</div>';
    }

    // ---- 组件状态总览 ----
    const comps = store.switches.slice().sort((a, x) => {
      const ia = COMPONENT_ORDER.indexOf(a.name);
      const ix = COMPONENT_ORDER.indexOf(x.name);
      return (ia < 0 ? 999 : ia) - (ix < 0 ? 999 : ix);
    });
    const hasMq = comps.some(c => c.name === 'mq');
    let compBody;
    if (!comps.length) {
      compBody = '<div class="empty">暂无组件数据</div>';
    } else {
      compBody = '<div class="comp-mini-grid">' + comps.map(c => {
        const meta = COMPONENT_META[c.name] || { title: c.name, slotLabel: c.kind === 'component' ? '独立服务' : '链中间件' };
        const dotCls = c.state === 'enabled' ? 'dot-ok' : (c.state === 'draining' ? 'dot-warn' : 'dot-off');
        return '<div class="comp-mini" data-act="goto-components">' +
          '<span class="dot ' + dotCls + '"></span>' +
          '<span class="comp-mini-name">' + esc(meta.title) + '</span>' +
          '<span class="comp-mini-slot">' + esc(meta.slotLabel || '') + '</span>' +
          '</div>';
      }).join('') + '</div>';
      if (!hasMq) {
        compBody += '<div class="form-hint" style="margin-top:10px">消息组件按配置装配（MQ_ENABLED + MQ_DSN），当前未装配。</div>';
      }
    }

    host.innerHTML =
      '<div class="page-head">' +
      '<div><div class="page-title">概览</div><div class="page-desc">30 秒完成巡检：网关状态 · 降级链 · 指标 · 组件</div></div>' +
      '<button class="btn btn-sm" data-act="overview-reload">⟳ 刷新</button>' +
      '</div>' +

      '<div class="grid grid-2">' +
      '<div class="card hoverable" data-act="goto-config" style="cursor:pointer">' +
      '<div class="card-title">网关信息 <span class="card-sub">点击进入配置页</span></div>' + gwItems +
      '</div>' +
      '<div class="card"><div class="card-title">运行指标 <span class="card-sub">实时</span></div>' + metricsBody + '</div>' +
      '</div>' +

      '<div class="card"><div class="card-title">降级链 <span class="card-sub">核心概念：关闭只是降级，转发永不中断</span></div>' +
      renderDegradeChain(store.switches) +
      '</div>' +

      '<div class="card"><div class="card-title">组件状态总览 <span class="card-sub">点击卡片进入组件页</span></div>' + compBody + '</div>';
  }

  window.Rock.views.overview = {
    load,
    render,
    skeleton,
    degradeState,
    renderDegradeChain,
  };
})();
