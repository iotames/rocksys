/* ==========================================================================
 * RockSys 管理控制台 - main.js 入口
 * 路由与视图切换、侧边栏高亮、顶部工具条（访问凭证 / 自动刷新 / 手动刷新）、
 * 全局事件委托（click / change）、初始化。最后加载，依赖全部模块。
 * 挂载到全局命名空间 window.Rock.main。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};

  const $ = Rock.util.$;
  const $$ = Rock.util.$$;
  const store = Rock.state.store;
  const views = Rock.views;
  const ui = Rock.ui;

  const ROUTES = { overview: 1, components: 1, config: 1, scripts: 1, metrics: 1, logs: 1 };

  function currentRoute() {
    const h = location.hash.replace(/^#\/?/, '');
    return ROUTES[h] ? h : 'overview';
  }

  function navigate(route) {
    if (!ROUTES[route]) route = 'overview';
    if (route === currentRoute()) { refreshPage(route, {}); return; }
    location.hash = '/' + route;
  }

  function activateNav(route) {
    $$('.menu-item[data-route]').forEach(a => {
      a.classList.toggle('active', a.getAttribute('data-route') === route);
    });
    const inObs = route === 'metrics' || route === 'logs';
    const grp = $('#menu-group-obs');
    if (grp) grp.classList.toggle('open', inObs || grp.classList.contains('open'));
  }

  // 页面加载器配置（lazy=true 的页面首次进入拉取，之后保留缓存；其余每次进入拉取）
  const pageLoaders = {
    overview:   { fetch: () => views.overview.load({}), lazy: false },
    components: { fetch: () => views.components.load({}), lazy: false },
    metrics:    { fetch: () => views.metrics.load({}), lazy: false },
    config:     { fetch: () => views.config.load({}), lazy: true },
    scripts:    { fetch: () => views.scripts.load({}), lazy: true },
    logs:       { fetch: () => views.logs.loadPage({ force: true }), lazy: true },
  };

  function refreshPage(route, opts) {
    const p = pageLoaders[route];
    if (!p) return Promise.resolve();
    return Promise.resolve(p.fetch());
  }

  function renderPage(route) {
    $$('.page').forEach(sec => sec.classList.add('hidden'));
    const page = $('#page-' + route);
    if (page) page.classList.remove('hidden');
    activateNav(route);
    // 懒加载页已加载时直接渲染缓存（config/scripts/logs 保留原行为）
    const p = pageLoaders[route];
    if (p.lazy) {
      if ((route === 'config' && store.configListLoaded) ||
          (route === 'scripts' && store.scriptsLoaded) ||
          (route === 'logs' && store.logsLoaded)) {
        if (route === 'config') views.config.render();
        else if (route === 'scripts') views.scripts.render();
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
    $('#btn-token').addEventListener('click', () => ui.openTokenDialog(''));
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

  // 自动刷新（作用于概览 / 组件 / 指标）
  let autoTimer = null;
  function restartAutoRefresh() {
    if (autoTimer) { clearInterval(autoTimer); autoTimer = null; }
    const el = $('#auto-refresh');
    const v = Number(el ? el.value : 0) || 0;
    if (v > 0) {
      autoTimer = setInterval(() => {
        const r = currentRoute();
        if (r === 'overview' || r === 'components' || r === 'metrics') {
          refreshPage(r, { silent: true });
        }
      }, v);
    }
  }

  function initRoute() {
    window.addEventListener('hashchange', function () {
      renderPage(currentRoute());
    });
    // 侧边栏"观测"分组展开
    const parent = document.querySelector('.menu-parent');
    if (parent) {
      parent.addEventListener('click', function () {
        const grp = document.getElementById('menu-group-obs');
        if (grp) grp.classList.toggle('open');
      });
    }
    // 首次渲染
    renderPage(currentRoute());
  }

  // 应用启动
  function boot() {
    bindToolbar();
    initRoute();
    restartAutoRefresh();
    // 主题切换：同步下拉框并绑定切换事件
    if (Rock.theme) Rock.theme.bind();
    // 认证引导：检测管理接口状态，未登录/未初始化时显示认证视图
    if (Rock.auth) {
      Rock.auth.bind();
      Rock.auth.init();
    }
    // 窗口尺寸变化时重绘图表
    window.addEventListener('resize', Rock.util.debounce(function () {
      if (currentRoute() === 'metrics') views.metrics.drawChart();
    }, 200));
  }

  // 注入：访问凭证保存/清除后刷新当前页
  ui.setTokenSavedHandler(function () {
    refreshPage(currentRoute(), { manual: true });
  });

  // ======== 全局点击委托 ========
  document.addEventListener('click', function (e) {
    const el = e.target.closest('[data-act]');
    if (!el) return;
    const act = el.getAttribute('data-act');
    const key = el.getAttribute('data-k') || '';
    const name = el.getAttribute('data-name') || '';

    switch (act) {
      // ---- 路由跳转 ----
      case 'goto-config':
        navigate('config');
        break;
      case 'goto-components':
        navigate('components');
        break;
      case 'go-obs':
        navigate('components');
        break;

      // ---- 概览 ----
      case 'overview-reload':
        views.overview.load({ manual: true });
        break;

      // ---- 组件 ----
      case 'components-reload':
        views.components.load({ manual: true });
        break;
      case 'comp-config':
        views.components.toggleConfig(name);
        break;

      // ---- 配置 ----
      case 'cfg-tab':
        views.config.setActiveTab(name);
        break;
      case 'config-reload':
        views.config.load({ manual: true });
        break;
      case 'cfg-edit':
        views.config.startEdit(key);
        break;
      case 'cfg-cancel':
        views.config.cancelEdit();
        break;
      case 'cfg-save':
        views.config.saveEdit(key);
        break;
      case 'cfg-reset':
        views.config.resetItem(key);
        break;
      case 'cfg-mask':
        views.config.toggleMask(key);
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

      // ---- 指标 ----
      case 'metrics-reload':
        views.metrics.load({ manual: true });
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
        const idx = Number(el.getAttribute('data-idx'));
        views.logs.toggleExpand(idx);
        break;
      }
      default:
        break;
    }
  });

  // 组件开关（change 事件，二次确认；失败/取消时还原开关状态）
  document.addEventListener('change', function (e) {
    const el = e.target.closest('[data-act="comp-toggle"]');
    if (!el) return;
    const name = el.getAttribute('data-name');
    const enabling = el.checked;
    views.components.toggle(name, enabling).then(ok => {
      if (!ok) {
        el.checked = !enabling;
        if (el.disabled) el.disabled = false;
      }
    });
  });

  window.Rock.main = {
    boot,
    navigate,
    currentRoute,
    renderPage,
    refreshPage,
    restartAutoRefresh,
    manualRefresh,
  };
})();

// 启动（等待 DOM 就绪）
if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', Rock.main.boot);
} else {
  window.Rock.main.boot();
}
