/* ==========================================================================
 * RockSys 管理控制台 - views/overview.js 概览页
 * 页签「总览」：网关信息卡 + 运行指标卡（含趋势图） + HTTP 数据流图（组件节点带开关）
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

  // 小黑屋数据缓存（切页签/自动刷新时刷新；拉取失败保留旧数据 + 行内提示）
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
      // expires_at 理论上不会为 NULL（jail 只收限时封禁），判空兜底显示 —
      { key: 'expires_at', label: '解封时间', render: r => esc(r.expires_at ? Rock.util.fmtDateTime(r.expires_at) : '—') },
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
      // 顶部管理地址
      const addr = $('#gw-addr');
      if (addr) addr.textContent = '管理地址：' + (store.base.admin || '—');
    } catch (e) {
      store.overviewFailed = !store.baseLoaded && !store.switchesLoaded;
      if (!opts.silent && e.status !== 0 && !e.obsDisabled) {
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
        else if (!opts.silent && e.status !== 0) { toast('指标加载失败：' + e.message, 'error'); }
      }
    }
    render();
    // 小黑屋页签随首页刷新周期联动刷新（自动刷新由 main.js 驱动 load，总览页签不受影响）
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

    if (ovActiveTab !== 'jail' && !store.metricsError && store.metrics) drawChart();
  }

  // ── 页签「总览」：原有内容（行为不变）──────────────────────────────
  function overviewBodyHTML() {
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

    return '<div class="grid grid-2">' +
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
  }

  // ── 页签「小黑屋」：当前在押的限时封禁预览（IP_BLACKLIST_PLAN §3.7）──

  // 拉取小黑屋数据：失败统一弹 error toast（不自动消失），同时保留行内提示兜底；
  // 仅自动刷新（silent）不弹 toast（避免周期性刷屏），只更新行内提示
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

  // 小黑屋页签主体：说明 + 表格/空态 + 计数与出口
  function jailBodyHTML() {
    bindJailTable(); // 表格分页控件委托（#page-overview 持久，仅绑一次）
    return '<div class="card"><div class="card-title">在押名单 ' +
      '<span class="card-sub">当前在押的限时封禁（未过期、未软删）；封禁时间为首次封禁时间，临近解封的在前</span></div>' +
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

  // 计数行：jail total + 超出预览条数提示 + 管理全部黑名单出口
  function jailFooterHTML() {
    const more = jailTotal > jailRows.length
      ? '，仅展示前 ' + jailRows.length + ' 条，其余请到黑白名单页查看'
      : '';
    return '<div class="form-hint">共 ' + Rock.util.fmtInt(jailTotal) + ' 条在押' + more +
      ' · <a data-act="goto-iplist" style="cursor:pointer">管理全部黑名单 →</a></div>';
  }

  // loadJail 完成后局部刷新表格区（当前停在宿主页才执行）
  function renderJailBody() {
    const wrap = $('#jail-body');
    if (wrap) wrap.innerHTML = jailInnerHTML();
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
    tabsHTML,
    actions: {
      'overview-reload': function () { load({ manual: true }); },
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
