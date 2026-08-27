/* ==========================================================================
 * RockSys 管理控制台 - views/config.js 全局配置页
 * 仅保留全局基础设施配置（网关 / 数据访问 / 其他）；
 * 组件与服务的独有配置项已迁至各自页面（配置页签），此处以链接卡片引导跳转。
 * 分组标签页 + 行内编辑保存 / 恢复默认 / 掩码切换 / 需重启置灰，
 * 配置项渲染下沉到 Rock.comp.configEditor。
 * 挂载到全局命名空间 window.Rock.views.config。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const store = Rock.state.store;
  const PREFIX_GROUPS = Rock.state.PREFIX_GROUPS;
  const COMPONENT_ORDER = Rock.state.COMPONENT_ORDER;
  const SERVICE_ORDER = Rock.state.SERVICE_ORDER;
  const COMPONENT_PREFIX = Rock.state.COMPONENT_PREFIX;
  const groupOf = Rock.state.groupOf;
  const normalizeConfigList = Rock.state.normalizeConfigList;
  const api = Rock.api;
  const toast = Rock.ui.toast;
  const skeletonHTML = Rock.ui.skeletonHTML;
  const noteUpdated = Rock.ui.noteUpdated;
  const ce = Rock.comp.configEditor;

  let configActiveGroup = null;

  // 加载配置页：底座信息与全量配置并行拉取
  async function load(opts) {
    const first = !store.configListLoaded && !opts.silent;
    if (first) skeleton();
    try {
      const [base, list] = await Promise.all([
        api.get('/admin/config'),
        api.get('/admin/config/list'),
      ]);
      if (base) { store.base = base; store.baseLoaded = true; }
      store.configList = normalizeConfigList(list);
      store.configListLoaded = true;
      store.configUnavailable = false;
      noteUpdated();
    } catch (e) {
      store.configFailed = !store.configListLoaded;
      if (e.status === 404) {
        store.configList = [];
        store.configListLoaded = true;
        store.configUnavailable = true;
      } else if (opts.manual && e.status !== 0) {
        toast('配置加载失败：' + e.message, 'error');
      }
    }
    render();
  }

  function skeleton() {
    const host = $('#page-config');
    if (host) host.innerHTML = skeletonHTML(6);
  }

  // 组件/服务各自配置项数量（链接卡片角标）
  function configCountOf(name) {
    const prefix = COMPONENT_PREFIX[name];
    if (!prefix) return 0;
    return (store.configList || []).filter(c => c.key.indexOf(prefix) === 0).length;
  }

  // 配置入口链接卡片（组件 / 服务）
  function linkGridHTML(kind) {
    const order = kind === 'service' ? SERVICE_ORDER : COMPONENT_ORDER;
    const routeBase = kind === 'service' ? 'services' : 'components';
    return order.map(name => {
      const meta = Rock.state.componentMeta(name, kind === 'service' ? 'service' : 'middleware');
      const cnt = configCountOf(name);
      const route = routeBase + '/' + name + '?tab=config';
      const tag = store.configUnavailable
        ? '<span class="tag tag-gray">配置接口暂不可用</span>'
        : (cnt
          ? '<span class="tag tag-blue">' + cnt + ' 项配置</span>'
          : '<span class="tag tag-gray">无独立配置</span>');
      return '<a class="cfg-link" data-act="nav-detail" data-route="' + route + '" href="#/' + route + '">' +
        '<span class="cfg-link-name">' + esc(meta.title) + ' <i>' + esc(name) + '</i></span>' +
        '<span class="cfg-link-desc">' + esc(meta.desc || '') + '</span>' +
        tag +
        '<span class="cfg-link-go">去配置 →</span>' +
        '</a>';
    }).join('');
  }

  // 构建配置分组（固定顺序：网关 / 数据访问 / 其他）
  function buildConfigGroups() {
    const orderMap = {};
    PREFIX_GROUPS.forEach(g => {
      if (!orderMap[g.name]) orderMap[g.name] = { name: g.name, label: g.label, items: [] };
    });
    const other = { name: 'other', label: '其他', items: [] };
    (store.configList || []).forEach(item => {
      // 组件/服务专属配置已迁至各自详情页（配置页签），全局配置不再重复展示
      const isComponentKey = Object.keys(COMPONENT_PREFIX).some(name =>
        item.key.indexOf(COMPONENT_PREFIX[name]) === 0);
      if (isComponentKey) return;
      const g = groupOf(item.key);
      if (g.name === 'other') other.items.push(item);
      else if (orderMap[g.name]) orderMap[g.name].items.push(item);
    });
    const groups = [];
    const pushed = {}; // 防御：同名分组只 push 一次
    PREFIX_GROUPS.forEach(g => {
      const grp = orderMap[g.name];
      if (grp && !pushed[g.name]) {
        groups.push(grp);
        pushed[g.name] = true;
      }
    });
    if (other.items.length) groups.push(other);
    // 网关组补齐底座项（GET /admin/config 提供）
    const gw = groups.find(g => g.name === 'gateway');
    if (!gw) groups.unshift({ name: 'gateway', label: '网关', items: [] });
    const b = store.base || {};
    const synth = [
      { key: 'ROCKSYS_LISTEN', title: '监听地址', defval: ':8080', current: b.listen || '', example: ':8080' },
      { key: 'ROCKSYS_UPSTREAM', title: '默认后端', defval: '', current: b.upstream || '', example: 'http://127.0.0.1:9000' },
      { key: 'ROCKSYS_TIMEOUT', title: '转发超时（秒）', defval: '18', current: b.timeout != null ? String(b.timeout) : '', example: '18' },
      { key: 'ROCKSYS_ADMIN', title: '管理接口地址', defval: '127.0.0.1:19527', current: b.admin || '', example: '127.0.0.1:19527' },
      { key: 'ROCKSYS_CONFIG', title: '配置文件路径', defval: '.env', current: b.config_file || '', example: '.env' },
      { key: 'ROCKSYS_LOG_LEVEL', title: '日志级别', defval: 'info', current: b.log_level || '', example: 'info' },
    ];
    synth.forEach(s => {
      if (!gw.items.some(i => i.key === s.key)) gw.items.push(s);
    });
    return groups;
  }

  function render() {
    const host = $('#page-config');
    if (!host) return;
    if (store.configFailed && !store.configListLoaded) {
      host.innerHTML =
        Rock.comp.head.headHTML({
          title: '全局配置',
          desc: '网关与全局基础设施配置',
          actions: '<button class="btn btn-sm" data-act="config-reload">⟳ 手动刷新</button>',
        }) +
        Rock.comp.empty.emptyCard({
          text: '管理接口不可达，无法加载配置。',
          action: '<button class="btn btn-sm btn-primary" data-act="config-reload">重试</button>',
          br: true,
        });
      return;
    }
    if (!store.configListLoaded && !store.switchesLoaded && !store.baseLoaded) { skeleton(); return; }
    const groups = buildConfigGroups();
    if (!configActiveGroup || !groups.some(g => g.name === configActiveGroup)) {
      configActiveGroup = groups.length ? groups[0].name : 'gateway';
    }
    const tabs = Rock.comp.tabs.tabsHTML(
      groups.map(g => ({ name: g.name, label: g.label, count: g.items.length })),
      configActiveGroup,
      { act: 'cfg-tab' }
    );
    host.innerHTML =
      Rock.comp.head.headHTML({
        title: '全局配置',
        desc: '网关与全局基础设施配置（保存即即时生效，无需重启）；组件/服务各自的配置请前往对应页面',
        actions: '<button class="btn btn-sm" data-act="config-reload">⟳ 手动刷新</button>',
      }) +
      (store.configUnavailable ? '<div class="alert alert-warning">配置接口（/admin/config/list）暂不可用或网关版本不支持，当前展示底座配置。修改项保存仍可用。</div>' : '') +
      '<div class="tabs">' + tabs + '</div>' +
      '<div class="card"><div id="config-group-panel"></div></div>' +
      '<div class="card">' +
      '<div class="card-title">组件配置 <span class="card-sub">数据流组件 · 点击进入对应页面查看与修改</span></div>' +
      '<div class="cfg-link-grid">' + linkGridHTML('component') + '</div>' +
      '<div class="card-title" style="margin-top:18px">服务配置 <span class="card-sub">独立服务 · 点击进入对应页面查看与修改</span></div>' +
      '<div class="cfg-link-grid">' + linkGridHTML('service') + '</div>' +
      '</div>';
    const panel = $('#config-group-panel');
    const active = groups.find(g => g.name === configActiveGroup);
    ce.render(panel, active ? active.items : []);
  }

  // 切换分组标签（main 的事件委托调用）
  function setActiveTab(name) {
    configActiveGroup = name;
    render();
  }

  window.Rock.views.config = {
    load,
    render,
    skeleton,
    setActiveTab,
    actions: {
      'config-reload': function () { load({ manual: true }); },
      'cfg-tab': function (el) { setActiveTab(el.getAttribute('data-name') || ''); },
    },
  };
})();
