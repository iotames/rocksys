/* ==========================================================================
 * RockSys 管理控制台 - components/configEditor.js 配置项渲染/编辑组件
 * 渲染配置行（掩码 / 枚举 / 需重启 / 编辑态），行内编辑保存 / 恢复默认 /
 * 掩码切换，并注册容器供全局刷新（配置页 + 组件页展开配置区共用同一状态）。
 * 依赖 Rock.api / Rock.ui.toast / Rock.ui.confirmDialog / Rock.state /
 * Rock.comp.select / Rock.util.esc。挂载到全局命名空间 window.Rock.comp.configEditor。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const esc = Rock.util.esc;
  const store = Rock.state.store;
  const normalizeConfigList = Rock.state.normalizeConfigList;
  const api = Rock.api;
  const toast = Rock.ui.toast;
  const confirmDialog = Rock.ui.confirmDialog;

  // 配置编辑/掩码/容器注册（组件页展开配置区与配置页共用同一状态）
  const configEditing = { key: null, value: '' };
  const configMask = {};        // key → 是否明文
  const configContainers = new Set();

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
    const enumOptions = Rock.state.ENUM_KEYS[key];
    let valueCell;
    if (editing) {
      if (enumOptions && enumOptions.length) {
        // 枚举值：下拉选择，不允许手填
        valueCell = '<select class="select select-sm cfg-edit-input" data-k="' + esc(key) + '">' +
          Rock.comp.select.options(enumOptions.map(o => [o, o]), String(configEditing.value)) + '</select>';
      } else {
        valueCell = '<input class="input input-sm cfg-edit-input" data-k="' + esc(key) + '" value="' + esc(configEditing.value) + '">';
      }
    } else {
      valueCell = '<span class="cfg-current" title="' + esc(current) + '">' + esc(display) + '</span>';
    }
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
  function render(container, items, opts) {
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
      if (inp.tagName === 'SELECT') {
        inp.addEventListener('change', () => { configEditing.value = inp.value; });
      } else {
        inp.addEventListener('input', () => { configEditing.value = inp.value; });
      }
      inp.addEventListener('keydown', e => {
        if (e.key === 'Enter') saveEdit(configEditing.key);
        if (e.key === 'Escape') cancelEdit();
      });
      inp.focus();
    }
  }

  function refresh() {
    configContainers.forEach(c => {
      if (c.isConnected && !c.hidden && c._items) {
        render(c, c._items, { compact: c._compact });
      }
    });
  }

  function startEdit(key) {
    const it = findConfig(key);
    if (!it) return;
    configEditing.key = key;
    configEditing.value = it.current;
    refresh();
  }

  function cancelEdit() {
    configEditing.key = null;
    configEditing.value = '';
    refresh();
  }

  async function saveEdit(key) {
    if (configEditing.key !== key) return;
    // 从当前编辑控件（input / select）取最新值：键盘 Enter 保存时 change 事件可能未触发
    const inp = document.querySelector('.cfg-edit-input');
    if (inp && inp.value !== undefined) configEditing.value = inp.value;
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
      refresh();
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
      refresh();
    } catch (e) {
      toast('恢复失败：' + e.message, 'error');
    }
  }

  function toggleMask(key) {
    configMask[key] = !configMask[key];
    refresh();
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

  window.Rock.comp.configEditor = {
    render,
    startEdit,
    cancelEdit,
    saveEdit,
    resetItem,
    toggleMask,
    refresh,
    loadList,
  };
})();