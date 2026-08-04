/* ==========================================================================
 * RockSys 管理控制台 - views/config.js 配置页
 * 分组标签页 + 行内编辑保存 / 恢复默认 / 掩码切换 / 需重启置灰。
 * 同时导出共享配置项渲染器（组件页展开配置区复用）。
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
  const groupOf = Rock.state.groupOf;
  const normalizeConfigList = Rock.state.normalizeConfigList;
  const api = Rock.api;
  const toast = Rock.ui.toast;
  const confirmDialog = Rock.ui.confirmDialog;
  const skeletonHTML = Rock.ui.skeletonHTML;
  const noteUpdated = Rock.ui.noteUpdated;

  // 配置编辑/掩码/容器注册（组件页展开配置区与配置页共用同一状态）
  const configEditing = { key: null, value: '' };
  const configMask = {};        // key → 是否明文
  const configContainers = new Set();
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

  // 懒加载全量配置（组件页展开配置区时调用）
  async function loadList() {
    if (store.configListLoaded) return;
    try {
      const list = await api.get('/admin/config/list');
      store.configList = normalizeConfigList(list);
      store.configListLoaded = true;
      store.configUnavailable = false;
    } catch (e) {
      if (e.status === 404) store.configUnavailable = true;
      store.configList = [];
      store.configListLoaded = true;
    }
  }

  // 构建配置分组（固定顺序，网关组自动补齐底座项）
  function buildConfigGroups() {
    const orderMap = {};
    PREFIX_GROUPS.forEach(g => {
      if (!orderMap[g.name]) orderMap[g.name] = { name: g.name, label: g.label, items: [] };
    });
    const other = { name: 'other', label: '其他', items: [] };
    (store.configList || []).forEach(item => {
      const g = groupOf(item.key);
      if (g.name === 'other') other.items.push(item);
      else if (orderMap[g.name]) orderMap[g.name].items.push(item);
    });
    const groups = [];
    PREFIX_GROUPS.forEach(g => {
      if (orderMap[g.name] && orderMap[g.name].items.length) groups.push(orderMap[g.name]);
    });
    if (other.items.length) groups.push(other);
    // 网关组补齐底座项（GET /admin/config 提供）
    const gw = groups.find(g => g.name === 'gateway');
    if (!gw) groups.unshift({ name: 'gateway', label: '网关', items: [] });
    const b = store.base || {};
    const synth = [
      { key: 'ROCKSYS_LISTEN', title: '监听地址', defval: ':8080', current: b.listen || '', example: ':8080' },
      { key: 'ROCKSYS_UPSTREAM', title: '默认后端', defval: '', current: b.upstream || '', example: 'http://127.0.0.1:9000' },
      { key: 'ROCKSYS_TIMEOUT', title: '转发超时（秒）', defval: '5', current: b.timeout != null ? String(b.timeout) : '', example: '5' },
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
        '<div class="page-head"><div><div class="page-title">配置</div><div class="page-desc">可视化查看与修改全部配置</div></div>' +
        '<button class="btn btn-sm" data-act="config-reload">⟳ 手动刷新</button></div>' +
        '<div class="card"><div class="empty">管理接口不可达，无法加载配置。' +
        '<br><button class="btn btn-sm btn-primary" data-act="config-reload">重试</button></div></div>';
      return;
    }
    if (!store.configListLoaded && !store.switchesLoaded && !store.baseLoaded) { skeleton(); return; }
    const groups = buildConfigGroups();
    if (!configActiveGroup || !groups.some(g => g.name === configActiveGroup)) {
      configActiveGroup = groups.length ? groups[0].name : 'gateway';
    }
    const tabs = groups.map(g =>
      '<div class="tab' + (g.name === configActiveGroup ? ' active' : '') + '" data-act="cfg-tab" data-name="' + esc(g.name) + '">' +
      esc(g.label) + '<span class="tab-count">' + g.items.length + '</span></div>'
    ).join('');
    host.innerHTML =
      '<div class="page-head">' +
      '<div><div class="page-title">配置</div><div class="page-desc">可视化查看与修改全部配置，保存即即时生效，无需重启</div></div>' +
      '<button class="btn btn-sm" data-act="config-reload">⟳ 手动刷新</button>' +
      '</div>' +
      (store.configUnavailable ? '<div class="alert alert-warning">配置接口（/admin/config/list）暂不可用或网关版本不支持，当前展示底座配置。修改项保存仍可用。</div>' : '') +
      '<div class="tabs">' + tabs + '</div>' +
      '<div class="card"><div id="config-group-panel"></div></div>';
    const panel = $('#config-group-panel');
    const active = groups.find(g => g.name === configActiveGroup);
    renderConfigItems(panel, active ? active.items : []);
  }

  function findConfig(key) {
    return (store.configList || []).find(c => c.key === key);
  }

  function updateConfigCurrent(key, val) {
    const it = findConfig(key);
    if (it) it.current = String(val);
  }

  function maskText(v) {
    v = String(v == null ? '' : v);
    if (!v) return '（空）';
    return '••••••••';
  }

  // 配置项说明（data-tip 提示）
  function configTip(item) {
    const lines = [];
    if (item.title) lines.push('说明：' + item.title);
    lines.push('配置名：' + item.key);
    lines.push('默认值：' + (item.defval === '' ? '（空）' : item.defval));
    if (item.example) lines.push('示例：' + item.example);
    return lines.join('\n');
  }

  // 共享配置行 HTML（配置页 + 组件展开配置区共用）
  function configRowHTML(item) {
    const key = item.key;
    const sensitive = Rock.state.isSensitiveKey(key);
    const restart = Rock.state.RESTART_KEYS.indexOf(key) >= 0;
    const editing = configEditing.key === key;
    const showMask = sensitive && configMask[key] !== true;
    const current = String(item.current == null ? '' : item.current);
    let display = current;
    if (sensitive && showMask) display = maskText(current);
    else if (display === '') display = '（空）';
    const valueCell = editing
      ? '<input class="input input-sm cfg-edit-input" data-k="' + esc(key) + '" value="' + esc(configEditing.value) + '">'
      : '<span class="cfg-current" title="' + esc(current) + '">' + esc(display) + '</span>';
    let actions = '';
    if (!restart) {
      if (editing) {
        actions += '<button class="btn btn-sm btn-primary" data-act="cfg-save" data-k="' + esc(key) + '">保存</button>';
        actions += '<button class="btn btn-sm" data-act="cfg-cancel">取消</button>';
      } else {
        actions += '<button class="btn btn-sm" data-act="cfg-edit" data-k="' + esc(key) + '">修改</button>';
        actions += '<button class="btn btn-sm btn-text" data-act="cfg-reset" data-k="' + esc(key) + '">恢复默认</button>';
      }
    }
    if (sensitive) {
      actions += '<button class="btn btn-sm btn-text" data-act="cfg-mask" data-k="' + esc(key) + '">' + (showMask ? '显示' : '隐藏') + '</button>';
    }
    actions += '<span class="cfg-tip" data-tip="' + esc(configTip(item)) + '">说明</span>';
    return '<div class="cfg-row' + (restart ? ' is-restart' : '') + (editing ? ' is-editing' : '') + '" data-key="' + esc(key) + '">' +
      '<div class="cfg-info">' +
      '<div class="cfg-title">' + esc(item.title || key) +
      (sensitive ? '<span class="tag tag-orange">敏感</span>' : '') +
      (restart ? '<span class="tag tag-gray">需重启后生效</span>' : '') +
      '</div>' +
      '<div class="cfg-key">' + esc(key) + '</div>' +
      '</div>' +
      '<div class="cfg-value">' + valueCell + '</div>' +
      '<div class="cfg-actions">' + actions + '</div>' +
      '</div>';
  }

  // 共享配置项渲染器（注册到 configContainers，供全局刷新）
  function renderConfigItems(container, items, opts) {
    items = items || [];
    container._items = items;
    container._compact = !!(opts && opts.compact);
    configContainers.add(container);
    if (!items.length) {
      container.innerHTML = '<div class="empty" style="padding:24px 8px">该分组暂无配置项</div>';
      return;
    }
    container.innerHTML = '<div class="config-table">' + items.map(configRowHTML).join('') + '</div>';
    const inp = container.querySelector('.cfg-edit-input');
    if (inp) {
      inp.addEventListener('input', () => { configEditing.value = inp.value; });
      inp.addEventListener('keydown', e => {
        if (e.key === 'Enter') saveEdit(configEditing.key);
        if (e.key === 'Escape') cancelEdit();
      });
      inp.focus();
    }
  }

  function refreshAllConfigContainers() {
    configContainers.forEach(c => {
      if (c.isConnected && !c.hidden && c._items) {
        renderConfigItems(c, c._items, { compact: c._compact });
      }
    });
  }

  function startEdit(key) {
    const it = findConfig(key);
    if (!it) return;
    configEditing.key = key;
    configEditing.value = it.current;
    refreshAllConfigContainers();
  }

  function cancelEdit() {
    configEditing.key = null;
    configEditing.value = '';
    refreshAllConfigContainers();
  }

  async function saveEdit(key) {
    if (configEditing.key !== key) return;
    const val = configEditing.value;
    try {
      const res = await api.put('/admin/config')({ [key]: val });
      if (res && res.ok === false) {
        toast('保存失败：' + (res.error || '未知错误'), 'error');
        return;
      }
      updateConfigCurrent(key, val);
      configEditing.key = null;
      configEditing.value = '';
      toast('⚡ 已即时生效，无需重启', 'success');
      refreshAllConfigContainers();
      // 若修改的是底座配置，同步刷新概览信息
      if (key.indexOf('ROCKSYS_') === 0) {
        api.get('/admin/config').then(base => {
          if (base) { store.base = base; }
        }).catch(() => {});
      }
    } catch (e) {
      toast('保存失败：' + e.message, 'error');
    }
  }

  async function resetItem(key) {
    const it = findConfig(key);
    if (!it) return;
    const ok = await confirmDialog({
      title: '恢复默认值',
      message: '确定将 <code>' + esc(key) + '</code> 恢复为默认值 <code>' + esc(it.defval === '' ? '空' : it.defval) + '</code> 吗？',
      confirmText: '恢复默认',
      danger: true,
    });
    if (!ok) return;
    try {
      const res = await api.put('/admin/config')({ [key]: it.defval });
      if (res && res.ok === false) throw new Error(res.error || '未知错误');
      updateConfigCurrent(key, it.defval);
      toast('⚡ 已恢复默认值并即时生效', 'success');
      refreshAllConfigContainers();
    } catch (e) {
      toast('恢复失败：' + e.message, 'error');
    }
  }

  function toggleMask(key) {
    configMask[key] = !configMask[key];
    refreshAllConfigContainers();
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
    loadList,
    renderConfigItems,
    startEdit,
    cancelEdit,
    saveEdit,
    resetItem,
    toggleMask,
    setActiveTab,
  };
})();
