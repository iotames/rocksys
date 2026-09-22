/* ==========================================================================
 * RockSys 管理控制台 - views/detail.js 组件/服务详情页（通用模板）
 * 一个组件/服务一个页面，统一「状态 / 配置」双页签；页签排序规范：配置一律最后。
 * dispatch 组件额外有「路由规则 / 负载均衡器 / 上游节点」三个管理页签
 * （路由分发视图经 Rock.views.dispatch.mountTab 按页签懒挂载）：
 *   - 状态页签（默认）：大卡片 = 左上 switch 直接启停 + 中文名/英文名 + 环节标签
 *     + 状态 + 描述 + 运行信息 + 数据流位置示意；dispatch 组件额外挂路由数据
 *     规模卡（Rock.views.dispatch.mountScaleCard，计数可点击直达对应管理页签）
 *   - 管理页签（仅 dispatch）：规则/均衡器/节点各自独立页签，?tab= URL 直达
 *   - 配置页签：该组件/服务独有配置项（复用 Rock.views.configEditor），
 *     顶部局部搜索（Rock.comp.cfgSearch，风格同全局配置页，仅搜本组件配置项，
 *     定位后滚动高亮并自动进入行内编辑）；无配置项显示空态引导；
 *     script 组件附"去脚本页发布策略"链接
 * 启停经二次确认后调用 /admin/switch/on|off，失败透出 error 原文。
 * 页签状态与 URL 联动（#/components/<name>?tab=<页签>，刷新不丢）。
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
  const notify = Rock.ui.notify;
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
        await Rock.views.configEditor.loadList();
      }
    } catch (e) {
      store.componentsFailed = !store.switchesLoaded;
      if (!opts.silent && e.status !== 0) notify.error('组件数据加载失败：' + e.message);
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
    // 顶部局部搜索（风格同全局配置页，作用域仅本组件配置项）+ 配置行子容器
    const rows = Rock.comp.cfgSearch.mount(container, items, function (key) {
      Rock.views.configEditor.locateAndEdit(key);
    }, {
      // 敏感/需重启属业务语义（枚举判定由视图层注入，组件保持业务无关）
      decorate: function (it) {
        return {
          sensitive: Rock.state.isSensitiveKey(it.key),
          restart: Rock.state.RESTART_KEYS.indexOf(it.key) >= 0,
        };
      },
    });
    Rock.views.configEditor.render(rows, items, {});
    // 全局配置页搜索跳转而来：定位并进入编辑（一次性消费，未命中静默忽略）
    if (store.pendingCfgLocate) {
      const k = store.pendingCfgLocate;
      delete store.pendingCfgLocate;
      Rock.views.configEditor.locateAndEdit(k);
    }
  }

  function configCount(name) {
    const prefix = COMPONENT_PREFIX[name];
    if (!prefix) return 0;
    return store.configList.filter(c => c.key.indexOf(prefix) === 0).length;
  }

  // dispatch 组件详情页描述（原组件描述与路由分发页说明合并润色；三个管理页签内不重复）
  const DISPATCH_DESC = '按 URL 规则从「路由规则 → 负载均衡器 → 上游节点」三层体系中选出目标后端并写入转发信息：规则按序号升序逐条匹配（域名维度可选参与），命中即停，全未命中走默认后端。路由数据在本页对应页签维护，保存即热更；关闭组件即降级，请求直通下一环，转发不中断。';

  function headDesc(name, meta) {
    return name === 'dispatch' ? DISPATCH_DESC : (meta.desc || '');
  }

  // dispatch 组件管理页签集合（rules/upstreams/nodes → 路由分发视图对应管理视图）
  const DISPATCH_TABS = [
    { name: 'rules', label: '路由规则' },
    { name: 'upstreams', label: '负载均衡器' },
    { name: 'nodes', label: '上游节点' },
  ];

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
    const tab = resolveTab(opts);
    const cnt = configCount(opts.name);
    const isService = opts.type === 'service';
    const st = Rock.comp.componentState.stateMeta(s.state);
    const slotLabel = isService ? '独立服务' : (meta.slotLabel || '链中间件');
    const barHTML =
      '<span class="page-title-bar">' +
      Rock.comp.form.switch({ title: st.text, attrs: { 'data-act': 'detail-toggle', 'data-name': opts.name, 'data-type': (isService ? 'service' : 'component') }, checked: s.state === 'enabled', disabled: s.state === 'draining' }) +
      '<span class="detail-name">' +
      '<span class="detail-cn">' + esc(meta.title) + '</span>' +
      '<span class="comp-key">' + esc(opts.name) + '</span>' +
      '</span>' +
      '<span class="tag tag-blue">' + esc(slotLabel) + '</span>' +
      '</span>';
    // 页签集合：全部组件有「状态/配置」；页签排序规范——配置一律最后。
    // dispatch 组件在状态与配置之间插入三个管理页签（原「路由管理」内层三视图拍平）
    let tabs = [{ name: 'state', label: '状态' }];
    if (opts.name === 'dispatch') tabs = tabs.concat(DISPATCH_TABS);
    tabs.push({ name: 'config', label: '配置', count: cnt || 0 });
    const isDispatch = opts.name === 'dispatch';
    host.innerHTML =
      breadcrumbHTML(opts) +
      Rock.comp.head.headHTML({
        titleHTML: barHTML,
        desc: esc(headDesc(opts.name, meta)),
        actions: '<button class="btn btn-sm" data-act="detail-reload">⟳ 刷新</button>',
      }) +
      Rock.comp.tabs.tabsHTML(
        tabs,
        tab,
        { act: 'detail-tab', nameAttr: 'data-tab' }
      ) +
      '<div id="detail-panel-state"' + (tab === 'state' ? '' : ' hidden') + '>' + stateCardHTML(s, opts) +
      (isDispatch ? '<div id="detail-dispatch-scale"></div>' : '') + '</div>' +
      (isDispatch
        ? '<div id="detail-panel-rules"' + (tab === 'rules' ? '' : ' hidden') + '></div>' +
          '<div id="detail-panel-upstreams"' + (tab === 'upstreams' ? '' : ' hidden') + '></div>' +
          '<div id="detail-panel-nodes"' + (tab === 'nodes' ? '' : ' hidden') + '></div>'
        : '') +
      '<div id="detail-panel-config"' + (tab === 'config' ? '' : ' hidden') + '></div>';
    // 容器内查询（components/services 两个 page 容器都有同名 panel，避免渲染错位）
    if (tab === 'config') renderConfigPanel(host.querySelector('#detail-panel-config'), opts.name, opts.type);
    // 状态页签：dispatch 组件额外挂路由数据规模卡（计数可点击直达对应管理页签）
    if (isDispatch && Rock.views.dispatch && tab === 'state') {
      Rock.views.dispatch.mountScaleCard(host.querySelector('#detail-dispatch-scale'));
    }
    // 管理页签：懒挂载路由分发对应管理视图（首次切入才拉数据；URL 直达 ?tab=rules 等同样生效）
    if (isDispatch && Rock.views.dispatch && (tab === 'rules' || tab === 'upstreams' || tab === 'nodes')) {
      Rock.views.dispatch.mountTab(host.querySelector('#detail-panel-' + tab), tab);
    }
  }

  // 页签解析：state 缺省；config 通用；dispatch 组件另有 rules / upstreams / nodes
  // 三个管理页签，均经 ?tab= 指定（非法值回落 state）
  function resolveTab(opts) {
    if (opts.tab === 'config') return 'config';
    if (opts.name === 'dispatch' &&
        (opts.tab === 'rules' || opts.tab === 'upstreams' || opts.tab === 'nodes')) return opts.tab;
    return 'state';
  }

  // 切换页签（同步 URL hash，刷新不丢）
  function setTab(opts, tab) {
    const base = '#/' + (opts.type === 'service' ? 'services' : 'components') + '/' + opts.name;
    location.hash = tab === 'state' ? base : base + '?tab=' + tab;
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
        notify.error((enabling ? '开启失败：' : '关闭失败：') + (res.error || '未知错误'));
        return false;
      }
      notify.success((enabling ? '已启用 ' : '已关闭 ') + meta.title + '（已即时生效，无需重启）');
      load({ type: opts.type, name: name, tab: opts.tab, silent: true, force: true });
      return true;
    } catch (e) {
      notify.error((enabling ? '开启失败：' : '关闭失败：') + e.message);
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
