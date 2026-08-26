/* ==========================================================================
 * RockSys 管理控制台 - main.js 入口
 * 路由与视图切换、侧边栏高亮与分组折叠、顶部工具条（自动刷新 / 手动刷新）、
 * 全局事件委托（click / change）、初始化。最后加载，依赖全部模块。
 * 路由支持参数化：#/components/<name>、#/services/<name>，可带 ?tab=config 查询。
 * 挂载到全局命名空间 window.Rock.main。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};

  const $ = Rock.util.$;
  const $$ = Rock.util.$$;
  const store = Rock.state.store;
  const views = Rock.views;

  // 路由表：1 = 固定页；'param' = 带二级参数（组件/服务详情）
  const ROUTES = {
    overview: 1, components: 'param', services: 'param',
    scripts: 1, config: 1, waf: 1, logs: 1, syslogs: 1,
  };
  // 侧边栏可折叠分组（路由 base → 分组 id）
  const MENU_GROUPS = ['components', 'services', 'obs'];

  // 解析 location.hash → { base, param, query }
  function parseHash() {
    const raw = location.hash.replace(/^#\/?/, '');
    const qIdx = raw.indexOf('?');
    const path = qIdx >= 0 ? raw.slice(0, qIdx) : raw;
    const qs = qIdx >= 0 ? raw.slice(qIdx + 1) : '';
    const r = parsePath(path);
    const query = {};
    qs.split('&').forEach(kv => {
      if (!kv) return;
      const i = kv.indexOf('=');
      if (i > 0) query[decodeURIComponent(kv.slice(0, i))] = decodeURIComponent(kv.slice(i + 1));
    });
    return { base: r.base, param: r.param, query };
  }

  // 解析路由字符串（如 'components/shield?tab=config'）→ { base, param }
  function parsePath(str) {
    const raw = String(str || '').replace(/^#\/?/, '');
    const qIdx = raw.indexOf('?');
    const path = qIdx >= 0 ? raw.slice(0, qIdx) : raw;
    const seg = path.split('/').filter(Boolean);
    return { base: seg[0] || '', param: seg[1] || '' };
  }

  function currentRoute() {
    const r = parseHash();
    if (!ROUTES[r.base]) return { base: 'overview', param: '', query: {} };
    return r;
  }

  // 跳转：navigate('components/shield') 或 navigate('components/shield?tab=config')
  function navigate(route) {
    const r = parsePath(route);
    if (!ROUTES[r.base]) route = 'overview';
    const cur = currentRoute();
    const curFull = (cur.param ? cur.base + '/' + cur.param : cur.base) +
      (cur.query && cur.query.tab ? '?tab=' + cur.query.tab : '');
    if (route === curFull) { refreshPage(cur, {}); return; }
    location.hash = '#/' + route;
  }

  function activateNav(route) {
    const full = route.param ? route.base + '/' + route.param : route.base;
    $$('.menu-item[data-route]').forEach(a => {
      a.classList.toggle('active', a.getAttribute('data-route') === full);
    });
    // 激活项所在分组自动展开；其余保持用户手动状态
    MENU_GROUPS.forEach(g => {
      const grp = $('#menu-group-' + g);
      if (grp && route.base === g) grp.classList.add('open');
    });
  }

  // 页面加载器配置（components/services 为详情页，按当前路由参数加载；lazy=true 的页面首次进入拉取）
  const pageLoaders = {
    overview:   { fetch: () => views.overview.load({}), lazy: false },
    components: { fetch: o => views.detail.load(Object.assign({ type: 'component', name: currentRoute().param, tab: currentRoute().query.tab }, o || {})), lazy: false },
    services:   { fetch: o => views.detail.load(Object.assign({ type: 'service', name: currentRoute().param, tab: currentRoute().query.tab }, o || {})), lazy: false },
    waf:        { fetch: () => views.waf.load({}), lazy: true },
    config:     { fetch: () => views.config.load({}), lazy: true },
    scripts:    { fetch: () => views.scripts.load({}), lazy: true },
    logs:       { fetch: () => views.logs.loadPage({ force: true }), lazy: true },
    syslogs:    { fetch: () => views.syslogs.load({}), lazy: false },
  };

  function refreshPage(route, opts) {
    const p = pageLoaders[route.base];
    if (!p) return Promise.resolve();
    return Promise.resolve(p.fetch(opts || {}));
  }

  // 路由切换前的清理钩子：系统日志页离开时关闭 SSE 实时流，避免后台连接泄漏
  let prevRoute = '';
  function renderPage(route) {
    if (prevRoute.base === 'syslogs' && route.base !== 'syslogs' && views.syslogs) {
      views.syslogs.leave();
    }
    prevRoute = route;
    $$('.page').forEach(sec => sec.classList.add('hidden'));
    const page = $('#page-' + route.base);
    if (page) page.classList.remove('hidden');
    activateNav(route);
    const p = pageLoaders[route.base];
    if (p.lazy) {
      if ((route.base === 'config' && store.configListLoaded) ||
          (route.base === 'scripts' && store.scriptsLoaded) ||
          (route.base === 'logs' && store.logsLoaded) ||
          (route.base === 'waf' && store.wafLoaded)) {
        if (route.base === 'config') views.config.render();
        else if (route.base === 'scripts') views.scripts.render();
        else if (route.base === 'waf') views.waf.render();
        else views.logs.render();
      } else {
        p.fetch();
      }
    } else {
      p.fetch();
    }
  }

  // 顶部工具条
  function bindToolbar() {
    $('#auto-refresh').addEventListener('change', restartAutoRefresh);
    $('#btn-refresh').addEventListener('click', manualRefresh);
  }

  function setRefreshing(v) {
    const b = $('#btn-refresh');
    if (b) b.classList.toggle('is-loading', v);
  }

  function manualRefresh() {
    setRefreshing(true);
    refreshPage(currentRoute(), { manual: true }).then(() => setRefreshing(false), () => setRefreshing(false));
  }

  // 左上角品牌区版本号（GET /admin/version，与 rocksys --version 同源；失败静默不阻塞控制台）
  function fetchVersion() {
    Rock.api.get('/admin/version').then(function (info) {
      const el = $('#brand-version');
      if (el && info && info.version) el.textContent = info.version;
    }).catch(function () { /* 版本号不可用时保持空 */ });
  }

  // 数据清理未开启警告（常驻置顶横幅，登录警告机制）：
  // 启动/登录后经 GET /admin/warnings 拉取（刷新页面不丢失、配置变更实时反映），
  // 与登录响应 warnings 同源。401（未登录）静默——登录流程成功后再渲染。
  function loadPruneWarnings() {
    Rock.api.get('/admin/warnings').then(function (r) {
      const ws = (r && Array.isArray(r.warnings)) ? r.warnings.filter(function (w) { return !!w; }) : [];
      store.loginWarnings = ws.length ? ws : null;
      renderPruneBanner();
    }).catch(function () { /* 未登录/网络异常静默，横幅保持隐藏 */ });
  }

  // 渲染常驻横幅：loginWarnings 非空显示置顶警告（可关闭），空则隐藏
  function renderPruneBanner() {
    const el = $('#prune-banner');
    if (!el) return;
    const ws = store.loginWarnings || [];
    if (!ws.length) { el.classList.add('hidden'); return; }
    const txt = $('#prune-banner-text');
    if (txt) txt.innerHTML = ws.map(function (w) { return '<span>' + Rock.util.esc(w) + '</span>'; }).join('');
    el.classList.remove('hidden');
  }

  // 关闭常驻横幅（仅本次会话：置空后隐藏；刷新页面重新拉取显示）
  function dismissPruneBanner() {
    store.loginWarnings = null;
    renderPruneBanner();
  }

  // 自动刷新（作用于概览 / 组件 / 服务 / WAF安全防护）
  let autoTimer = null;
  function restartAutoRefresh() {
    if (autoTimer) { clearInterval(autoTimer); autoTimer = null; }
    const el = $('#auto-refresh');
    const v = Number(el ? el.value : 0) || 0;
    if (v > 0) {
      autoTimer = setInterval(() => {
        const r = currentRoute();
        if (r.base === 'overview' || r.base === 'waf') {
          refreshPage(r, { silent: true });
        } else if (r.base === 'components' || r.base === 'services') {
          // 配置页签正在编辑时不整页重绘，避免打断输入；状态页签强制拉取最新开关状态
          if (r.query.tab !== 'config') refreshPage(r, { silent: true, force: true });
        }
      }, v);
    }
  }

  function initRoute() {
    window.addEventListener('hashchange', function () {
      renderPage(currentRoute());
    });
    // 侧边栏分组展开（组件 / 服务 / 观测）
    $$('.menu-parent').forEach(p => {
      p.addEventListener('click', function () {
        const grp = document.getElementById('menu-group-' + p.getAttribute('data-group'));
        if (grp) grp.classList.toggle('open');
      });
    });
    // 首次渲染
    renderPage(currentRoute());
  }

  // 应用启动
  function boot() {
    bindToolbar();
    initRoute();
    restartAutoRefresh();
    fetchVersion();
    loadPruneWarnings();
    // 主题切换：同步下拉框并绑定切换事件
    if (Rock.theme) Rock.theme.bind();
    // 认证引导：检测管理接口状态，未登录/未初始化时显示认证视图
    if (Rock.auth) {
      Rock.auth.bind();
      Rock.auth.init();
    }
    // 窗口尺寸变化时重绘图表
    window.addEventListener('resize', Rock.util.debounce(function () {
      const r = currentRoute();
      if (r.base === 'overview' && views.overview) views.overview.drawChart();
      if (r.base === 'waf' && views.waf) views.waf.drawDailyChart();
    }, 200));
  }

  // ======== 全局 tooltip 委托（[data-tip]：跟随鼠标、视口内自动翻转，防溢出） ========
  let tipEl = null;
  document.addEventListener('mouseover', function (e) {
    const el = e.target.closest('[data-tip]');
    if (!el) { hideTip(); return; }
    const txt = el.getAttribute('data-tip');
    if (!txt) { hideTip(); return; }
    if (!tipEl) {
      tipEl = document.createElement('div');
      tipEl.className = 'tip-popup';
      document.body.appendChild(tipEl);
    }
    tipEl.textContent = txt;
    tipEl.style.display = 'block';
    const rect = el.getBoundingClientRect();
    tipEl.style.left = '0px';
    tipEl.style.top = '0px';
    const pad = 12;
    let x = rect.left + rect.width / 2 - tipEl.offsetWidth / 2;
    x = Math.min(Math.max(x, pad), window.innerWidth - tipEl.offsetWidth - pad);
    let y = rect.top - tipEl.offsetHeight - 10;
    if (y < pad) y = rect.bottom + 10; // 上方放不下则下方
    if (y + tipEl.offsetHeight > window.innerHeight - pad) y = Math.max(pad, window.innerHeight - tipEl.offsetHeight - pad);
    tipEl.style.left = x + 'px';
    tipEl.style.top = y + 'px';
  });
  document.addEventListener('mouseout', function (e) {
    if (!e.target.closest('[data-tip]')) hideTip();
  });
  document.addEventListener('scroll', hideTip, true);
  window.addEventListener('resize', Rock.util.debounce(hideTip, 150));
  function hideTip() { if (tipEl) tipEl.style.display = 'none'; }

  // ======== 全局点击委托 ========
  document.addEventListener('click', function (e) {
    const el = e.target.closest('[data-act]');
    if (!el) return;
    const act = el.getAttribute('data-act');
    const key = el.getAttribute('data-k') || '';
    const name = el.getAttribute('data-name') || '';

    switch (act) {
      // ---- 路由跳转 ----
      case 'goto-overview':
        navigate('overview');
        break;
      case 'goto-config':
        navigate('config');
        break;
      case 'goto-scripts':
        navigate('scripts');
        break;
      case 'go-obs':
        navigate('components/obs');
        break;
      case 'nav-detail': {
        const target = el.getAttribute('data-route') || '';
        if (target) navigate(target);
        break;
      }

      // ---- 概览 ----
      case 'overview-reload':
        views.overview.load({ manual: true });
        break;

      // ---- 组件/服务详情 ----
      case 'detail-reload':
        refreshPage(currentRoute(), { manual: true });
        break;
      case 'detail-tab': {
        const r = currentRoute();
        views.detail.setTab({
          type: r.base === 'services' ? 'service' : 'component',
          name: r.param,
        }, el.getAttribute('data-tab') || 'state');
        break;
      }

      // ---- 配置 ----
      case 'cfg-tab':
        views.config.setActiveTab(name);
        break;
      case 'config-reload':
        views.config.load({ manual: true });
        break;
      case 'cfg-edit':
        Rock.comp.configEditor.startEdit(key);
        break;
      case 'cfg-cancel':
        Rock.comp.configEditor.cancelEdit();
        break;
      case 'cfg-save':
        Rock.comp.configEditor.saveEdit(key);
        break;
      case 'cfg-reset':
        Rock.comp.configEditor.resetItem(key);
        break;
      case 'cfg-mask':
        Rock.comp.configEditor.toggleMask(key);
        break;

      // ---- 脚本 ----
      case 'scripts-reload':
        views.scripts.load({ manual: true });
        break;
      case 'script-select':
        views.scripts.select(name);
        break;
      case 'script-new':
        views.scripts.openNew();
        break;
      case 'script-check':
        views.scripts.checkCurrent();
        break;
      case 'script-publish':
        views.scripts.publish();
        break;
      case 'script-rollback':
        views.scripts.openRollback();
        break;

      // ---- WAF安全防护（WAF 监控）----
      case 'waf-reload':
        views.waf.load({ manual: true });
        break;
      case 'waf-query':
        views.waf.queryEvents();
        break;
      case 'waf-reset':
        views.waf.resetFilter();
        break;
      case 'waf-prune-events':
        views.waf.pruneEvents();
        break;
      case 'waf-prune-logs':
        views.waf.pruneLogs();
        break;
      case 'waf-expand': {
        // data-idx 为行展开键（time|trace_id|ip 字符串）
        const wkey = el.getAttribute('data-idx') || '';
        views.waf.toggleExpand(wkey);
        break;
      }

      // ---- 黑白名单管理 Tab（WAF 方案 §6.2）----
      case 'waf-tab':
        views.waf.ipListSwitchTab(el.getAttribute('data-tab') || 'stats');
        break;
      case 'waf-iplist-kind':
        views.waf.ipListSwitchKind(el.getAttribute('data-kind') || 'black');
        break;
      case 'waf-iplist-query':
        views.waf.ipListQuery();
        break;
      case 'waf-iplist-reset':
        views.waf.ipListReset();
        break;
      case 'waf-iplist-reload':
        views.waf.ipListQuery();
        break;
      case 'waf-iplist-page':
        views.waf.ipListPage(Number(el.getAttribute('data-dir')) || 0);
        break;
      case 'waf-iplist-add':
        views.waf.ipListAdd();
        break;
      case 'waf-iplist-del':
        views.waf.ipListDelete(el.getAttribute('data-id'));
        break;
      case 'waf-iplist-restore':
        views.waf.ipListRestore(el.getAttribute('data-id'));
        break;
      case 'waf-iplist-import':
        views.waf.ipListImport();
        break;

      // ---- 数据清理警告横幅 ----
      case 'prune-dismiss':
        dismissPruneBanner();
        break;

      // ---- 日志 ----
      case 'logs-reload':
        views.logs.query();
        break;
      case 'log-query':
        views.logs.query();
        break;
      case 'log-export':
        views.logs.exportLogs();
        break;
      case 'log-reset':
        views.logs.resetFilter();
        break;
      case 'log-expand': {
        // data-idx 为行展开键（time|trace_id 字符串），不再是数字索引，勿转 Number
        const key = el.getAttribute('data-idx') || '';
        views.logs.toggleExpand(key);
        break;
      }

      // ---- 系统日志 ----
      case 'syslog-toggle-stream':
        views.syslogs.toggleStream();
        break;
      case 'syslog-history':
        views.syslogs.loadHistory(500);
        break;
      case 'syslog-clear':
        views.syslogs.clearLines();
        break;
      default:
        break;
    }
  });

  // 组件/服务开关（change 事件，二次确认；失败/取消时还原开关状态）
  // 概览页卡片开关切换成功后额外刷新概览（详情页由 detail.toggle 自行刷新）
  document.addEventListener('change', function (e) {
    const el = e.target.closest('[data-act="detail-toggle"]');
    if (!el) return;
    const name = el.getAttribute('data-name');
    const enabling = el.checked;
    const r = currentRoute();
    const onOverview = r.base === 'overview';
    const type = el.getAttribute('data-type') === 'service' ? 'service' : 'component';
    views.detail.toggle(name, enabling, { type: type, tab: r.query.tab || 'state' }).then(ok => {
      if (!ok) {
        el.checked = !enabling;
        if (el.disabled) el.disabled = false;
      } else if (onOverview) {
        views.overview.load({ silent: true });
      }
    });
  });

  window.Rock.main = {
    boot,
    navigate,
    currentRoute,
    parseHash,
    renderPage,
    refreshPage,
    restartAutoRefresh,
    manualRefresh,
    loadPruneWarnings,
    renderPruneBanner,
    dismissPruneBanner,
  };
})();

// 启动（等待 DOM 就绪）
if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', Rock.main.boot);
} else {
  window.Rock.main.boot();
}
