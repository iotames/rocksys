/* ==========================================================================
 * RockSys 管控控制台 - views/waf.js WAF 拦截统计页
 * 数据来源（admin API）：
 *   - GET  /admin/shield/metrics  近 1 分钟实时计数（内存滑动窗口，DB 未配置也可用）
 *   - GET  /admin/shield/stats    按日 × 类别聚合 + Top 攻击源 IP（查询时聚合）
 *   - GET  /admin/shield/events   拦截明细（JSONL，时间/类别/IP 过滤）
 *   - POST /admin/shield/prune    手动清理拦截明细（保留期外）
 *   - POST /admin/logs/prune      手动清理访问日志（保留期外）
 * 页面结构：实时计数卡 → 按日趋势图（Canvas 柱状）+ 类别分布 → Top IP → 明细表。
 * prune 未开启警告由全局常驻置顶横幅承载（main.js renderPruneBanner，登录/刷新后经
 * GET /admin/warnings 拉取），本页不再重复展示。
 * 挂载到全局命名空间 window.Rock.views.waf。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const debounce = Rock.util.debounce;
  const fmtDate = Rock.util.fmtDate;
  const fmtDateTime = Rock.util.fmtDateTime;
  const fmtInt = Rock.util.fmtInt;
  const fmtBytes = Rock.util.fmtBytes;
  const truncate = Rock.util.truncate;
  const store = Rock.state.store;
  const api = Rock.api;
  const toast = Rock.ui.toast;
  const confirmDialog = Rock.ui.confirmDialog;
  const skeletonHTML = Rock.ui.skeletonHTML;
  const noteUpdated = Rock.ui.noteUpdated;

  // ── 拦截类别（与 plugins/shield/block_type.go 数值稳定约定一致）────────
  const BLOCK_TYPES = [
    [1, 'IP黑名单'], [2, '限流'], [3, '方法不允许'], [4, '请求体超限'], [5, '风险路径'],
    [6, '路径遍历'], [7, 'SQL注入'], [8, 'XSS'], [9, '爬虫UA'], [10, '路径规则'],
  ];
  function typeName(bt) {
    bt = Number(bt) || 0;
    for (let i = 0; i < BLOCK_TYPES.length; i++) {
      if (BLOCK_TYPES[i][0] === bt) return BLOCK_TYPES[i][1];
    }
    return '未知';
  }

  // ── 页内私有状态（查询条件 / 明细展开）──────────────────────────────
  const wafQuery = { fromDate: '', fromTime: '', toDate: '', toTime: '', blockType: '', clientIP: '' };
  let statsDays = 7;
  let eventsExpanded = {};

  function today() { return fmtDate(new Date()); }
  function qFrom() { return (wafQuery.fromDate || today()) + 'T' + (wafQuery.fromTime || '00:00'); }
  function qTo() { return (wafQuery.toDate || today()) + 'T' + (wafQuery.toTime || '23:59'); }

  // ── 数据加载 ────────────────────────────────────────────────────────

  // 实时计数：DB 未配置也返回（counter 在内存），失败仅置错误标记
  async function loadMetrics() {
    try {
      store.wafMetrics = await api.get('/admin/shield/metrics');
      store.wafMetricsError = null;
    } catch (e) {
      store.wafMetrics = store.wafMetrics || null;
      store.wafMetricsError = e.message || '加载失败';
    }
  }

  // 聚合统计 + 明细（DB 未配置时 503 → 置 wafDbOff 展示降级态）
  async function loadStats() {
    try {
      store.wafStats = await api.get('/admin/shield/stats?days=' + statsDays + '&top=10');
      store.wafStatsError = null;
    } catch (e) {
      store.wafStats = null;
      store.wafStatsError = e.message || '加载失败';
    }
  }

  async function loadEvents() {
    const params = new URLSearchParams();
    params.set('from', qFrom());
    params.set('to', qTo());
    if (wafQuery.blockType) params.set('block_type', wafQuery.blockType);
    if (wafQuery.clientIP) params.set('client_ip', wafQuery.clientIP);
    try {
      const txt = await api.text('/admin/shield/events?' + params.toString());
      store.wafEvents = parseNdjson(txt);
      store.wafEventsError = null;
      eventsExpanded = {};
      noteUpdated();
    } catch (e) {
      store.wafEvents = [];
      eventsExpanded = {};
      store.wafEventsError = e.message || '加载失败';
    }
  }

  async function load(opts) {
    const host = $('#page-waf');
    if (!store.wafLoaded && host && !host.innerHTML.trim()) {
      host.innerHTML = skeletonHTML(5);
    }
    await Promise.all([loadMetrics(), loadStats(), loadEvents()]);
    store.wafLoaded = true;
    render();
  }

  // NDJSON 按行解析（坏行容错跳过，与 logs 页同思路）
  function parseNdjson(txt) {
    const out = [];
    const lines = String(txt || '').split('\n');
    for (let i = 0; i < lines.length; i++) {
      const t = lines[i].trim();
      if (!t) continue;
      try { out.push(JSON.parse(t)); } catch (e) { /* 跳过坏行 */ }
    }
    return out;
  }

  // ── 渲染 ────────────────────────────────────────────────────────────

  function metricTilesHTML() {
    const m = store.wafMetrics;
    if (!m) return '<div class="empty">实时计数加载中…</div>';
    const byType = m.by_type || {};
    const chips = Object.keys(byType).sort((a, b) => byType[b] - byType[a]).map(k =>
      '<span class="waf-chip"><b>' + esc(k) + '</b> ' + fmtInt(byType[k]) + '</span>'
    ).join('');
    return '<div class="waf-tiles">' +
      '<div class="waf-tile"><div class="waf-tile-v">' + fmtInt(m.total) + '</div><div class="waf-tile-k">近 1 分钟拦截</div></div>' +
      '<div class="waf-tile"><div class="waf-tile-v">' + fmtInt(m.written) + '</div><div class="waf-tile-k">累计落库</div></div>' +
      '<div class="waf-tile"><div class="waf-tile-v">' + fmtInt(m.dropped) + '</div><div class="waf-tile-k">累计丢弃（通道满降级）</div></div>' +
      '</div>' +
      (chips ? '<div class="waf-chips">' + chips + '</div>' : '<div class="empty">窗口内暂无拦截</div>');
  }

  // stats.daily 为按日 × 类别明细行，前端透视成"日 → 总量"与"类别 → 总量"
  function pivotDaily() {
    const days = {}, types = {};
    (store.wafStats && store.wafStats.daily || []).forEach(row => {
      const d = String(row.day || '');
      const c = Number(row.cnt) || 0;
      days[d] = (days[d] || 0) + c;
      const bt = Number(row.block_type) || 0;
      types[bt] = (types[bt] || 0) + c;
    });
    // 补齐日期空档（无拦截的日期画 0，趋势不失真）
    const sorted = [];
    if (Object.keys(days).length) {
      const min = Object.keys(days).sort()[0];
      const max = Object.keys(days).sort().pop();
      let cur = new Date(min + 'T00:00:00');
      const end = new Date(max + 'T00:00:00');
      while (cur <= end) {
        const k = fmtDate(cur);
        sorted.push({ day: k, cnt: days[k] || 0 });
        cur.setDate(cur.getDate() + 1);
      }
    }
    const typeRows = Object.keys(types).map(bt => ({ bt: Number(bt), cnt: types[bt] }))
      .sort((a, b) => b.cnt - a.cnt);
    return { daily: sorted, types: typeRows };
  }

  function statsHTML() {
    if (store.wafStatsError) {
      return '<div class="empty">' + esc(store.wafStatsError) + '</div>';
    }
    if (!store.wafStats) return '<div class="empty">统计加载中…</div>';
    const s = store.wafStats;
    const p = pivotDaily();
    const typeRows = p.types.map(t =>
      '<tr><td><span class="status status-warn">' + esc(typeName(t.bt)) + '</span></td>' +
      '<td class="mono">' + fmtInt(t.cnt) + '</td></tr>'
    ).join('');
    return '<div class="waf-stats-grid">' +
      '<div class="waf-stats-left">' +
      '<div class="card-sub">近 ' + esc(String(s.days)) + ' 天合计 <b>' + fmtInt(s.total) + '</b> 次拦截</div>' +
      '<div class="chart-box"><canvas id="waf-daily-chart"></canvas></div>' +
      (p.daily.length < 2 ? '<div class="empty">暂无按日数据</div>' : '') +
      '</div>' +
      '<div class="waf-stats-right">' +
      '<div class="card-sub">类别分布（近 ' + esc(String(s.days)) + ' 天）</div>' +
      (typeRows
        ? '<div class="table-wrap" style="max-height:260px"><table class="table"><thead><tr><th>类别</th><th>拦截次数</th></tr></thead><tbody>' + typeRows + '</tbody></table></div>'
        : '<div class="empty">暂无拦截记录</div>') +
      '</div></div>';
  }

  function topIPHTML() {
    if (store.wafStatsError || !store.wafStats) return '';
    const rows = store.wafStats.top_ips || [];
    if (!rows.length) return '';
    return '<div class="card"><div class="card-title">Top 攻击源 IP <span class="card-sub">近 ' + esc(String(store.wafStats.days)) + ' 天</span></div>' +
      '<div class="table-wrap" style="max-height:280px"><table class="table"><thead><tr><th>IP</th><th>拦截次数</th></tr></thead><tbody>' +
      rows.map(r =>
        '<tr><td class="mono">' + esc(r.client_ip || '') + '</td>' +
        '<td class="mono">' + fmtInt(Number(r.cnt) || 0) + '</td></tr>'
      ).join('') +
      '</tbody></table></div></div>';
  }

  // 明细行展开详情（平铺全部字段，extra 为 JSON 串单独解析展示）
  function eventDetailHTML(r) {
    const items = [
      ['链路 ID', r.trace_id],
      ['原始 URL', r.raw_url],
      ['User-Agent', r.user_agent],
      ['Host', r.host],
      ['命中规则', r.rule_hit],
      ['请求体积', fmtBytes(Number(r.req_bytes) || 0)],
    ];
    if (r.extra) {
      try {
        const ex = JSON.parse(r.extra);
        if (ex.referer) items.push(['Referer', ex.referer]);
        if (ex.x_forwarded_for) items.push(['X-Forwarded-For', ex.x_forwarded_for]);
      } catch (e) { /* extra 非法 JSON 忽略 */ }
    }
    return '<div class="detail-grid">' + items.map(it =>
      '<div class="detail-item"><span class="k">' + esc(it[0]) + '：</span><span class="v">' + esc(it[1] === '' || it[1] == null ? '—' : it[1]) + '</span></div>'
    ).join('') + '</div>';
  }

  function expKey(r) { return (r.time || '') + '|' + (r.trace_id || '') + '|' + (r.client_ip || ''); }

  function eventRowHTML(r) {
    const expanded = !!eventsExpanded[expKey(r)];
    const st = Number(r.status_code) || 0;
    return '<tr class="log-row" data-act="waf-expand" data-idx="' + esc(expKey(r)) + '">' +
      '<td class="mono" title="' + esc(r.time) + '">' + esc(fmtDateTime(r.time)) + '</td>' +
      '<td><span class="status status-warn">' + esc(typeName(r.block_type)) + '</span></td>' +
      '<td class="mono">' + esc(r.client_ip || '') + '</td>' +
      '<td><span class="method method-' + esc(String(r.method || '').toLowerCase()) + '">' + esc(r.method || '') + '</span></td>' +
      '<td class="log-path" title="' + esc(r.raw_url || r.path) + '">' + esc(truncate(r.raw_url || r.path, 50)) + '</td>' +
      '<td><span class="status status-red">' + (st || '-') + '</span></td>' +
      '<td class="row-arrow">' + (expanded ? '▾' : '▸') + '</td>' +
      '</tr>' +
      (expanded ? '<tr class="log-detail-row"><td colspan="7">' + eventDetailHTML(r) + '</td></tr>' : '');
  }

  function eventsHTML() {
    if (store.wafEventsError) {
      return '<div class="empty">' + esc(store.wafEventsError) + '</div>';
    }
    const rows = store.wafEvents || [];
    if (!rows.length) return '<div class="empty">所选条件无拦截明细</div>';
    const shown = rows.slice(0, 1000);
    return '<div class="table-wrap" style="max-height:520px">' +
      '<table class="table"><thead><tr>' +
      '<th>时间</th><th>类别</th><th>来源 IP</th><th>方法</th><th>URL</th><th>状态</th><th style="width:28px"></th>' +
      '</tr></thead><tbody>' + shown.map(eventRowHTML).join('') + '</tbody></table></div>' +
      (rows.length >= 1000 ? '<div class="form-hint" style="margin-top:8px">已达 1000 条展示上限，请收窄时间范围或筛选条件。</div>' : '');
  }

  const TYPE_OPTIONS = [['', '类别：全部']].concat(BLOCK_TYPES.map(t => [String(t[0]), t[1]]));

  function render() {
    const host = $('#page-waf');
    if (!host) return;
    if (!wafQuery.fromDate) wafQuery.fromDate = today();
    if (!wafQuery.fromTime) wafQuery.fromTime = '00:00';
    if (!wafQuery.toDate) wafQuery.toDate = today();
    if (!wafQuery.toTime) wafQuery.toTime = '23:59';
    host.innerHTML =
      Rock.comp.head.headHTML({
        title: '拦截统计',
        desc: 'WAF 拦截事件监控：实时计数、按日趋势、Top 攻击源与明细追溯（拦截与放行请求分开记录，互不关联）',
        actions:
          '<button class="btn btn-sm" data-act="waf-reload">⟳ 手动刷新</button>' +
          '<button class="btn btn-sm" data-act="waf-prune-events">清理拦截明细</button>' +
          '<button class="btn btn-sm" data-act="waf-prune-logs">清理访问日志</button>',
      }) +
      '<div class="card"><div class="card-title">实时拦截 <span class="card-sub">内存滑动窗口（近 1 分钟），无需查库</span></div>' +
      metricTilesHTML() + '</div>' +
      '<div class="card"><div class="card-title">按日趋势' +
      '<span class="card-sub">查询时聚合（近 ' + statsDays + ' 天）</span>' +
      '<select class="select select-sm" id="waf-days" style="margin-left:8px">' +
      Rock.comp.select.options([['7', '近 7 天'], ['14', '近 14 天'], ['30', '近 30 天'], ['90', '近 90 天']], String(statsDays)) +
      '</select></div>' +
      statsHTML() + '</div>' +
      topIPHTML() +
      '<div class="card">' +
      '<div class="log-toolbar">' +
      '<div class="tool-group"><span class="muted">开始</span>' +
      '<input type="date" class="input input-sm" id="waf-from-date" value="' + esc(wafQuery.fromDate) + '">' +
      '<input type="time" class="input input-sm" id="waf-from-time" value="' + esc(wafQuery.fromTime) + '">' +
      '</div>' +
      '<div class="tool-group"><span class="muted">结束</span>' +
      '<input type="date" class="input input-sm" id="waf-to-date" value="' + esc(wafQuery.toDate) + '">' +
      '<input type="time" class="input input-sm" id="waf-to-time" value="' + esc(wafQuery.toTime) + '">' +
      '</div>' +
      '<select class="select select-sm" id="waf-block-type">' + Rock.comp.select.options(TYPE_OPTIONS, wafQuery.blockType) + '</select>' +
      '<input class="input input-sm" id="waf-client-ip" placeholder="来源 IP 精确匹配" style="width:160px" value="' + esc(wafQuery.clientIP) + '">' +
      '<button class="btn btn-sm btn-primary" data-act="waf-query">查询</button>' +
      '<button class="btn btn-sm btn-text" data-act="waf-reset">重置</button>' +
      '</div>' +
      '<div id="waf-events-wrap">' + eventsHTML() + '</div>' +
      '</div>';

    drawDailyChart();

    // 统计天数切换：改即拉
    const daysSel = $('#waf-days');
    if (daysSel) daysSel.addEventListener('change', () => {
      statsDays = Number(daysSel.value) || 7;
      loadStats().then(render);
    });

    // 明细条件：变更即查询（防抖，与日志页一致）
    const syncTime = debounce(() => {
      wafQuery.fromDate = $('#waf-from-date').value;
      wafQuery.fromTime = $('#waf-from-time').value;
      wafQuery.toDate = $('#waf-to-date').value;
      wafQuery.toTime = $('#waf-to-time').value;
      queryEvents();
    }, 300);
    [$('#waf-from-date'), $('#waf-from-time'), $('#waf-to-date'), $('#waf-to-time')].forEach(el => el.addEventListener('change', syncTime));

    const btSel = $('#waf-block-type');
    btSel.addEventListener('change', () => {
      wafQuery.blockType = btSel.value;
      queryEvents();
    });
    const ipInput = $('#waf-client-ip');
    ipInput.addEventListener('input', debounce(() => {
      wafQuery.clientIP = ipInput.value.trim();
      queryEvents();
    }, 400));
  }

  // ── Canvas 按日柱状图（颜色取 CSS 变量，主题自适应）─────────────────
  function drawDailyChart() {
    const canvas = $('#waf-daily-chart');
    if (!canvas) return;
    const container = canvas.parentElement;
    const W = container.clientWidth;
    const H = container.clientHeight;
    if (!W || !H) return;
    const cssVar = Rock.comp.chart.cssVar;
    const dpr = window.devicePixelRatio || 1;
    canvas.width = W * dpr;
    canvas.height = H * dpr;
    canvas.style.width = W + 'px';
    canvas.style.height = H + 'px';
    const ctx = canvas.getContext('2d');
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, W, H);
    const data = pivotDaily().daily;
    if (data.length < 2) {
      ctx.fillStyle = cssVar('--text-2');
      ctx.font = '12px sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText('等待拦截数据…', W / 2, H / 2);
      return;
    }
    const padL = 46, padR = 12, padT = 14, padB = 26;
    const iw = W - padL - padR;
    const ih = H - padT - padB;
    if (iw <= 0 || ih <= 0) return;
    let max = 10;
    data.forEach(p => { if (p.cnt > max) max = p.cnt; });
    max = max * 1.15;
    const n = data.length;
    const bw = Math.max(2, (iw / n) * 0.6);
    // 横向网格 + Y 轴刻度
    ctx.strokeStyle = cssVar('--text-2') + '66';
    ctx.fillStyle = cssVar('--text-2');
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
      ctx.fillText(v >= 1000 ? fmtInt(v) : String(Math.round(v)), padL - 6, y + 4);
    }
    // 柱体 + X 轴日期刻度（首/中/尾）
    ctx.textAlign = 'center';
    [0, 0.5, 1].forEach(f => {
      const idx = Math.min(n - 1, Math.round((n - 1) * f));
      const x = padL + ((idx + 0.5) / n) * iw;
      ctx.fillText(String(data[idx].day).slice(5), x, H - 8);
    });
    const primary = cssVar('--primary');
    data.forEach((p, i) => {
      const x = padL + ((i + 0.5) / n) * iw - bw / 2;
      const h = (p.cnt / max) * ih;
      ctx.fillStyle = primary;
      ctx.globalAlpha = p.cnt > 0 ? 0.85 : 0.25; // 零值日淡柱，日期对齐不失真
      ctx.fillRect(x, padT + ih - h, bw, Math.max(p.cnt > 0 ? 1 : 0, h));
    });
    ctx.globalAlpha = 1;
  }

  // ── 交互动作 ────────────────────────────────────────────────────────

  // 明细查询（读工具栏输入，时间非法直接提示）
  async function queryEvents() {
    wafQuery.fromDate = $('#waf-from-date').value || today();
    wafQuery.fromTime = $('#waf-from-time').value || '00:00';
    wafQuery.toDate = $('#waf-to-date').value || today();
    wafQuery.toTime = $('#waf-to-time').value || '23:59';
    if (qFrom() > qTo()) {
      toast('开始时间不能晚于结束时间', 'error');
      return;
    }
    await loadEvents();
    const wrap = $('#waf-events-wrap');
    if (wrap) wrap.innerHTML = eventsHTML();
  }

  // 重置明细条件（时间回当天全天）
  async function resetFilter() {
    wafQuery.fromDate = today();
    wafQuery.fromTime = '00:00';
    wafQuery.toDate = today();
    wafQuery.toTime = '23:59';
    wafQuery.blockType = '';
    wafQuery.clientIP = '';
    render();
    await loadEvents();
    const wrap = $('#waf-events-wrap');
    if (wrap) wrap.innerHTML = eventsHTML();
  }

  // 行展开 / 收起（key = time|trace_id|ip，跟随行不错位）
  function toggleExpand(key) {
    if (!key) return;
    eventsExpanded[key] = !eventsExpanded[key];
    const wrap = $('#waf-events-wrap');
    if (wrap) wrap.innerHTML = eventsHTML();
  }

  // 手动清理（二次确认；days 缺省用后端配置的保留天数）
  async function pruneEvents() {
    const ok = await confirmDialog({
      title: '清理拦截明细',
      message: '将删除保留期（SHIELD_EVENT_RETENTION_DAYS，默认 90 天）之外的拦截明细，操作不可撤销。确认执行？',
      confirmText: '确认清理',
      danger: true,
    });
    if (!ok) return;
    try {
      const r = await api.post('/admin/shield/prune')({});
      toast('已清理 ' + (fmtInt(Number(r && r.deleted) || 0)) + ' 条拦截明细', 'success');
    } catch (e) {
      toast('清理失败：' + e.message, 'error');
    }
  }

  async function pruneLogs() {
    const ok = await confirmDialog({
      title: '清理访问日志',
      message: '将删除保留期（OBS_LOG_RETENTION_DAYS，默认 7 天）之外的访问日志记录，操作不可撤销。确认执行？',
      confirmText: '确认清理',
      danger: true,
    });
    if (!ok) return;
    try {
      const r = await api.post('/admin/logs/prune')({});
      toast('已清理 ' + (fmtInt(Number(r && r.deleted) || 0)) + ' 条访问日志', 'success');
    } catch (e) {
      toast('清理失败：' + e.message, 'error');
    }
  }

  window.Rock.views.waf = {
    load,
    render,
    drawDailyChart,
    queryEvents,
    resetFilter,
    toggleExpand,
    pruneEvents,
    pruneLogs,
    typeName,
  };
})();
