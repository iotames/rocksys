/* ==========================================================================
 * RockSys 管理控制台 - views/overview.js 概览页
 * 页签「总览」：网关信息横条 + 流量统计区（TRAFFIC_ANALYSIS：时间范围筛选 + 指标瓦片 + 地理位置（并入卡内，
 * 紧接瓦片之下） + 访问/拦截趋势，按需 SQL 聚合、服务端缓存） + 运行状态卡（实时指标 + 资源监控） + HTTP 数据流图（组件节点带开关）
 * + 服务状态总览；页签「小黑屋」：当前在押的限时封禁预览（IP_BLACKLIST_PLAN §3.7）。
 * 依赖 Rock.state / Rock.util / Rock.ui / Rock.api
 * / Rock.comp.{tabs,metrics,componentState,dataflow,chart,dataTable,empty}。
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

  // ── 页签状态：总览 / 小黑屋 ─────────────────────────────────────────
  let ovActiveTab = 'overview'; // 'overview' | 'jail'

  // ── 流量统计区状态（TRAFFIC_ANALYSIS D10/D14：页面局部区块，与运行指标区口径互不替代）──
  let metricsWindow = '1m'; // 实时窗口桶宽（METRICS_WINDOW）：1m/5m/15m/1h，缺省 1m
  let trafficPreset = '24h'; // 当前预设：'24h' | 'today' | '7d' | '30d' | 'custom'
  let trafficQuery = { fromDate: '', fromTime: '00:00', toDate: '', toTime: '23:59' }; // 自定义范围（本地时间）
  let traffic = null; // { summary, series } 拉取结果
  let trafficErr = null; // 行内错误兜底（toast 按 UX 红线在 loadTraffic 里弹）
  let trafficOff = false; // obs 未启用（503 引导态）
  let trafficLoading = false; // 统计拉取中（页面显示加载中提示，刷新时保留旧数据）
  const trafficTimeoutMs = 30000; // 查库聚合是重操作，超时放宽到 30 秒（默认 5 秒易误报）
  // 地理位置卡片状态：scope=世界/中国地图切换，source=访问/总拦截切换（联动地图热力与 Top 排名）
  let geoScope = 'china'; // 'world' | 'china'
  let geoSource = 'access'; // 'access' | 'blocked'
  let geo = null; // 拉取结果 { level, source, geo:[{country|region,cnt}], geo_ready }
  let geoErr = null; // 行内错误兜底（toast 按 UX 红线在 loadGeo 里弹）
  let geoLoading = false; // 地理分布拉取中

  // 小黑屋数据缓存（切页签/静默重载时刷新；拉取失败保留旧数据 + 行内提示）
  let jailRows = [];
  let jailTotal = 0;
  let jailErr = null;

  // 小黑屋表格（client 模式：拉 limit=20 条全量喂入，组件内切片，不分页请求）
  const jailTable = Rock.comp.dataTable.create({
    ns: 'jail',
    columns: [
      { key: 'ip', label: '封禁 IP' },
      { key: 'block_type', label: '封禁原因', render: r => esc(Rock.state.blockTypeName(r.block_type)) },
      { key: 'hit_count', label: '命中次数', render: r => esc(Rock.util.fmtInt(r.hit_count)) },
      { key: 'warn_times', label: '封禁次数', render: r => esc(Rock.util.fmtInt(r.warn_times)) },
      { key: 'created_at', label: '封禁时间（首次）', render: r => esc(Rock.util.fmtDateTime(r.created_at)) },
      // expires_at 为 NULL 即永久封禁，展示「永久」；限时封禁展示解封时间 —
      { key: 'expires_at', label: '解封时间', render: r => esc(r.expires_at ? Rock.util.fmtDateTime(r.expires_at) : '永久') },
    ],
    paging: { mode: 'client', pageSize: 20 },
    emptyText: '小黑屋空空如也',
  });

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
      // 顶栏管理地址经全局 UI 接口供数（页面不直接操作全局栏 DOM）
      Rock.ui.setAdminAddr(store.base.admin);
    } catch (e) {
      store.overviewFailed = !store.baseLoaded && !store.switchesLoaded;
      if (!opts.silent && e.status !== 0 && !e.obsDisabled) {
        toast('概览加载失败：' + e.message, 'error');
      }
    }
    // 系统资源信息（运行时长/CPU/内存）：与指标并行拉取；失败行内降级，且非静默加载时
    // 弹统一 error toast（UX 红线：行内兜底只能与 toast 并存，不能替代；gate baseOk
    // 避免与「概览加载失败」对同一根因双弹）。status=0（网络不可达）走全局横幅，不弹。
    const systemP = api.get('/admin/system').then(
      function (s) { store.system = s; },
      function (e) {
        store.system = null;
        if (baseOk && !opts.silent && e.status !== 0) {
          toast('系统资源信息加载失败：' + e.message + '，可点页内「刷新」重试', 'error');
        }
      }
    );
    if (baseOk) {
      try {
        // system 与 metrics 无依赖，并行拉取缩短首屏（systemP 内部已容错，不会 reject）
        const [m] = await Promise.all([api.get('/admin/metrics?window=' + metricsWindow), systemP]);
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
          if (store.metricsHistory.length > 720) store.metricsHistory.shift(); // 5s×720 = 覆盖 1h 窗口
        }
        noteUpdated();
      } catch (e) {
        if (e.obsDisabled) { store.metricsError = 'obs'; }
        else if (!opts.silent && e.status !== 0) { toast('指标加载失败：' + e.message, 'error'); }
      }
    }
    ensureSampler(); // 实时区 5s 静默采样（METRICS_WINDOW：让窗口曲线有连续样本）
    loadTraffic(opts); // 流量统计区：异步拉取后局部重渲染（不阻塞首屏）
    render();
    // 小黑屋页签随首页静默重载联动刷新（手动刷新/开关操作触发，总览页签不受影响）
    if (ovActiveTab === 'jail') loadJail(opts);
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

  // 页签条：总览 / 小黑屋（点击经全局委托走 'overview-tab' 动作）
  function tabsHTML() {
    return Rock.comp.tabs.tabsHTML(
      [{ name: 'overview', label: '总览' }, { name: 'jail', label: '小黑屋' }],
      ovActiveTab,
      { act: 'overview-tab', nameAttr: 'data-tab' }
    );
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

    host.innerHTML =
      Rock.comp.head.headHTML({
        title: '概览',
        desc: '30 秒完成巡检：网关状态 · 数据流 · 指标 · 组件 · 服务',
        actions: '<button class="btn btn-sm" data-act="overview-reload">⟳ 刷新</button>',
      }) +
      tabsHTML() +
      (ovActiveTab === 'jail' ? jailBodyHTML() : overviewBodyHTML());

    if (ovActiveTab !== 'jail') {
      if (!store.metricsError && store.metrics) drawChart();
      drawTrafficCharts();
    }
  }

  // ── 页签「总览」：原有内容（行为不变）──────────────────────────────
  // 网关信息横条：一行排布关键信息（管理地址在顶栏全局展示，此处不再重复），最右侧「进入全局配置」跳转链接
  function gatewayBarHTML() {
    const b = store.base || {};
    const items = [
      ['监听端口', b.listen || '—'],
      ['转发地址', b.upstream || '—'],
    ].map(it =>
      '<div class="gw-bar-item"><span class="k">' + esc(it[0]) + '</span><span class="v">' + esc(it[1]) + '</span></div>'
    ).join('');
    return '<div class="card hoverable gw-bar" data-act="goto-config" title="点击进入全局配置">' +
      items +
      '<a class="link-like gw-bar-link">进入全局配置 →</a>' +
      '</div>';
  }

  // 资源信息条（METRICS_WINDOW 重排：资源监控并入运行状态卡，消除两卡高度差）：
  // 机器 CPU / 内存进度条 + 进程级占用（数据来自 /admin/system；系统级仅 Linux 可得）。
  function resourceRowsHTML() {
    const sys = store.system;
    let body;
    if (!sys) {
      body = Rock.comp.empty.message({ text: '系统资源信息不可用', padding: '24px 8px' });
    } else {
      const fmtMB = n => Rock.util.fmtInt(Math.round(Number(n) / 1048576));
      const bar = (label, pct, sub) => {
        const p = pct == null ? null : Math.max(0, Math.min(100, pct));
        return '<div class="res-row"><span class="res-label">' + esc(label) + '</span>' +
          '<div class="res-track"><div class="res-fill' + (p != null && p >= 80 ? ' res-hot' : '') + '"' +
          (p != null ? ' style="width:' + p.toFixed(1) + '%"' : ' style="visibility:hidden"') + '></div></div>' +
          '<span class="res-pct">' + (p != null ? p.toFixed(1) + '%' : '—') + '</span></div>' +
          (sub ? '<div class="res-sub">' + sub + '</div>' : '');
      };
      const isLinux = sys.os === 'linux';
      let cpuRow, memRow;
      if (sys.cpu_percent != null) {
        cpuRow = bar('CPU', sys.cpu_percent, '进程占用 ' + (sys.proc_cpu_percent != null ? sys.proc_cpu_percent.toFixed(1) + '%' : '—'));
      } else {
        // null 的两种成因分开说清（文案三要素）：Linux=首次采样未完成（数秒后刷新可见）；
        // 其余平台=不支持系统级采集
        cpuRow = bar('CPU', null, isLinux
          ? '首次采样中（间隔约 3 秒），点「刷新」后显示'
          : '当前平台不支持系统级 CPU 采集');
      }
      if (sys.mem_total != null) {
        const usedPct = sys.mem_total > 0 ? (sys.mem_used / sys.mem_total) * 100 : null;
        memRow = bar('内存', usedPct, esc(fmtMB(sys.mem_used)) + ' MB / ' + esc(fmtMB(sys.mem_total)) + ' MB');
      } else {
        memRow = bar('内存', null, isLinux
          ? '系统级内存读取失败（本机 /proc 受限不可读）'
          : '当前平台不支持系统级内存采集');
      }
      body = cpuRow + memRow +
        '<div class="form-hint" style="margin-top:10px">进程内存 ' + esc(fmtMB(sys.proc_mem_bytes)) +
        ' MB · Goroutines ' + esc(Rock.util.fmtInt(sys.goroutines)) +
        ' · 核心数 ' + esc(Rock.util.fmtInt(sys.num_cpu)) + '</div>';
    }
    return body;
  }

  // ── 流量统计区（TRAFFIC_ANALYSIS）────────────────────────────────
  // 指标口径（与 /admin/obs/traffic/summary 对齐）：请求次数 = req_ok + block_total；
  // req_ok 含静态资源与放行后 4xx/5xx；PV 去静态资源；UV = IP+UA。
  function trafficRangeValue(preset) {
    const now = new Date();
    let from;
    if (preset === '24h') from = new Date(now.getTime() - 24 * 3600 * 1000);
    else if (preset === '7d') from = new Date(now.getTime() - 7 * 24 * 3600 * 1000);
    else if (preset === '30d') from = new Date(now.getTime() - 30 * 24 * 3600 * 1000);
    else if (preset === 'today') from = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    else return null; // custom：用 trafficQuery 组装
    return { from: fmtLocalMin(from), to: fmtLocalMin(now) };
  }
  // 本地时间 → 'YYYY-MM-DDTHH:mm'（服务端 logs 页同款解析口径：按本地时区解析后转 UTC 聚合）
  function fmtLocalMin(d) {
    return d.getFullYear() + '-' + Rock.util.pad2(d.getMonth() + 1) + '-' + Rock.util.pad2(d.getDate()) +
      'T' + Rock.util.pad2(d.getHours()) + ':' + Rock.util.pad2(d.getMinutes());
  }
  function currentTrafficRange() {
    if (trafficPreset === 'custom') {
      if (!trafficQuery.fromDate || !trafficQuery.toDate) return null;
      return { from: trafficQuery.fromDate + 'T' + trafficQuery.fromTime, to: trafficQuery.toDate + 'T' + trafficQuery.toTime };
    }
    return trafficRangeValue(trafficPreset);
  }

  async function loadTraffic(opts) {
    const range = currentTrafficRange();
    if (!range) { trafficErr = '自定义时间范围不完整'; renderTrafficBody(); return; }
    const qs = 'from=' + encodeURIComponent(range.from) + '&to=' + encodeURIComponent(range.to);
    trafficOff = false;
    trafficLoading = true;
    renderTrafficBody(); // 立即给出加载中反馈（已有旧数据时保留旧内容+刷新中角标）
    try {
      const spanMs = new Date(range.to.replace('T', ' ')) - new Date(range.from.replace('T', ' '));
      const bucket = spanMs <= 48 * 3600 * 1000 ? 'hour' : 'day';
      // 查库聚合是重操作，数据量大时耗时秒级，超时放宽到 30 秒（默认 5 秒易误报）
      const [summary, series] = await Promise.all([
        api.get('/admin/obs/traffic/summary?' + qs, trafficTimeoutMs),
        api.get('/admin/obs/traffic/series?' + qs + '&bucket=' + bucket, trafficTimeoutMs),
      ]);
      traffic = { summary, series, bucket };
      trafficErr = null;
    } catch (e) {
      if (e.status === 503) { trafficOff = true; } // obs 未启用：页内引导态（豁免 toast 红线②）
      else {
        trafficErr = e.message || '加载失败';
        if (!opts.silent && e.status !== 0) {
          toast('流量统计加载失败：' + e.message + '，可点页内「刷新」重试；若数据库刚升级请先在「数据库」页完成表结构同步', 'error');
        }
      }
    }
    trafficLoading = false;
    renderTrafficBody();
    loadGeo({ silent: true });
  }

  // 地理位置：按当前 scope/source 拉取（scope 决定聚合 level：中国=province，世界=country），
  // 只重绘地理位置卡片，不打断流量统计其他区块。
  async function loadGeo(opts) {
    if (!traffic) return; // 流量区未就绪（obs 未启用等），不单独拉
    const range = currentTrafficRange();
    if (!range) return;
    const level = geoScope === 'china' ? 'province' : 'country';
    const source = geoSource;
    const stale = function () { // 期间已切换 scope/source → 本次结果已过时，丢弃（由最新请求负责渲染）
      return level !== (geoScope === 'china' ? 'province' : 'country') || source !== geoSource;
    };
    const qs = 'from=' + encodeURIComponent(range.from) + '&to=' + encodeURIComponent(range.to) +
      '&source=' + source + '&level=' + level;
    geoLoading = true;
    try {
      const res = await api.get('/admin/obs/traffic/geo?' + qs, trafficTimeoutMs);
      if (res.level !== level || res.source !== source || stale()) return;
      geo = res;
      geoErr = null;
    } catch (e) {
      if (stale()) return;
      geoErr = e.message || '加载失败';
      if (!opts.silent && e.status !== 0 && e.status !== 503) {
        toast('地理位置分布加载失败：' + geoErr + '，可点页内「刷新」重试', 'error');
      }
    }
    geoLoading = false;
    renderGeoCard();
    maybeGeoToast();
  }

  // D17：依赖 geo 的页面会话内首次进入且 geo 未就绪时弹一次统一警告 toast（sessionStorage 标记防刷屏）
  function maybeGeoToast() {
    if (!geo || geo.geo_ready) return;
    if (sessionStorage.getItem('rock-geo-warned')) return;
    sessionStorage.setItem('rock-geo-warned', '1');
    toast('地理位置数据未加载：未找到 mmdb 文件，统计中地区将显示为「未知」。请下载 GeoLite2 mmdb 放置到 GEOIP_MMDB_DIR 目录（缺省 geoip/）后重启服务生效。下载直链见页内引导卡', 'error');
  }

  function trafficTilesHTML(sm) {
    const dash = '—';
    const fmtRate = r => (r == null ? dash : (r * 100).toFixed(1) + '%');
    const tile = (label, val, sub) =>
      '<div class="metric-tile"><div class="metric-label">' + esc(label) + '</div>' +
      '<div class="metric-value">' + esc(val) + '</div>' +
      (sub ? '<div class="metric-sub form-hint">' + esc(sub) + '</div>' : '') + '</div>';
    const N = Rock.util.fmtInt;
    const n = v => (v == null ? dash : N(v));
    const reqTotal = (sm.req_ok != null && sm.block_total != null) ? sm.req_ok + sm.block_total : null;
    return '<div class="metric-grid">' +
      tile('请求次数', n(reqTotal), '放行 + 拦截') +
      tile('PV', n(sm.req_pv), '去静态资源') +
      tile('UV', n(sm.uv), 'IP + UA 口径') +
      tile('独立 IP', n(sm.ip_all), '') +
      tile('拦截次数', n(sm.block_total), '') +
      tile('攻击 IP', n(sm.attack_ips), '') +
      tile('4xx', n(sm.err4xx), '错误率 ' + fmtRate(sm.err4xx_rate)) +
      tile('4xx 拦截', n(sm.block4xx), '拦截率 ' + fmtRate(sm.block4xx_rate)) +
      tile('5xx', n(sm.err5xx), '错误率 ' + fmtRate(sm.err5xx_rate)) +
      tile('延迟 P50', n(sm.lat_p50), '毫秒 · 范围内精确统计') +
      tile('延迟 P95', n(sm.lat_p95), '毫秒') +
      tile('延迟 P99', n(sm.lat_p99), '毫秒') +
      tile('平均延迟', n(sm.lat_avg), '毫秒') +
      '</div>' +
      // 缓存时长随服务端配置动态展示（OBS_TRAFFIC_CACHE_TTL，0=禁用时不显示）
      (sm.computed_at ? '<div class="form-hint">统计时刻 ' + esc(sm.computed_at.replace('T', ' ').slice(0, 19)) + ' UTC' +
        (sm.cache_ttl_sec ? ' · 服务端缓存 ' + Math.round(sm.cache_ttl_sec / 60) + ' 分钟（可配置）' : '') + '</div>' : '');
  }

  // 地理位置卡片：世界/中国热力地图 + Top 地区排名（scope/source 切换联动地图与排名）
  const GEO_GUIDE = '<div class="empty" style="padding:16px 8px;text-align:left">' +
    '<div><b>地理位置数据未加载</b>：未找到 mmdb 文件，地区统计显示为「未知」。</div>' +
    '<div class="form-hint">下一步：下载 GeoLite2 的 GeoLite2-City.mmdb / GeoLite2-Country.mmdb，' +
    '放置到 GEOIP_MMDB_DIR 目录（缺省 geoip/，或工作目录、~/geoip 任一处），重启服务后生效。</div>' +
    '<div class="form-hint">下载直链（GitHub，P3TERX/GeoLite.mmdb）：<br>' +
    '<a href="https://github.com/P3TERX/GeoLite.mmdb/releases/download/2026.09.07/GeoLite2-City.mmdb" target="_blank" rel="noopener">GeoLite2-City.mmdb</a><br>' +
    '<a href="https://github.com/P3TERX/GeoLite.mmdb/releases/download/2026.09.07/GeoLite2-Country.mmdb" target="_blank" rel="noopener">GeoLite2-Country.mmdb</a></div></div>';

  function geoTogglesHTML() {
    const chip = (act, key, label, cur) =>
      '<button class="btn btn-sm' + (cur === key ? ' btn-primary' : '') + '" data-act="' + act + '" data-key="' + key + '">' + label + '</button>';
    return '<div class="geo-toggles">' +
      chip('geo-scope', 'world', '世界', geoScope) + chip('geo-scope', 'china', '中国', geoScope) +
      '<span class="geo-toggle-gap"></span>' +
      chip('geo-source', 'access', '访问', geoSource) + chip('geo-source', 'blocked', '总拦截', geoSource) +
      '</div>';
  }

  // 排名地区显示名：世界=ISO 码转中文国名（浏览器内建本地化），中国=省名原样
  function geoDisplayName(name) {
    if (!name || name === '未知') return '未知';
    return geoScope === 'world' ? Rock.comp.geoMap.isoToCn(name) : name;
  }

  function geoRankHTML(rows) {
    if (!rows.length) return Rock.comp.empty.message({ text: '所选范围暂无数据' });
    const max = rows[0].cnt || 1;
    const rowsHtml = rows.map(function (g) {
      const name = g.region || g.country || '';
      const pct = Math.max(2, Math.round((g.cnt / max) * 100));
      return '<div class="geo-row"><span class="geo-name" title="' + esc(geoDisplayName(name)) + '">' + esc(geoDisplayName(name)) + '</span>' +
        '<span class="geo-bar"><span style="width:' + pct + '%"></span></span>' +
        '<span class="geo-cnt">' + esc(Rock.util.fmtInt(g.cnt)) + '</span></div>';
    }).join('');
    return '<div class="geo-list">' + rowsHtml + '</div>';
  }

  function geoCardHTML() {
    const srcLabel = geoSource === 'blocked' ? '总拦截' : '访问';
    const scopeLabel = geoScope === 'world' ? '世界' : '中国';
    let body;
    if (!traffic) {
      body = '';
    } else if (geo && geo.geo_ready === false) {
      body = GEO_GUIDE;
    } else if (geoErr && !geo) {
      body = Rock.comp.empty.emptyCard({ text: '地理位置分布加载失败：' + geoErr, br: true,
        action: '<button class="btn btn-sm btn-primary" data-act="geo-retry">重试</button>' });
    } else if (!geo) {
      body = Rock.comp.empty.message({ text: geoLoading ? '地理分布加载中…按范围查库聚合，请稍候' : '加载中…' });
    } else {
      body = '<div class="geo-panels">' +
        '<div class="geo-map-box" style="height:380px"><div id="geo-map" style="width:100%;height:100%"></div></div>' +
        '<div class="geo-rank"><div class="card-title" style="margin-bottom:6px">Top 地区 <span class="card-sub">' +
        srcLabel + '口径 · ' + scopeLabel + '</span></div>' + geoRankHTML(geo.geo || []) + '</div>' +
        '</div>';
    }
    // 并入流量统计卡内的分区样式（同运行状态卡「资源」分区的 border-top 风格）
    return '<div style="border-top:1px solid rgba(127,127,127,.15);margin-top:12px;padding-top:10px"><div class="geo-card-head"><div class="card-title">地理位置 <span class="card-sub">按范围查库聚合 · 地图与排名联动</span></div>' +
      geoTogglesHTML() + '</div>' + body + '</div>';
  }

  // 地理位置卡片局部渲染（切换 scope/source / 拉取完成后调用，不整页重绘）
  function renderGeoCard() {
    const host = $('#page-overview .geo-slot');
    if (!host || ovActiveTab !== 'overview') return;
    const oldMap = $('#geo-map');
    if (oldMap && window.echarts && echarts.getInstanceByDom) echarts.dispose(oldMap); // 旧实例随 DOM 重建而销毁，防泄漏
    host.innerHTML = geoCardHTML();
    drawGeoMap();
  }

  // 地图热力绘制：懒加载注册地图（会话内一次）后喂入当前 geo 数据；
  // 「未知」不进地图（无地理坐标），只留在排名列表。
  function drawGeoMap() {
    if (!geo || geo.geo_ready === false || !$('#geo-map')) return;
    const scope = geoScope;
    Rock.comp.geoMap.ensureMap(scope).then(function () {
      if (!$('#geo-map') || scope !== geoScope || !geo) return; // 期间已切换/重绘
      const data = (geo.geo || []).filter(function (g) {
        const name = g.region || g.country || '';
        return name && name !== '未知';
      }).map(function (g) {
        const name = g.region || g.country || '';
        return { name: scope === 'china' ? (Rock.comp.geoMap.chinaShort[name] || name) : name, value: g.cnt };
      });
      Rock.comp.geoMap.heat($('#geo-map'), scope, data, {
        mode: geoSource === 'blocked' ? '拦截' : '访问',
        fmtName: scope === 'world' ? Rock.comp.geoMap.isoToCn : function (n) { return n; },
      });
    }).catch(function (err) {
      const box = $('#geo-map');
      if (box) box.innerHTML = '<div class="empty" style="padding:16px">' + esc(err.message || '地图加载失败') + '</div>';
    });
  }

  function trafficBodyHTML() {
    const presets = [['24h', '近 24 小时'], ['today', '今日'], ['7d', '近 7 天'], ['30d', '近 30 天']];
    const chips = presets.map(p =>
      '<button class="btn btn-sm' + (trafficPreset === p[0] ? ' btn-primary' : '') + '" data-act="traffic-range" data-preset="' + p[0] + '">' + p[1] + '</button>'
    ).join('');
    const custom =
      '<input type="date" id="traffic-from-date" value="' + esc(trafficQuery.fromDate) + '"> ' +
      '<input type="time" id="traffic-from-time" value="' + esc(trafficQuery.fromTime) + '"> ~ ' +
      '<input type="date" id="traffic-to-date" value="' + esc(trafficQuery.toDate) + '"> ' +
      '<input type="time" id="traffic-to-time" value="' + esc(trafficQuery.toTime) + '"> ' +
      '<button class="btn btn-sm" data-act="traffic-apply">应用</button>';
    let body;
    if (trafficOff) {
      body = '<div class="empty" style="padding:24px 8px">' +
        '<div>观测组件未开启，无法统计流量（数据由访问日志/拦截日志按需聚合而来）</div>' +
        '<button class="btn btn-sm btn-primary" data-act="go-obs">去组件页开启观测</button></div>';
    } else if (trafficLoading && !traffic) {
      // 首次加载：给出友好等待提示（查库聚合数据量大时可达秒级，超时已放宽 30 秒）
      body = Rock.comp.empty.message({ text: '统计加载中…按时间范围查库聚合，数据量大时可能需要数秒，请稍候' });
    } else if (trafficErr) {
      body = Rock.comp.empty.emptyCard({ text: '流量统计加载失败：' + esc(trafficErr), br: true,
        action: '<button class="btn btn-sm btn-primary" data-act="overview-reload">重试</button>' });
    } else if (!traffic) {
      body = Rock.comp.empty.message({ text: '加载中…' });
    } else {
      const sm = traffic.summary || {};
      body = '<div style="margin-bottom:10px">' + chips + '</div>' +
        (trafficPreset === 'custom' ? '<div style="margin-bottom:10px">' + custom + '</div>' : '') +
        trafficTilesHTML(sm) +
        // 地理位置并入流量统计卡：紧接指标瓦片之下（异步拉取后经 renderGeoCard 填充）
        '<div class="geo-slot"></div>' +
        '<div class="grid grid-2" style="margin-top:12px">' +
        '<div><div class="card-title" style="margin-bottom:6px">访问趋势 <span class="card-sub">req_ok · UTC 桶</span></div>' +
        '<div class="chart-box" style="height:140px"><canvas id="traffic-chart-ok"></canvas></div></div>' +
        '<div><div class="card-title" style="margin-bottom:6px">拦截趋势 <span class="card-sub">blocked · UTC 桶</span></div>' +
        '<div class="chart-box" style="height:140px"><canvas id="traffic-chart-blocked"></canvas></div></div>' +
        '</div>';
    }
    return '<div class="card" style="margin-top:16px"><div class="card-title" style="display:flex;align-items:center;gap:8px">流量统计' +
      (trafficLoading && traffic ? ' <span class="tag tag-blue">刷新中…</span>' : '') +
      ' <span class="card-sub">按时间范围查库聚合 · 服务端缓存 15 分钟</span>' +
      '<span style="flex:1"></span>' +
      '<button class="btn btn-sm" data-act="traffic-cache-clear"' + (trafficLoading ? ' disabled' : '') +
      ' title="清空服务端统计缓存，下次查询强制重新聚合">清空缓存</button></div>' +
      body + '</div>';
  }

  // 流量趋势两图（render 后调用；桶标签为 UTC 原样，仅格式化显示）
  function drawTrafficCharts() {
    if (!traffic || !traffic.series) return;
    const rows = traffic.series.series || [];
    const toPoint = key => rows.map(r => ({ t: String(r.bucket).replace(' ', 'T') + 'Z', value: r[key] }));
    const dayFmt = t => { const d = Rock.util.toDate(t); return d ? (d.getUTCMonth() + 1) + '-' + d.getUTCDate() : ''; };
    const hourFmt = t => { const d = Rock.util.toDate(t); return d ? (d.getUTCMonth() + 1) + '-' + d.getUTCDate() + ' ' + Rock.util.pad2(d.getUTCHours()) + ':00' : ''; };
    const fmtX = traffic.bucket === 'day' ? dayFmt : hourFmt;
    Rock.comp.chart.line($('#traffic-chart-ok'), { data: toPoint('ok_count'), value: p => p.value, fmtX: fmtX });
    Rock.comp.chart.line($('#traffic-chart-blocked'), { data: toPoint('blocked_count'), value: p => p.value, fmtX: fmtX });
  }

  // 流量区局部重渲染（拉取完成后不整页重绘，避免打断其他区块）
  function renderTrafficBody() {
    const host = $('#page-overview .traffic-slot');
    if (!host || ovActiveTab !== 'overview') return;
    const oldMap = $('#geo-map'); // 地理位置已并入本卡，整卡重绘前先销毁地图实例防泄漏
    if (oldMap && window.echarts && echarts.getInstanceByDom) echarts.dispose(oldMap);
    host.innerHTML = trafficBodyHTML();
    drawTrafficCharts();
  }

  // 实时区（chips + 瓦片 + QPS 曲线）局部渲染单元：自动采样每 5s 静默更新此区块
  function metricsSlotHTML(metricsOff) {
    const uptime = store.system && store.system.uptime_seconds != null ? store.system.uptime_seconds : null;
    let body;
    if (metricsOff) {
      body =
        '<div class="empty" style="padding:24px 8px">' +
        '<div>观测组件未开启，无法获取运行指标</div>' +
        '<button class="btn btn-sm btn-primary" data-act="go-obs">去组件页开启观测</button>' +
        '</div>';
    } else if (!store.metrics) {
      body = Rock.comp.empty.message({ text: '暂无指标数据', padding: '24px 8px' });
    } else {
      const chips = ['1m', '5m', '15m', '1h'].map(w =>
        '<button class="btn btn-sm' + (metricsWindow === w ? ' btn-primary' : '') + '" data-act="metrics-window" data-window="' + w + '">' + w + '</button>'
      ).join('');
      body = '<div style="margin-bottom:10px">' + chips +
        '<span class="form-hint"> 内存窗口数据重启后从零重新累计</span></div>' +
        Rock.comp.metrics.metricTiles({
          metrics: store.metrics,
          history: store.metricsHistory,
          uptime: uptime,
          windowLabel: metricsWindow,
        });
    }
    const chartBody = metricsOff
      ? ''
      : '<div class="card-title" style="margin-top:12px;margin-bottom:6px">QPS <span class="card-sub">每秒请求 · 采样趋势 · 近 ' + esc(metricsWindow) + '（样本随页面刷新累积）</span></div>' +
        '<div class="chart-box" style="height:150px"><canvas id="overview-chart"></canvas></div>' +
        (store.metricsHistory.length < 2 ? '<div class="empty">等待采样数据…（每次刷新自动累积趋势采样）</div>' : '');
    return body + chartBody;
  }

  // 静默采样器：概览页可见期间每 5s 拉一次 /admin/metrics 并局部更新实时区，
  // 使 5m/15m/1h 窗口的 QPS 曲线有连续样本（离开概览页自动停采，页签隐藏时跳过）。
  let sampleTimer = null;
  function ensureSampler() {
    if (sampleTimer) return;
    sampleTimer = setInterval(function () {
      if (!location.hash.startsWith('#/overview') || document.hidden) return;
      api.get('/admin/metrics?window=' + metricsWindow).then(function (m) {
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
          if (store.metricsHistory.length > 720) store.metricsHistory.shift();
        }
        const slot = document.querySelector('.metrics-slot');
        if (slot && ovActiveTab === 'overview') {
          slot.innerHTML = metricsSlotHTML(store.metricsError === 'obs');
          drawChart();
        }
      }).catch(function () { /* 静默采样失败忽略，下轮再试 */ });
    }, 5000);
  }

  function overviewBodyHTML() {
    // ---- 运行指标卡（含趋势图，指标页合并至此；运行时间来自 /admin/system）----
    const metricsOff = store.metricsError === 'obs';
    const uptime = store.system && store.system.uptime_seconds != null ? store.system.uptime_seconds : null;
    const metricsBody = '<div class="metrics-slot">' + metricsSlotHTML(metricsOff) + '</div>';

    // ---- 独立服务 ----
    const services = store.switches.filter(s => s.kind === 'component');

    return gatewayBarHTML() +

      // 流量统计卡在前（含地理位置：紧接指标瓦片之下）；运行状态卡（实时口径）整体后移
      '<div class="traffic-slot" style="margin-top:16px">' + trafficBodyHTML() + '</div>' +

      '<div class="card" style="margin-top:16px"><div class="card-title">运行状态 <span class="card-sub">实时 · 趋势 · 资源</span></div>' +
      metricsBody +
      (metricsOff ? '' : '<div style="border-top:1px solid rgba(127,127,127,.15);margin-top:12px;padding-top:10px">' +
        '<div class="card-title" style="margin-bottom:6px">资源 <span class="card-sub">机器 CPU / 内存 · 进程占用</span></div>' +
        resourceRowsHTML() + '</div>') +
      '</div>' +

      '<div class="card" style="margin-top:16px"><div class="card-title">HTTP 数据流 <span class="card-sub">组件按链路顺序执行 · 开关即启停 · 点击名称进入详情（关闭即降级）</span></div>' +
      Rock.comp.dataflow.renderHTML(store.switches) +
      '</div>' +

      '<div class="card" style="margin-top:16px"><div class="card-title">服务状态总览 <span class="card-sub">独立服务 · 点击名称进入详情</span></div>' +
      overviewGridHTML(services, SERVICE_ORDER, 'services') +
      (services.length ? '' : '<div class="form-hint">服务按配置装配，当前未装配独立服务。</div>') +
      '</div>';
  }

  // ── 页签「小黑屋」：当前在押的限时封禁预览（IP_BLACKLIST_PLAN §3.7）──

  // 拉取小黑屋数据：失败统一弹 error toast（不自动消失），同时保留行内提示兜底；
  // 仅静默重载（silent）不弹 toast（避免重复操作刷屏），只更新行内提示
  let jailFetched = false; // 是否成功拉取过一次（失败时决定是否显示行内错误）
  async function loadJail(opts) {
    opts = opts || {};
    try {
      const res = await api.get('/admin/shield/jail?limit=20');
      jailRows = (res && res.rows) || [];
      jailTotal = Number(res && res.total) || jailRows.length;
      jailErr = null;
      jailFetched = true;
    } catch (e) {
      if (!opts.silent && e.status !== 0) toast('小黑屋加载失败：' + e.message + '，可稍后重试或检查 DB 配置', 'error');
      if (!jailFetched) jailErr = e.message || '加载失败';
    }
    renderJailBody();
  }

  // 小黑屋页签主体：说明 + 表格/空态 + 计数与出口（管理入口固定卡片右上角，主色可点击）
  function jailBodyHTML() {
    bindJailTable(); // 表格分页控件委托（#page-overview 持久，仅绑一次）
    return '<div class="card"><div class="card-title"><span>在押名单 ' +
      '<span class="card-sub">当前封禁IP（未过期、未软删）；封禁时间为首次封禁时间，临近解封的在前，永久殿后</span></span>' +
      '<a class="link-like" data-act="goto-iplist">管理全部黑名单 →</a></div>' +
      '<div id="jail-body">' + jailInnerHTML() + '</div></div>';
  }

  // 表格分页控件事件绑定（宿主元素持久，只绑一次防重复触发）
  let jailTableBound = false;
  function bindJailTable() {
    if (jailTableBound) return;
    const host = $('#page-overview');
    if (!host) return;
    jailTable.bind(host);
    jailTableBound = true;
  }

  // 表格区（loadJail 完成后只重渲染此块，不动页签结构）
  function jailInnerHTML() {
    if (jailErr) {
      return Rock.comp.empty.message({
        text: '小黑屋数据加载失败：' + jailErr + '。可稍后重试，或检查数据库配置后重进本页。',
        br: true,
      });
    }
    if (!jailRows.length) {
      return Rock.comp.empty.message({ text: '小黑屋空空如也', padding: '24px 8px' });
    }
    return jailTable.html(jailRows) + jailFooterHTML();
  }

  // 计数行：jail total + 超出预览条数提示（管理出口在卡片右上角「管理全部黑名单 →」）
  function jailFooterHTML() {
    const more = jailTotal > jailRows.length
      ? '，仅展示前 ' + jailRows.length + ' 条'
      : '';
    return '<div class="form-hint">共 ' + Rock.util.fmtInt(jailTotal) + ' 条在押' + more + '</div>';
  }

  // loadJail 完成后局部刷新表格区（当前停在宿主页才执行）
  function renderJailBody() {
    const wrap = $('#jail-body');
    if (wrap) wrap.innerHTML = jailInnerHTML();
  }

  // 趋势折线图（main.js resize 钩子调用）：按所选窗口过滤采样点，
  // 与瓦片的窗口口径一致（METRICS_WINDOW 修订：曲线跟随窗口，不再全量展示）
  function drawChart() {
    const winSec = (store.metrics && store.metrics.window_seconds) || 60;
    const since = Date.now() - winSec * 1000;
    const data = store.metricsHistory.filter(p => p.t >= since);
    Rock.comp.chart.line($('#overview-chart'), { data: data, value: p => p.qps });
  }

  window.Rock.views.overview = {
    load,
    render,
    skeleton,
    drawChart,
    ovCardHTML,
    tabsHTML,
    actions: {
      'overview-reload': function () { load({ manual: true }); },
      // 实时窗口桶宽切换（METRICS_WINDOW）：只重拉 metrics，不动其他区块
      'metrics-window': function (el) {
        metricsWindow = el.getAttribute('data-window') || '1m';
        render();
        api.get('/admin/metrics?window=' + metricsWindow).then(function (m) {
          store.metrics = normalizeMetrics(m);
          store.metricsError = null;
          render();
        }).catch(function () { /* 重拉失败保留旧数据，不打断页面 */ });
      },
      // 流量统计：预设时间范围切换（立即拉取）
      'traffic-range': function (el) {
        trafficPreset = el.getAttribute('data-preset') || '24h';
        render();
        loadTraffic({ manual: true });
      },
      // 流量统计：自定义范围应用（读当前输入值）
      'traffic-apply': function () {
        trafficPreset = 'custom';
        trafficQuery = {
          fromDate: ($('#traffic-from-date') || {}).value || trafficQuery.fromDate,
          fromTime: ($('#traffic-from-time') || {}).value || '00:00',
          toDate: ($('#traffic-to-date') || {}).value || trafficQuery.toDate,
          toTime: ($('#traffic-to-time') || {}).value || '23:59',
        };
        loadTraffic({ manual: true });
      },
      // 流量统计：清空服务端结果缓存（POST；成功后重新聚合当前范围，立见最新数据）
      'traffic-cache-clear': async function (el) {
        el.disabled = true;
        try {
          const r = await api.post('/admin/obs/traffic/cache_clear', trafficTimeoutMs)();
          if (r && r.ok === false) throw new Error(r.err || '清空失败');
          toast(r && r.text || '缓存已清空', 'success');
          traffic = null; geo = null; // 旧结果作废，强制重拉
          await loadTraffic({ manual: true, silent: true });
        } catch (e) {
          toast('清空缓存失败：' + (e.message || '未知错误') + '，可稍后重试', 'error');
        }
        renderTrafficBody();
      },
      // 地理位置：世界/中国切换（scope 决定聚合 level，地图与排名联动重拉）
      'geo-scope': function (el) {
        const key = el.getAttribute('data-key') || 'china';
        if (key === geoScope) return;
        geoScope = key === 'world' ? 'world' : 'china';
        geo = null; geoErr = null; // 清空旧维度数据，避免切换间隙用旧口径渲染新标签
        renderGeoCard();
        loadGeo({ silent: true });
      },
      // 地理位置：访问/总拦截切换（source 变更，地图与排名联动重拉）
      'geo-source': function (el) {
        const key = el.getAttribute('data-key') || 'access';
        if (key === geoSource) return;
        geoSource = key === 'blocked' ? 'blocked' : 'access';
        geo = null; geoErr = null;
        renderGeoCard();
        loadGeo({ silent: true });
      },
      // 地理位置：失败重试
      'geo-retry': function () { loadGeo({ manual: true }); },
      // 页签切换：总览 / 小黑屋（切到小黑屋时拉取最新在押数据）
      'overview-tab': function (el) {
        const tab = el.getAttribute('data-tab') || 'overview';
        if (tab === ovActiveTab) return;
        ovActiveTab = tab === 'jail' ? 'jail' : 'overview';
        render();
        if (ovActiveTab === 'jail') loadJail({});
      },
      // 跳黑白名单页签：先切路由，待 WAF 页渲染出页签后模拟点击「黑白名单」
      // （走全局点击委托，与用户手点等价；waf 页暂不识别 ?tab= 查询参数）
      'goto-iplist': function () {
        Rock.main.navigate('waf?tab=iplist');
        let tries = 0;
        const timer = setInterval(function () {
          if (++tries > 50) { clearInterval(timer); return; } // 最多等 5 秒
          const tab = document.querySelector('#page-waf .tab[data-tab="iplist"]');
          if (tab) { clearInterval(timer); tab.click(); }
        }, 100);
      },
    },
  };
})();
