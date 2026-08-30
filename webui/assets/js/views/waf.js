/* ==========================================================================
 * RockSys 管控控制台 - views/waf.js WAF 拦截统计页
 * 数据来源（admin API）：
 *   - GET  /admin/shield/metrics  近 1 分钟实时计数（内存滑动窗口，DB 未配置也可用）
 *   - GET  /admin/shield/stats    按日 × 类别聚合 + Top 攻击源 IP（查询时聚合）
 *   - GET  /admin/shield/events   拦截明细（JSONL，时间/类别/IP 过滤；行含 in_blacklist 标记）
 *   - POST /admin/shield/prune    手动清理拦截明细（保留期外）
 *   - POST /admin/shield/blacklist/ban 行内「IP封禁」（操作列/详情弹层共用弹窗，IP_BLACKLIST_PLAN §3.5）
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

  // 拦截类别与名称映射集中定义于 state.js（WAF 领域数据，与黑白名单子视图共享）
  const BLOCK_TYPES = Rock.state.BLOCK_TYPES;
  const typeName = Rock.state.blockTypeName;

  // ── 页内私有状态（统计天数 / 明细筛选栏 / 明细表实例）───────────────
  let statsDays = 7;

  const dateRange = Rock.comp.dateRange;
  function today() { return dateRange.today(); }
  function qFrom(s) { return dateRange.from(s || eventsBar.state()); }
  function qTo(s) { return dateRange.to(s || eventsBar.state()); }

  // 拦截明细类别筛选：排除 0（0=其他仅黑名单表存储兜底，拦截事件不会落 0，查询语境亦不提供）
  const TYPE_OPTIONS = [['', '类别：全部']].concat(
    BLOCK_TYPES.filter(t => t[0] !== 0).map(t => [String(t[0]), t[1]])
  );

  // 明细筛选栏：时间范围（dateRange 仅完整区间即改即查）+ 类别 + 来源 IP；查询/重置按钮留在视图
  const eventsBar = Rock.comp.filterBar.create({
    ns: 'waf-events-filter',
    live: true,
    onQuery: function () { eventsTable.go(1); queryEvents(); }, // 条件变更回第 1 页再查
    fields: [
      { type: 'dateRange', key: '', default: { fromDate: today(), fromTime: '00:00', toDate: today(), toTime: '23:59' } },
      { type: 'select', key: 'blockType', options: TYPE_OPTIONS },
      { type: 'text', key: 'clientIP', placeholder: '来源 IP 精确匹配', width: '160px' },
    ],
  });

  // 明细表（服务端分页：limit/offset 由后端执行，总数经 X-Total-Count 回传）+ 行详情弹层
  const eventsTable = Rock.comp.dataTable.create({
    ns: 'waf-events',
    columns: [
      { key: 'time', label: '时间', cls: 'mono', render: r => esc(fmtDateTime(r.time)) },
      { key: 'block_type', label: '类别', render: r => '<span class="status status-warn">' + esc(typeName(r.block_type)) + '</span>' },
      { key: 'client_ip', label: '来源 IP', cls: 'mono' },
      { key: 'method', label: '方法', render: r => '<span class="method method-' + esc(String(r.method || '').toLowerCase()) + '">' + esc(r.method || '') + '</span>' },
      { key: 'raw_url', label: 'URL', render: r => { const u = r.raw_url || r.path || ''; return '<span class="log-path" title="' + esc(u) + '">' + esc(truncate(u, 50)) + '</span>'; } },
      { key: 'status_code', label: '状态', render: r => '<span class="status status-red">' + (Number(r.status_code) || '-') + '</span>' },
      { key: 'ban_op', label: '操作', width: '96px', render: r => banBtnHTML(r) },
    ],
    rowKey: expKey, // time|trace_id|client_ip
    detail: {
      title: () => '攻击拦截明细',
      // 详情弹层 footer 注入「IP封禁」（与表格操作列共用同一封禁弹窗，避免双份实现）
      actions: [{ label: 'IP封禁', className: 'btn-primary', onClick: r => openBanModal(r) }],
      fields: [
        { key: 'time', label: '时间', render: r => esc(fmtDateTime(r.time)) },
        { key: 'trace_id', label: '链路 ID' },
        { key: 'client_ip', label: '来源 IP' },
        { key: 'method', label: '方法' },
        { key: 'status_code', label: '状态码' },
        { key: 'host', label: 'Host' },
        { key: 'rule_hit', label: '命中规则' },
        { key: 'req_bytes', label: '请求体积', render: r => esc(fmtBytes(Number(r.req_bytes) || 0)) },
        { key: 'raw_url', label: '原始 URL', pre: true, copy: true },
        { key: 'user_agent', label: 'User-Agent', pre: true, copy: true },
        { key: 'extra_referer', label: 'Referer', render: r => esc(extraField(r, 'referer')) },
        { key: 'extra_xff', label: 'X-Forwarded-For', render: r => esc(extraField(r, 'x_forwarded_for')) },
      ],
    },
    paging: { mode: 'server', pageSize: 20 },
    emptyText: '所选条件无拦截明细',
    onPaging: function () { queryEvents(); }, // 翻页/跳页/改条数：按新 limit/offset 重新拉数
  });

  // extra 为 JSON 串：单独取字段展示（非法 JSON 忽略）
  function extraField(r, key) {
    if (!r.extra) return '';
    try { const ex = JSON.parse(r.extra); return ex[key] == null ? '' : String(ex[key]); } catch (e) { return ''; }
  }

  function expKey(r) { return (r.time || '') + '|' + (r.trace_id || '') + '|' + (r.client_ip || ''); }

  // ── 数据加载 ────────────────────────────────────────────────────────

  // 实时计数：DB 未配置也返回（counter 在内存），失败仅置错误标记
  async function loadMetrics(opts) {
    opts = opts || {};
    try {
      store.wafMetrics = await api.get('/admin/shield/metrics');
      store.wafMetricsError = null;
    } catch (e) {
      store.wafMetrics = store.wafMetrics || null;
      store.wafMetricsError = e.message || '加载失败';
      if (!opts.silent && e.status !== 0) toast('拦截统计加载失败：' + e.message, 'error');
    }
  }

  // 聚合统计 + 明细（DB 未配置时 503 → 置 wafDbOff 展示降级态）
  async function loadStats(opts) {
    opts = opts || {};
    try {
      store.wafStats = await api.get('/admin/shield/stats?days=' + statsDays + '&top=' + Rock.views.topIPs.topN());
      Rock.views.topIPs.setData(store.wafStats);
      store.wafStatsError = null;
    } catch (e) {
      store.wafStats = null;
      Rock.views.topIPs.setData(null);
      store.wafStatsError = e.message || '加载失败';
      // 503 = 防护/DB 未启用，属降级引导态（页内有引导卡片），不弹 toast
      if (!opts.silent && e.status !== 0 && e.status !== 503) toast('攻击拦截统计加载失败：' + e.message, 'error');
    }
  }

  async function loadEvents(opts) {
    opts = opts || {};
    const s = eventsBar.state();
    const st = eventsTable.state();
    const params = new URLSearchParams();
    params.set('from', qFrom(s));
    params.set('to', qTo(s));
    if (s.blockType) params.set('block_type', s.blockType);
    if (s.clientIP) params.set('client_ip', s.clientIP);
    params.set('limit', String(st.pageSize));
    params.set('offset', String(st.offset));
    try {
      const r = await api.textMeta('/admin/shield/events?' + params.toString());
      store.wafEvents = parseNdjson(r.text);
      store.wafEventsTotal = r.total;
      store.wafEventsError = null;
      noteUpdated();
    } catch (e) {
      store.wafEvents = [];
      store.wafEventsTotal = 0;
      store.wafEventsError = e.message || '加载失败';
      if (!opts.silent && e.status !== 0 && e.status !== 503) toast('攻击拦截明细加载失败：' + e.message, 'error');
    }
  }

  async function load(opts) {
    opts = opts || {};
    const host = $('#page-waf');
    if (!store.wafLoaded && host && !host.innerHTML.trim()) {
      host.innerHTML = skeletonHTML(5);
    }
    await Promise.all([loadMetrics(opts), loadStats(opts), loadEvents(opts)]);
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
        ? '<div class="table-wrap"><table class="table"><thead><tr><th>类别</th><th>拦截次数</th></tr></thead><tbody>' + typeRows + '</tbody></table></div>'
        : '<div class="empty">暂无拦截记录</div>') +
      '</div></div>';
  }


  // 明细表渲染：表格 + 分页栏交给 dataTable 实例（server 模式喂总数）；错误态由视图兜底
  function eventsHTML() {
    if (store.wafEventsError) {
      return '<div class="empty">' + esc(store.wafEventsError) + '</div>';
    }
    return eventsTable.html(store.wafEvents || [], { total: store.wafEventsTotal || 0 });
  }

  function renderEventsWrap() {
    const wrap = $('#waf-events-wrap');
    if (wrap) wrap.innerHTML = eventsHTML();
  }

  // 行详情弹层（data-key = time|trace_id|ip 回查行）
  function openEventDetail(el) {
    const key = el.getAttribute('data-key') || '';
    const row = (store.wafEvents || []).find(r => expKey(r) === key);
    if (row) eventsTable.onDetail(row);
  }

  // ── 行内「IP封禁」（IP_BLACKLIST_PLAN §3.5：表格操作列 + 详情弹层共用同一弹窗）──

  // 操作列按钮：已在黑名单的行置灰 + tooltip（与攻击源 TOP「在黑名单不可选」同语义）；
  // client_ip/block_type 写入 data 属性属 col.render 显式信任边界，须 esc 转义
  function banBtnHTML(r) {
    const ip = String(r.client_ip || '');
    if (!ip) return '<span class="muted">—</span>';
    if (r.in_blacklist) {
      return '<button class="btn btn-sm" disabled title="该 IP 已在黑名单，无需重复封禁（可到「黑白名单」页签查看/管理该条目）">IP封禁</button>';
    }
    return '<button class="btn btn-sm" data-act="waf-events-ban" data-ip="' + esc(ip) + '" data-bt="' +
      esc(String(r.block_type == null ? '' : r.block_type)) + '">IP封禁</button>';
  }

  // 弹窗打开时单查该 IP 的黑名单记录（复用列表接口，取 ip 完全相等行）：
  // 得 warn_times / 软删（deleted_at）/ 过期（expires_at）状态——注意 expires_at 为 NULL 时
  // JSON 可能缺键，判空须容忍「缺键」与「空串」两种形态
  async function fetchBanStatus(ip) {
    try {
      const r = await api.get('/admin/shield/blacklist?ip=' + encodeURIComponent(ip) + '&limit=5');
      const rows = (r && r.rows) || [];
      const hit = rows.find(x => String(x.ip || '') === ip);
      if (!hit) return { exists: false, warnTimes: 0, active: false, history: false };
      const deleted = !!hit.deleted_at; // NULL → 缺键/空值均 falsy
      const expStr = hit.expires_at == null ? '' : String(hit.expires_at);
      const expired = expStr !== '' && new Date(expStr).getTime() < Date.now();
      return {
        exists: true,
        warnTimes: Number(hit.warn_times) || 0,
        active: !deleted && !expired,
        history: deleted || expired, // 软删/过期：提交后恢复原条目（决策 10）
      };
    } catch (e) {
      // 查询失败不阻断封禁（弹窗内提示状态未知），但服务端报错仍须弹统一 error toast
      if (e.status !== 0) toast('封禁状态查询失败：' + (e.message || '未知错误') + '，弹窗内将按状态未知处理', 'error');
      return null;
    }
  }

  // 封禁弹窗（复用 openModal）：字段齐全，来源 IP 只读、理由预填、类别下拉（BLOCK_TYPES 同源）
  // 默认 11、时长单选、效果说明；提交走专用 ban 端点
  async function openBanModal(row) {
    const ip = String((row && row.client_ip) || '');
    if (!ip) return;
    if (row && row.in_blacklist) {
      toast('该 IP 已在黑名单，无需重复封禁。可到「黑白名单」页签查看/管理该条目', 'warn');
      return;
    }
    const btName = typeName(row && row.block_type);
    // 纵向表单布局（form-row/label/hint）：不复用 detail-grid（键值网格列宽 ~200px，
    // 装不下宽输入框与长提示，会把弹窗撑出横向滚动条、radio 挤成竖排折行）
    const body =
      '<div class="form-row"><label class="form-label">来源 IP</label>' +
      '<span class="v mono">' + esc(ip) + '</span>' +
      '<span class="form-hint" id="waf-ban-status" style="margin-left:10px">状态查询中…</span></div>' +
      '<div class="form-row"><label class="form-label">封禁理由</label>' +
      '<input class="input" id="waf-ban-title" style="width:100%" maxlength="200" value="人工封禁：' + esc(btName) + '拦截"></div>' +
      '<div class="form-row"><label class="form-label">拉黑原因类别</label>' +
      '<select class="select" id="waf-ban-bt" style="width:220px">' +
      Rock.comp.select.options(BLOCK_TYPES.filter(t => t[0] > 0).map(t => [String(t[0]), t[0] + ' ' + t[1]]), '11') +
      '</select><div class="form-hint" style="margin-top:4px">缺省人工收录，可改选具体拦截类别</div></div>' +
      '<div class="form-row"><label class="form-label">封禁时长</label>' +
      '<label style="margin-right:20px;cursor:pointer"><input type="radio" name="waf-ban-duration" value="24h" checked> 封禁 24 小时</label>' +
      '<label style="cursor:pointer"><input type="radio" name="waf-ban-duration" value="permanent"> 永久封禁</label></div>' +
      '<div class="form-hint" style="line-height:1.8">提交后该 IP 封禁次数 +1；限时封禁累计达 5 次将自动转为永久封禁。</div>' +
      '<div id="waf-ban-err" class="status-red" style="margin-top:8px;display:none;white-space:pre-wrap"></div>';
    const overlay = Rock.ui.openModal({
      title: 'IP封禁',
      body: body,
      width: 480,
      footer: '<button class="btn btn-primary" data-act="waf-ban-submit">确认封禁</button>',
    });

    // 打开时单查状态并回填提示（warn_times / 是否在黑名单 / 历史记录预提示）
    const st = await fetchBanStatus(ip);
    const statusEl = overlay.querySelector('#waf-ban-status');
    if (!statusEl) return; // 弹层已被关闭
    if (!st) {
      statusEl.textContent = '状态查询失败，不影响封禁提交';
    } else if (st.active) {
      statusEl.textContent = '该 IP 当前已在黑名单（封禁次数 ' + st.warnTimes + '），无需重复封禁';
    } else if (st.history) {
      statusEl.textContent = '当前封禁次数：' + st.warnTimes + '。该 IP 有历史封禁记录（已软删/已过期），提交将恢复原条目并累计次数';
    } else {
      statusEl.textContent = '当前封禁次数：' + st.warnTimes + '（暂无生效中的黑名单记录）';
    }

    // 提交：走专用 ban 端点；成功 toast 自动消失 + 关弹层 + 刷新明细；失败常驻三要素
    overlay.addEventListener('click', async e => {
      if (!e.target.closest('[data-act="waf-ban-submit"]')) return;
      const title = overlay.querySelector('#waf-ban-title').value.trim();
      const bt = Number(overlay.querySelector('#waf-ban-bt').value) || 11;
      const durEl = overlay.querySelector('input[name="waf-ban-duration"]:checked');
      const duration = durEl ? durEl.value : '24h';
      const errEl = overlay.querySelector('#waf-ban-err');
      try {
        const r = await api.post('/admin/shield/blacklist/ban')({ ip: ip, title: title, block_type: bt, duration: duration });
        overlay.remove();
        if (r && r.to_permanent) {
          toast('已封禁 ' + ip + '（累计封禁满 5 次，已自动转为永久封禁）', 'success');
        } else {
          toast(duration === 'permanent' ? '已永久封禁 ' + ip : '已封禁 ' + ip + '（24 小时后自动解封）', 'success');
        }
        queryEvents(); // 刷新明细（重新拉取后 in_blacklist 标记与按钮置灰同步更新）
      } catch (err) {
        // 失败统一走 error toast（常驻不自动消失，后端文案已含三要素）；弹窗保持打开，行内提示同步兜底
        const m = '封禁失败：' + (err.message || '未知错误');
        toast(m, 'error');
        errEl.textContent = m;
        errEl.style.display = 'block';
      }
    });
  }

  // ── 渲染 ────────────────────────────────────────────────────────────

  // 页内 Tab：拦截统计 / 黑白名单 / 文件编辑（子视图分别见 blacklist.js / ruleFiles.js）
  let wafActiveTab = 'stats'; // 'stats' | 'iplist' | 'files'

  function tabsHTML() {
    return Rock.comp.tabs.tabsHTML(
      [{ name: 'stats', label: '攻击拦截' }, { name: 'iplist', label: '黑白名单' }, { name: 'files', label: '文件编辑' }],
      wafActiveTab,
      { act: 'waf-tab', nameAttr: 'data-tab' }
    );
  }

  function render() {
    const host = $('#page-waf');
    if (!host) return;
    if (wafActiveTab === 'iplist') {
      Rock.views.blacklist.render(host);
      return;
    }
    if (wafActiveTab === 'files') {
      Rock.views.ruleFiles.render(host);
      return;
    }
    renderStatsPage();
  }

  function renderStatsPage() {
    const host = $('#page-waf');
    if (!host) return;
    host.innerHTML =
      Rock.comp.head.headHTML({
        title: 'WAF安全',
        desc: 'WAF 防护管理：实时计数、按日趋势、Top 攻击源与明细追溯；黑白名单管理（拦截与放行请求分开记录，互不关联）',
        actions:
          '<button class="btn btn-sm" data-act="waf-reload">⟳ 手动刷新</button>' +
          '<button class="btn btn-sm" data-act="waf-prune-events">清理拦截明细</button>' +
          '<button class="btn btn-sm" data-act="waf-prune-logs">清理访问日志</button>',
      }) +
      tabsHTML() +
      '<div class="card"><div class="card-title">实时拦截 <span class="card-sub">内存滑动窗口（近 1 分钟），无需查库</span></div>' +
      metricTilesHTML() + '</div>' +
      '<div class="card"><div class="card-title">按日趋势' +
      '<span class="card-sub">查询时聚合（近 ' + statsDays + ' 天）</span>' +
      '<select class="select select-sm" id="waf-days" style="margin-left:8px">' +
      Rock.comp.select.options([['7', '近 7 天'], ['14', '近 14 天'], ['30', '近 30 天'], ['90', '近 90 天']], String(statsDays)) +
      '</select></div>' +
      statsHTML() + '</div>' +
      Rock.views.topIPs.html() +
      '<div class="card"><div class="card-title">攻击拦截明细 <span class="card-sub">拦截事件逐条追溯，行内可封禁</span></div>' +
      eventsBar.html() +
      '<div class="log-toolbar" style="margin-top:-6px">' +
      '<button class="btn btn-sm btn-primary" data-act="waf-query">查询</button>' +
      '<button class="btn btn-sm btn-text" data-act="waf-reset">重置</button>' +
      '</div>' +
      '<div id="waf-events-wrap">' + eventsHTML() + '</div>' +
      '</div>';

    drawDailyChart();

    Rock.views.topIPs.wire();

    // 统计天数切换：改即拉
    const daysSel = $('#waf-days');
    if (daysSel) daysSel.addEventListener('change', () => {
      statsDays = Number(daysSel.value) || 7;
      loadStats().then(render);
    });

    // 明细筛选栏即改即查（组件内防抖）与分页控件委托（host 持久，只绑一次）
    eventsBar.bind(host);
    if (!eventsTableBound) { eventsTable.bind(host); eventsTableBound = true; }
  }

  // dataTable 分页控件在持久 host 上只绑一次（renderStatsPage 重渲染 innerHTML 不影响委托）
  let eventsTableBound = false;

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

  // 明细查询（state 缺省时从筛选栏收集；时间非法直接提示；页码由调用方决定：
  // 条件变更先 eventsTable.go(1)，翻页回调保持当前页）
  async function queryEvents() {
    const state = eventsBar.collect();
    if (qFrom(state) > qTo(state)) {
      toast('开始时间不能晚于结束时间', 'error');
      return;
    }
    await loadEvents();
    renderEventsWrap();
  }

  // 重置明细条件：回筛选栏默认值（时间当天全天）并触发查询
  function resetFilter() {
    eventsBar.reset();
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

  // 主 Tab 切换：拦截统计 / 黑白名单 / 文件编辑（子视图渲染与 CRUD 下沉各自模块）
  async function ipListSwitchTab(tab) {
    wafActiveTab = ['iplist', 'files'].indexOf(tab) >= 0 ? tab : 'stats';
    if (wafActiveTab === 'iplist') await Rock.views.blacklist.ensureLoaded();
    if (wafActiveTab === 'files') await Rock.views.ruleFiles.ensureLoaded();
    render();
  }

  window.Rock.views.waf = {
    load,
    render,
    drawDailyChart,
    queryEvents,
    resetFilter,
    pruneEvents,
    pruneLogs,
    typeName,
    actions: {
      'waf-reload': function () { load({ manual: true }); },
      'waf-query': function () { queryEvents(); },
      'waf-reset': function () { resetFilter(); },
      'waf-prune-events': function () { pruneEvents(); },
      'waf-prune-logs': function () { pruneLogs(); },
      'waf-events-detail': function (el) { openEventDetail(el); },
      'waf-events-ban': function (el) {
        // 操作列「IP封禁」：回查明细行（data-ip 精确匹配）后打开封禁弹窗
        const ip = el.getAttribute('data-ip') || '';
        const row = (store.wafEvents || []).find(r => String(r.client_ip || '') === ip);
        openBanModal(row || { client_ip: ip, block_type: Number(el.getAttribute('data-bt')) || 0 });
      },
      'waf-tab': function (el) { ipListSwitchTab(el.getAttribute('data-tab') || 'stats'); },
      'waf-iplist-kind': function (el) { Rock.views.blacklist.switchKind(el.getAttribute('data-kind') || 'black'); },
      'waf-iplist-detail': function (el) { Rock.views.blacklist.openDetail(el.getAttribute('data-key')); },
      'waf-iplist-query': function () { Rock.views.blacklist.query(); },
      'waf-iplist-reset': function () { Rock.views.blacklist.reset(); },
      'waf-iplist-reload': function () { Rock.views.blacklist.query(); },
      'waf-iplist-add': function () { Rock.views.blacklist.add(); },
      'waf-iplist-del': function (el) { Rock.views.blacklist.del(el.getAttribute('data-id')); },
      'waf-iplist-restore': function (el) { Rock.views.blacklist.restore(el.getAttribute('data-id')); },
      'waf-iplist-import': function () { Rock.views.blacklist.importRows(); },
      'waf-iplist-sync-file': function () { Rock.views.blacklist.syncFile(); },
    },
  };

  // 注入攻击源卡片协作钩子：Top N 变更/批量加黑成功后重新拉取统计并渲染
  Rock.views.topIPs.bindHooks({ refresh: function () { loadStats().then(render); } });
  // 注入页面上下文：主 Tab HTML 与当前 Tab（黑白名单子视图渲染主 Tab 用）
  Rock.views.blacklist.bindPage({
    tabsHTML: tabsHTML,
    activeTab: function () { return wafActiveTab; },
  });
  // 注入页面上下文：主 Tab HTML 与当前 Tab（文件编辑子视图渲染主 Tab 用）
  Rock.views.ruleFiles.bindPage({
    tabsHTML: tabsHTML,
    activeTab: function () { return wafActiveTab; },
  });
})();
