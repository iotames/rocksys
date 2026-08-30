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
      invalidateSearchIndex(); // 配置快照已更新，重建搜索索引
      noteUpdated();
    } catch (e) {
      store.configFailed = !store.configListLoaded;
      if (e.status === 404) {
        store.configList = [];
        store.configListLoaded = true;
        store.configUnavailable = true;
      } else if (!opts.silent && e.status !== 0) {
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

  // ── 配置项搜索工具栏（KEY 优先 / 标题；纯前端过滤，无新接口）──────────
  // 单一编辑路径：结果点击 = 定位（切分组/跳组件页）+ 滚动高亮 + 自动进入行内编辑，
  // 不另造弹窗编辑（避免与行内编辑形成需永久双维护的对偶实现）。
  const cfgSearch = { q: '', sel: 0, results: [], timer: null };
  let cfgSearchIndex = null; // 渲染后构建：configList 未变则复用

  function invalidateSearchIndex() { cfgSearchIndex = null; }

  // 扁平搜索索引：全量配置项 + 归属（分组名 / 组件·服务名与路由）
  function buildSearchIndex() {
    if (cfgSearchIndex) return cfgSearchIndex;
    const owners = Object.keys(COMPONENT_PREFIX).map(name => ({
      name,
      prefix: COMPONENT_PREFIX[name],
      isService: COMPONENT_ORDER.indexOf(name) < 0 && SERVICE_ORDER.indexOf(name) >= 0,
    }));
    cfgSearchIndex = (store.configList || []).map(item => {
      const o = owners.find(x => item.key.indexOf(x.prefix) === 0);
      if (o) {
        const meta = Rock.state.componentMeta(o.name, o.isService ? 'service' : 'middleware');
        return {
          item,
          ownerLabel: meta.title,
          ownerKind: o.isService ? '服务' : '组件',
          route: (o.isService ? 'services/' : 'components/') + o.name + '?tab=config',
        };
      }
      const g = groupOf(item.key);
      return { item, ownerLabel: g.label, ownerKind: '分组', group: g.name };
    }).filter(e => e.item.key);
    return cfgSearchIndex;
  }

  // 匹配评分：KEY 前缀 0 < KEY 包含 1 < 标题包含 2 < 说明(Usage) 包含 3；-1 = 不匹配。
  // 综合搜索：历史原因部分配置项的说明写在 Title、部分在 Usage，两处都参与匹配才不漏。
  function searchScore(e, q) {
    const k = e.item.key.toUpperCase();
    const t = String(e.item.title || '').toUpperCase();
    if (k.indexOf(q) === 0) return 0;
    if (k.indexOf(q) >= 0) return 1;
    if (t.indexOf(q) >= 0) return 2;
    if (String(e.item.example || '').toUpperCase().indexOf(q) >= 0) return 3;
    return -1;
  }

  function hiText(text, q) {
    const s = String(text || '');
    const i = s.toUpperCase().indexOf(q);
    if (i < 0) return esc(s);
    return esc(s.slice(0, i)) + '<mark>' + esc(s.slice(i, i + q.length)) + '</mark>' + esc(s.slice(i + q.length));
  }

  // Usage 命中摘录：取命中位置前后各 ~24 字符的窗口，超长以 … 截断
  function hiSnippet(text, q) {
    const s = String(text || '');
    const i = s.toUpperCase().indexOf(q);
    if (i < 0) return '';
    const from = Math.max(0, i - 24), to = Math.min(s.length, i + q.length + 24);
    return (from > 0 ? '…' : '') + hiText(s.slice(from, to), q) + (to < s.length ? '…' : '');
  }

  function searchDropHTML() {
    const rs = cfgSearch.results;
    if (!cfgSearch.q) return '';
    if (!rs.length) return '<div class="cfg-search-empty">无匹配配置项（支持 KEY 与标题，KEY 优先）</div>';
    const rows = rs.map((e, i) => {
      const it = e.item;
      const sensitive = Rock.state.isSensitiveKey(it.key);
      const restart = Rock.state.RESTART_KEYS.indexOf(it.key) >= 0;
      const display = sensitive ? maskOf(it.current) : (it.current === '' ? '（空）' : it.current);
      return '<div class="cfg-search-item' + (i === cfgSearch.sel ? ' is-sel' : '') + '" data-key="' + esc(it.key) + '">' +
        '<div class="cfg-search-main">' +
        '<span class="cfg-search-key">' + hiText(it.key, cfgSearch.q) + '</span>' +
        '<span class="cfg-search-title">' + hiText(it.title, cfgSearch.q) + '</span>' +
        '</div>' +
        '<div class="cfg-search-meta">' +
        '<span class="tag tag-blue">' + esc(e.ownerKind + ' · ' + e.ownerLabel) + '</span>' +
        (restart ? '<span class="tag tag-gray">需重启</span>' : '') +
        (sensitive ? '<span class="tag tag-orange">敏感</span>' : '') +
        '<span class="cfg-search-val mono">' + esc(display) + '</span>' +
        '<span class="cfg-search-go">回车定位并编辑 →</span>' +
        '</div>' +
        (hiSnippet(it.example, cfgSearch.q)
          ? '<div class="cfg-search-usage">说明：' + hiSnippet(it.example, cfgSearch.q) + '</div>'
          : '') +
        '</div>';
    }).join('');
    const total = buildSearchIndex().filter(e => searchScore(e, cfgSearch.q) >= 0).length;
    return rows + (total > rs.length
      ? '<div class="cfg-search-more">共 ' + total + ' 项匹配，仅展示前 ' + rs.length + ' 项，请细化关键字</div>'
      : '');
  }

  function maskOf(v) { return String(v == null ? '' : v) === '' ? '（空）' : '••••••••'; }

  function cfgSearchHTML() {
    return '<div class="card cfg-searchbar"><div class="cfg-search-wrap">' +
      '<input id="cfg-search" class="input" placeholder="🔍 搜索配置项 KEY / 标题，选择结果定位并编辑" autocomplete="off" spellcheck="false">' +
      '<div id="cfg-search-drop" class="cfg-search-drop" hidden></div>' +
      '</div></div>';
  }

  function runCfgSearch(q) {
    cfgSearch.q = q.trim().toUpperCase();
    cfgSearch.sel = 0;
    const idx = buildSearchIndex();
    cfgSearch.results = cfgSearch.q
      ? idx.map(e => ({ e, s: searchScore(e, cfgSearch.q) }))
          .filter(x => x.s >= 0)
          .sort((a, b) => a.s - b.s || a.e.item.key.localeCompare(b.e.item.key))
          .slice(0, 20).map(x => x.e)
      : [];
    const drop = $('#cfg-search-drop');
    if (!drop) return;
    drop.innerHTML = searchDropHTML();
    drop.hidden = !cfgSearch.q;
  }

  // 定位：全局项切分组页签后滚动高亮 + 自动入编辑；组件/服务项跳转对应页面配置页签
  function locateConfig(key) {
    const drop = $('#cfg-search-drop');
    if (drop) drop.hidden = true;
    $('#cfg-search').value = '';
    cfgSearch.q = '';
    const e = buildSearchIndex().find(x => x.item.key === key);
    if (!e) return;
    if (e.group) {
      if (configActiveGroup !== e.group) {
        configActiveGroup = e.group;
        render(); // 重建页签与面板后再定位
      }
      if (!ce.locateAndEdit(key)) toast('未找到配置项 ' + key + '，请刷新页面后重试', 'warn');
    } else {
      store.pendingCfgLocate = key; // detail.js 渲染配置页签后消费
      window.location.hash = '#/' + e.route;
    }
  }

  function bindCfgSearch(host) {
    const inp = host.querySelector('#cfg-search');
    const drop = host.querySelector('#cfg-search-drop');
    if (!inp || !drop) return;
    inp.addEventListener('input', function () {
      clearTimeout(cfgSearch.timer);
      const v = inp.value;
      cfgSearch.timer = setTimeout(function () { runCfgSearch(v); }, 150);
    });
    inp.addEventListener('focus', function () { if (cfgSearch.q) drop.hidden = false; });
    inp.addEventListener('keydown', function (e) {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        if (!cfgSearch.results.length) return;
        cfgSearch.sel = (cfgSearch.sel + (e.key === 'ArrowDown' ? 1 : cfgSearch.results.length - 1)) % cfgSearch.results.length;
        drop.innerHTML = searchDropHTML();
        const sel = drop.querySelector('.cfg-search-item.is-sel');
        if (sel) sel.scrollIntoView({ block: 'nearest' });
        return;
      }
      if (e.key === 'Enter') {
        const it = cfgSearch.results[cfgSearch.sel];
        if (it) locateConfig(it.item.key);
        return;
      }
      if (e.key === 'Escape') {
        inp.value = '';
        cfgSearch.q = '';
        drop.hidden = true;
      }
    });
    drop.addEventListener('mousedown', function (e) {
      // mousedown：先于 input blur 触发，保证点击结果行可靠
      const row = e.target.closest('.cfg-search-item[data-key]');
      if (row) { e.preventDefault(); locateConfig(row.getAttribute('data-key')); }
    });
    inp.addEventListener('blur', function () { setTimeout(function () { drop.hidden = true; }, 120); });
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
    // 'proxy' 为功能页签（非配置分组），不参与默认分组回退
    if (!configActiveGroup || (configActiveGroup !== 'proxy' && !groups.some(g => g.name === configActiveGroup))) {
      configActiveGroup = groups.length ? groups[0].name : 'gateway';
    }
    // 可信代理为功能页签（非配置分组）：追加在配置分组之后
    const tabItems = groups.map(g => ({ name: g.name, label: g.label, count: g.items.length }));
    tabItems.push({ name: 'proxy', label: '可信代理' });
    const tabs = Rock.comp.tabs.tabsHTML(
      tabItems,
      configActiveGroup,
      { act: 'cfg-tab' }
    );
    host.innerHTML =
      Rock.comp.head.headHTML({
        title: '全局配置',
        desc: '网关与全局基础设施配置（保存即即时生效，无需重启）；组件/服务各自的配置请前往对应页面',
        actions: '<button class="btn btn-sm" data-act="config-reload">⟳ 手动刷新</button>',
      }) +
      cfgSearchHTML() +
      (store.configUnavailable ? '<div class="alert alert-warning">配置接口（/admin/config/list）暂不可用或网关版本不支持，当前展示底座配置。修改项保存仍可用。</div>' : '') +
      tabs +
      '<div class="card"><div id="config-group-panel"></div></div>' +
      '<div class="card">' +
      '<div class="card-title">组件配置 <span class="card-sub">数据流组件 · 点击进入对应页面查看与修改</span></div>' +
      '<div class="cfg-link-grid">' + linkGridHTML('component') + '</div>' +
      '<div class="card-title" style="margin-top:18px">服务配置 <span class="card-sub">独立服务 · 点击进入对应页面查看与修改</span></div>' +
      '<div class="cfg-link-grid">' + linkGridHTML('service') + '</div>' +
      '</div>';
    bindCfgSearch(host);
    const panel = $('#config-group-panel');
    if (configActiveGroup === 'proxy') {
      // 可信代理页签：外挂文件在线编辑（views/proxy.js，无独立路由）
      Rock.views.proxy.renderPanel(panel);
      return;
    }
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
