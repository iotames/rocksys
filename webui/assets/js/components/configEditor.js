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

  // 前端值校验（保存前拦截明显非法输入；返回错误文案，空串 = 通过）
  function validateValue(key, val) {
    const v = String(val == null ? '' : val).trim();
    if (Rock.state.isIntKey(key)) {
      if (!/^\d+$/.test(v)) return '需为非负整数';
      return '';
    }
    const enums = Rock.state.ENUM_KEYS[key];
    if (enums && enums.length && enums.indexOf(v) < 0) {
      return '需为以下之一：' + enums.join(' / ');
    }
    switch (key) {
      case 'ROCKSYS_LISTEN':
      case 'ROCKSYS_ADMIN':
      case 'REGISTRY_ADDR':
        if (!/^[A-Za-z0-9_.\-]*:\d{1,5}$/.test(v)) return '格式需为 host:port（如 127.0.0.1:19527 或 :8080）';
        break;
      case 'ROCKSYS_UPSTREAM':
        if (!/^https?:\/\/\S+$/i.test(v)) return '需为 http(s):// 开头的后端地址（如 http://127.0.0.1:9000）';
        break;
      case 'SHIELD_IP_WHITELIST': {
        if (!v) break; // 空 = 不限，合法
        const bad = v.split(',').map(s => s.trim()).filter(s => s && !Rock.util.validIPOrCIDR(s));
        if (bad.length) return '存在非法 IP/CIDR：' + bad.slice(0, 3).join('、');
        break;
      }
      case 'DB_PORT':
        if (!/^\d+$/.test(v) || Number(v) < 1 || Number(v) > 65535) return '端口需为 1-65535 的整数';
        break;
    }
    return '';
  }

  // 编辑控件 HTML（按配置类型选择：布尔=开关 / 枚举=下拉 / 长文本=textarea / 整数=number / 其余=文本）
  function editInputHTML(item, value) {
    const key = item.key;
    const attrs = 'class="cfg-edit-input" data-k="' + esc(key) + '"';
    if (Rock.state.isBoolKey(key)) {
      const on = String(value).trim() === 'true';
      return '<span class="cfg-switch-row">' +
        '<label class="el-switch" title="开 = true / 关 = false">' +
        '<input type="checkbox" ' + attrs + (on ? ' checked' : '') + '>' +
        '<span class="el-switch-core"></span></label>' +
        '<span class="cfg-switch-state">' + (on ? 'true（开）' : 'false（关）') + '</span>' +
        '</span>';
    }
    const enums = Rock.state.ENUM_KEYS[key];
    if (enums && enums.length) {
      // 枚举值：下拉选择，不允许手填
      return '<select ' + attrs.replace('class="', 'class="select select-sm ') + '>' +
        Rock.comp.select.options(enums.map(o => [o, o]), String(value)) + '</select>';
    }
    if (Rock.state.isIntKey(key)) {
      return '<input type="number" min="0" step="1" ' + attrs.replace('class="', 'class="input input-sm ') +
        ' value="' + esc(value) + '">';
    }
    if (Rock.state.isTextareaKey(key)) {
      return '<textarea rows="3" placeholder="' + esc(item.example || '') + '" ' +
        attrs.replace('class="', 'class="input input-sm cfg-edit-textarea ') + '>' + esc(value) + '</textarea>';
    }
    return '<input ' + attrs.replace('class="', 'class="input input-sm ') + ' value="' + esc(value) + '">';
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
    let valueCell;
    if (editing) {
      valueCell = editInputHTML(item, configEditing.value);
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
      // 编辑值同步：开关取 checked（true/false），其余取 value
      const sync = function () {
        configEditing.value = (inp.type === 'checkbox') ? (inp.checked ? 'true' : 'false') : inp.value;
        // 开关旁的 true/false 文案联动
        const st = container.querySelector('.cfg-switch-state');
        if (st) st.textContent = configEditing.value === 'true' ? 'true（开）' : 'false（关）';
      };
      const ev = (inp.tagName === 'SELECT' || inp.type === 'checkbox') ? 'change' : 'input';
      inp.addEventListener(ev, sync);
      inp.addEventListener('keydown', e => {
        if (e.key === 'Enter' && inp.tagName !== 'TEXTAREA') saveEdit(configEditing.key);
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

  // 搜索定位：滚动到目标配置行并闪烁高亮，非"需重启"项同时自动进入行内编辑。
  // 须在目标容器已渲染且可见（配置页对应分组 / 组件页配置页签）后调用；
  // 找不到该配置或行（容器未渲染）返回 false，调用方可据此兜底。
  function locateAndEdit(key) {
    if (!findConfig(key)) return false;
    if (Rock.state.RESTART_KEYS.indexOf(key) < 0) startEdit(key); // 内部 refresh() 同步重建行 DOM
    const row = document.querySelector('.cfg-row[data-key="' + key.replace(/"/g, '') + '"]');
    if (!row) return false;
    row.scrollIntoView({ block: 'center' });
    row.classList.remove('cfg-locate-flash');
    void row.offsetWidth; // 强制回流，保证连续定位时闪烁动画重放
    row.classList.add('cfg-locate-flash');
    return true;
  }

  function cancelEdit() {
    configEditing.key = null;
    configEditing.value = '';
    refresh();
  }

  async function saveEdit(key) {
    if (configEditing.key !== key) return;
    // 从当前编辑控件（input / select / checkbox）取最新值：键盘 Enter 保存时 change 事件可能未触发
    const inp = document.querySelector('.cfg-edit-input');
    if (inp) {
      configEditing.value = (inp.type === 'checkbox') ? (inp.checked ? 'true' : 'false') : inp.value;
    }
    const val = configEditing.value;
    // 前端校验：类型不符 / 格式非法直接拦截，不打后端
    const err = validateValue(key, val);
    if (err) {
      toast('保存失败：' + err, 'error');
      if (inp && inp.classList) {
        inp.classList.add('is-invalid');
        inp.addEventListener('input', function onInput() { inp.classList.remove('is-invalid'); inp.removeEventListener('input', onInput); });
      }
      return;
    }
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
      // 接口不可用（404 / 500 / 网络异常）一律置位：详情页配置页签与全局配置入口
      // 应提示"接口暂不可用"，而非误判为"该组件无独立配置项"
      store.configUnavailable = true;
      store.configList = [];
      store.configListLoaded = true;
    }
  }

  window.Rock.comp.configEditor = {
    render,
    validateValue,
    startEdit,
    cancelEdit,
    saveEdit,
    resetItem,
    toggleMask,
    refresh,
    loadList,
    locateAndEdit,
    actions: {
      'cfg-edit': function (el) { startEdit(el.getAttribute('data-k') || ''); },
      'cfg-cancel': function () { cancelEdit(); },
      'cfg-save': function (el) { saveEdit(el.getAttribute('data-k') || ''); },
      'cfg-reset': function (el) { resetItem(el.getAttribute('data-k') || ''); },
      'cfg-mask': function (el) { toggleMask(el.getAttribute('data-k') || ''); },
    },
  };
})();
