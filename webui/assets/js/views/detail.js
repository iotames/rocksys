/* ==========================================================================
 * RockSys 管理控制台 - views/detail.js 组件/服务详情页（通用模板）
 * 一个组件/服务一个页面，统一「状态 / 配置」双页签：
 *   - 状态页签（默认）：大卡片 = 左上 switch 直接启停 + 中文名/英文名 + 环节标签
 *     + 状态 + 描述 + 运行信息 + 数据流位置示意
 *   - 配置页签：该组件/服务独有配置项（复用 Rock.comp.configEditor），
 *     无配置项显示空态引导；script 组件附"去脚本页发布策略"链接
 * 启停经二次确认后调用 /admin/switch/on|off，失败透出 error 原文。
 * 页签状态与 URL 联动（#/components/<name>?tab=config，刷新不丢）。
 * 挂载到全局命名空间 window.Rock.views.detail。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const fmtDateTime = Rock.util.fmtDateTime;
  const store = Rock.state.store;
  const COMPONENT_PREFIX = Rock.state.COMPONENT_PREFIX;
  const normalizeSwitches = Rock.state.normalizeSwitches;
  const api = Rock.api;
  const toast = Rock.ui.toast;
  const confirmDialog = Rock.ui.confirmDialog;
  const skeletonHTML = Rock.ui.skeletonHTML;
  const noteUpdated = Rock.ui.noteUpdated;

  // 拉取组件/服务数据（switches + 配置列表），首次骨架屏
  async function load(opts) {
    opts = opts || {};
    const first = !store.switchesLoaded && !opts.silent;
    if (first) skeleton(opts);
    try {
      if (!store.switchesLoaded || opts.force || opts.manual) {
        const switches = await api.get('/admin/switch/list');
        store.switches = normalizeSwitches(switches);
        store.switchesLoaded = true;
        noteUpdated();
      }
      if (!store.configListLoaded && !store.configUnavailable) {
        await Rock.comp.configEditor.loadList();
      }
    } catch (e) {
      store.componentsFailed = !store.switchesLoaded;
      if (!opts.silent && e.status !== 0) toast('组件数据加载失败：' + e.message, 'error');
    }
    render(opts);
  }

  function skeleton(opts) {
    const host = pageHost(opts);
    if (host) host.innerHTML = skeletonHTML(4);
  }

  function pageHost(opts) {
    return opts && opts.type === 'service' ? $('#page-services') : $('#page-components');
  }

  // 面包屑：概览 > 组件/服务 > 名称（中间层为分组名，仅概览可点）
  function breadcrumbHTML(opts) {
    const group = opts.type === 'service' ? '服务' : '组件';
    const meta = Rock.state.componentMeta(opts.name, opts.type === 'service' ? 'service' : 'middleware');
    return '<div class="breadcrumb">' +
      '<a data-act="goto-overview" href="#/overview">概览</a>' +
      '<span class="crumb-sep">/</span>' +
      '<span class="crumb-static">' + esc(group) + '</span>' +
      '<span class="crumb-sep">/</span>' +
      '<span class="crumb-cur">' + esc(meta.title) + ' ' + esc(opts.name) + '</span>' +
      '</div>';
  }

  // 状态页签：运行信息卡片（开关 / 名称 / 环节 / 描述已上移至页面公共区域）
  function stateCardHTML(s, opts) {
    const meta = Rock.comp.componentState.meta(s.name, s.kind);
    const isService = opts.type === 'service';
    const slotHint = isService
      ? '独立于 HTTP 数据流运行，作为网关的支撑系统。'
      : ('位于数据流「' + (meta.slotLabel || '链中间件') + '」，请求按顺序流经本环节。');
    const msgBad = /fail|error|timeout/i.test(s.message);
    return '<div class="detail-card">' +
      '<div class="comp-meta">' +
      '<span>启用时间 <b>' + esc(fmtDateTime(s.started_at)) + '</b></span>' +
      '<span>最近切换 <b>' + esc(fmtDateTime(s.last_switch_at)) + '</b></span>' +
      (s.message ? '<span class="' + (msgBad ? 'text-danger' : '') + '">状态：' + esc(s.message) + '</span>' : '') +
      '</div>' +
      '<div class="detail-slot">' +
      '<span class="muted">数据流位置：</span>' + esc(slotHint) +
      (isService ? '' : ' <span class="muted">（关闭本组件，请求直通下一环，转发不中断）</span>') +
      '</div>' +
      '</div>';
  }

  // 配置页签容器（hidden 由页签切换控制）
  function renderConfigPanel(container, name, type) {
    if (!container) return;
    const prefix = COMPONENT_PREFIX[name];
    let items = [];
    if (prefix) items = store.configList.filter(c => c.key.indexOf(prefix) === 0);
    if (store.configUnavailable && !items.length) {
      container.innerHTML = '<div class="empty">配置接口暂不可用（/admin/config/list）</div>';
      return;
    }
    if (!items.length) {
      container.innerHTML = '<div class="empty">' +
        '<div>' + esc(Rock.state.componentMeta(name, type === 'service' ? 'service' : 'middleware').title) + ' 无独立配置项</div>' +
        '<div class="muted" style="margin-top:6px">本组件不持有专属配置；全局基础设施配置请前往「全局配置」页。</div>' +
        (name === 'script'
          ? '<button class="btn btn-sm btn-primary" style="margin-top:12px" data-act="goto-scripts">去脚本页发布策略</button>'
          : '') +
        '</div>';
      return;
    }
    Rock.comp.configEditor.render(container, items, {});
    // 全局配置页搜索跳转而来：定位并进入编辑（一次性消费，未命中静默忽略）
    if (store.pendingCfgLocate) {
      const k = store.pendingCfgLocate;
      delete store.pendingCfgLocate;
      Rock.comp.configEditor.locateAndEdit(k);
    }
  }

  function configCount(name) {
    const prefix = COMPONENT_PREFIX[name];
    if (!prefix) return 0;
    return store.configList.filter(c => c.key.indexOf(prefix) === 0).length;
  }

  // 渲染入口
  function render(opts) {
    opts = opts || {};
    const host = pageHost(opts);
    if (!host) return;
    if (store.componentsFailed && !store.switchesLoaded) {
      host.innerHTML = breadcrumbHTML(opts) +
        Rock.comp.head.headHTML({
          title: '组件 / 服务',
          desc: '状态与配置',
          actions: '<button class="btn btn-sm" data-act="detail-reload">⟳ 重试</button>',
        }) +
        Rock.comp.empty.emptyCard({
          text: '管理接口不可达，无法加载组件/服务数据。',
          action: '<button class="btn btn-sm btn-primary" data-act="detail-reload">重试</button>',
          br: true,
        });
      return;
    }
    if (!store.switchesLoaded) { skeleton(opts); return; }
    const s = store.switches.find(x => x.name === opts.name);
    const meta = Rock.state.componentMeta(opts.name, opts.type === 'service' ? 'service' : 'middleware');
    if (!s) {
      host.innerHTML = breadcrumbHTML(opts) +
        Rock.comp.head.headHTML({ title: esc(meta.title) + ' ' + esc(opts.name), desc: '组件状态' }) +
        '<div class="card">' + Rock.comp.empty.message({ text: opts.name === 'mq'
          ? '消息组件按配置装配（MQ_ENABLED + MQ_DSN），当前未装配。'
          : '未找到该组件。' }) + '</div>';
      return;
    }
    const tab = opts.tab === 'config' ? 'config' : 'state';
    const cnt = configCount(opts.name);
    const isService = opts.type === 'service';
    const st = Rock.comp.componentState.stateMeta(s.state);
    const slotLabel = isService ? '独立服务' : (meta.slotLabel || '链中间件');
    const barHTML =
      '<span class="page-title-bar">' +
      '<label class="el-switch" title="' + esc(st.text) + '">' +
      '<input type="checkbox" data-act="detail-toggle" data-name="' + esc(opts.name) + '" data-type="' + (isService ? 'service' : 'component') + '"' +
      (s.state === 'enabled' ? ' checked' : '') +
      (s.state === 'draining' ? ' disabled' : '') + '>' +
      '<span class="el-switch-core"></span></label>' +
      '<span class="detail-name">' +
      '<span class="detail-cn">' + esc(meta.title) + '</span>' +
      '<span class="comp-key">' + esc(opts.name) + '</span>' +
      '</span>' +
      '<span class="tag tag-blue">' + esc(slotLabel) + '</span>' +
      '</span>';
    host.innerHTML =
      breadcrumbHTML(opts) +
      Rock.comp.head.headHTML({
        titleHTML: barHTML,
        desc: esc(meta.desc || ''),
        actions: '<button class="btn btn-sm" data-act="detail-reload">⟳ 刷新</button>',
      }) +
      Rock.comp.tabs.tabsHTML(
        [{ name: 'state', label: '状态' }, { name: 'config', label: '配置', count: cnt || 0 }],
        tab,
        { act: 'detail-tab', nameAttr: 'data-tab' }
      ) +
      '<div id="detail-panel-state"' + (tab === 'state' ? '' : ' hidden') + '>' + stateCardHTML(s, opts) + '</div>' +
      '<div id="detail-panel-config"' + (tab === 'config' ? '' : ' hidden') + '></div>';
    // 容器内查询（components/services 两个 page 容器都有同名 panel，避免渲染错位）
    if (tab === 'config') renderConfigPanel(host.querySelector('#detail-panel-config'), opts.name, opts.type);
  }

  // 切换页签（同步 URL hash，刷新不丢）
  function setTab(opts, tab) {
    const base = '#/' + (opts.type === 'service' ? 'services' : 'components') + '/' + opts.name;
    location.hash = tab === 'config' ? base + '?tab=config' : base;
  }

  // 启停组件/服务（二次确认 → 请求 → Toast → 刷新）
  async function toggle(name, enabling, opts) {
    opts = opts || {};
    const meta = Rock.state.componentMeta(name, opts.type === 'service' ? 'service' : 'middleware');
    const isService = opts.type === 'service';
    const kindText = isService ? '服务' : '组件';
    const ok = await confirmDialog({
      title: (enabling ? '开启' : '关闭') + kindText + ' · ' + meta.title,
      message:
        '<p>' + (enabling
          ? (isService ? '开启后将作为网关的独立支撑系统运行。' : '开启后将参与请求链路，相关规则立即生效。')
          : (isService ? '关闭后该服务停止运行，不影响转发链路。' : '关闭后请求将绕过该环节直通下一级，转发不会中断。')) + '</p>' +
        '<p class="muted">启停影响全局，需确认。' + (enabling ? '' : '关闭只是降级，转发永不中断。') + '</p>',
      confirmText: enabling ? '确认开启' : '确认关闭',
      danger: !enabling,
    });
    if (!ok) return false;
    try {
      const res = await api.post('/admin/switch/' + (enabling ? 'on' : 'off'))({ name: name });
      if (res && res.ok === false) {
        toast((enabling ? '开启失败：' : '关闭失败：') + (res.error || '未知错误'), 'error');
        return false;
      }
      toast((enabling ? '已启用 ' : '已关闭 ') + meta.title + '（已即时生效，无需重启）', 'success');
      load({ type: opts.type, name: name, tab: opts.tab, silent: true, force: true });
      return true;
    } catch (e) {
      toast((enabling ? '开启失败：' : '关闭失败：') + e.message, 'error');
      return false;
    }
  }

  window.Rock.views.detail = {
    load,
    render,
    skeleton,
    setTab,
    toggle,
    stateCardHTML,
    actions: {
      'detail-reload': function () { Rock.main.refreshPage(Rock.main.currentRoute(), { manual: true }); },
      'detail-tab': function (el) {
        const r = Rock.main.currentRoute();
        setTab({ type: r.base === 'services' ? 'service' : 'component', name: r.param }, el.getAttribute('data-tab') || 'state');
      },
    },
  };
})();
