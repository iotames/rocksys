/* ==========================================================================
 * RockSys 管理控制台 - views/components.js 组件页
 * 全部组件卡片（开关 / 状态色点 / 运行信息 / 可展开配置区），
 * 启停经二次确认后调用 /admin/switch/on|off，失败透出 error 原文。
 * 配置区复用 Rock.views.config 的共享渲染器。
 * 挂载到全局命名空间 window.Rock.views.components。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const fmtDateTime = Rock.util.fmtDateTime;
  const debounce = Rock.util.debounce;
  const store = Rock.state.store;
  const COMPONENT_META = Rock.state.COMPONENT_META;
  const COMPONENT_ORDER = Rock.state.COMPONENT_ORDER;
  const COMPONENT_PREFIX = Rock.state.COMPONENT_PREFIX;
  const normalizeSwitches = Rock.state.normalizeSwitches;
  const api = Rock.api;
  const toast = Rock.ui.toast;
  const confirmDialog = Rock.ui.confirmDialog;
  const skeletonHTML = Rock.ui.skeletonHTML;
  const noteUpdated = Rock.ui.noteUpdated;

  // 页内筛选状态
  let compFilter = { kind: 'all', q: '' };

  async function load(opts) {
    const first = !store.switchesLoaded && !opts.silent;
    if (first) skeleton();
    try {
      const switches = await api.get('/admin/switch/list');
      store.switches = normalizeSwitches(switches);
      store.switchesLoaded = true;
      noteUpdated();
    } catch (e) {
      store.componentsFailed = !store.switchesLoaded;
      if (opts.manual && e.status !== 0) toast('组件列表加载失败：' + e.message, 'error');
    }
    render();
  }

  function skeleton() {
    const host = $('#page-components');
    if (host) host.innerHTML = skeletonHTML(5);
  }

  function compStateMeta(s) {
    if (s.state === 'enabled') return { text: '已启用', dot: 'dot-ok', tag: 'tag-green' };
    if (s.state === 'draining') return { text: '切换中', dot: 'dot-warn', tag: 'tag-orange' };
    return { text: '已关闭', dot: 'dot-off', tag: 'tag-gray' };
  }

  function compCardHTML(s) {
    const meta = COMPONENT_META[s.name] || {
      title: s.name,
      desc: '',
      slotLabel: s.kind === 'component' ? '独立服务' : '链中间件',
    };
    const slotLabel = s.kind === 'component' ? '独立服务' : (meta.slotLabel || '链中间件');
    const st = compStateMeta(s);
    const msgBad = /fail|error|timeout/i.test(s.message);
    return '<div class="comp-card" data-name="' + esc(s.name) + '">' +
      '<div class="comp-head">' +
      '<span class="dot ' + st.dot + '"></span>' +
      '<span class="comp-name">' + esc(meta.title) + '</span>' +
      '<span class="comp-key">' + esc(s.name) + '</span>' +
      '<span class="tag ' + st.tag + '">' + esc(st.text) + '</span>' +
      '<span class="tag tag-blue">' + esc(slotLabel) + '</span>' +
      '<div class="comp-actions">' +
      '<label class="el-switch" title="' + esc(st.text) + '">' +
      '<input type="checkbox" data-act="comp-toggle" data-name="' + esc(s.name) + '"' +
      (s.state === 'enabled' ? ' checked' : '') +
      (s.state === 'draining' ? ' disabled' : '') + '>' +
      '<span class="el-switch-core"></span></label>' +
      '<button class="btn btn-sm btn-text" data-act="comp-config" data-name="' + esc(s.name) + '">配置</button>' +
      '</div></div>' +
      '<div class="comp-desc">' + esc(meta.desc) + '</div>' +
      '<div class="comp-meta">' +
      '<span>启用时间 <b>' + esc(fmtDateTime(s.started_at)) + '</b></span>' +
      '<span>最近切换 <b>' + esc(fmtDateTime(s.last_switch_at)) + '</b></span>' +
      (s.message ? '<span class="' + (msgBad ? 'text-danger' : '') + '">状态：' + esc(s.message) + '</span>' : '') +
      '</div>' +
      '<div class="comp-config" data-config-panel data-name="' + esc(s.name) + '" hidden></div>' +
      '</div>';
  }

  function render() {
    const host = $('#page-components');
    if (!host) return;
    if (store.componentsFailed && !store.switchesLoaded) {
      host.innerHTML =
        '<div class="card"><div class="empty">管理接口不可达，无法加载组件列表。' +
        '<br><button class="btn btn-sm btn-primary" data-act="components-reload">重试</button></div></div>';
      return;
    }
    if (!store.switchesLoaded) { skeleton(); return; }
    let list = store.switches.slice();
    if (compFilter.kind !== 'all') {
      list = list.filter(s => (compFilter.kind === 'component' ? s.kind === 'component' : s.kind === 'middleware'));
    }
    if (compFilter.q) {
      const q = compFilter.q.toLowerCase();
      list = list.filter(s => {
        const meta = COMPONENT_META[s.name];
        return s.name.indexOf(q) >= 0 || (meta && (meta.title || '').indexOf(q) >= 0);
      });
    }
    list.sort((a, x) => {
      const ia = COMPONENT_ORDER.indexOf(a.name);
      const ix = COMPONENT_ORDER.indexOf(x.name);
      return (ia < 0 ? 999 : ia) - (ix < 0 ? 999 : ix);
    });
    const hasMq = store.switches.some(c => c.name === 'mq');
    const kindOpts = [
      ['all', '全部'],
      ['middleware', '链中间件'],
      ['component', '独立组件'],
    ].map(o => '<option value="' + o[0] + '"' + (compFilter.kind === o[0] ? ' selected' : '') + '>' + o[1] + '</option>').join('');

    host.innerHTML =
      '<div class="page-head">' +
      '<div><div class="page-title">组件</div><div class="page-desc">全部组件（默认 12 个，消息组件按配置装配）一键启停，操作即时生效</div></div>' +
      '<button class="btn btn-sm" data-act="components-reload">⟳ 刷新</button>' +
      '</div>' +
      '<div class="filter-bar">' +
      '<select class="select select-sm" id="comp-kind">' + kindOpts + '</select>' +
      '<input class="input input-sm" id="comp-search" placeholder="搜索组件名" value="' + esc(compFilter.q) + '">' +
      '<span class="filter-spacer"></span>' +
      '<span class="muted">已启用 <b class="text-danger" style="color:var(--ok-light)">' + list.filter(s => s.state === 'enabled').length + '</b> · 已关闭 ' + list.filter(s => s.state === 'disabled').length + '</span>' +
      (hasMq ? '' : '<span class="tag tag-gray">消息组件未装配</span>') +
      '</div>' +
      (list.length
        ? '<div class="comp-grid">' + list.map(compCardHTML).join('') + '</div>'
        : '<div class="card"><div class="empty">没有符合条件的组件</div></div>');

    // 绑定筛选
    const kindSel = $('#comp-kind');
    kindSel.addEventListener('change', () => { compFilter.kind = kindSel.value; render(); });
    const search = $('#comp-search');
    search.addEventListener('input', debounce(() => { compFilter.q = search.value.trim(); render(); }, 250));
  }

  // 启停组件（二次确认 → 请求 → Toast → 刷新）
  async function toggle(name, enabling) {
    const meta = COMPONENT_META[name] || { title: name };
    const ok = await confirmDialog({
      title: (enabling ? '开启' : '关闭') + '组件 · ' + meta.title,
      message:
        '<p>' + (enabling
          ? '开启后将参与请求链路，相关规则立即生效。'
          : '关闭后请求将绕过该环节直通下一级，转发不会中断。') + '</p>' +
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
      load({ silent: true });
      return true;
    } catch (e) {
      toast((enabling ? '开启失败：' : '关闭失败：') + e.message, 'error');
      return false;
    }
  }

  // 展开 / 收起组件配置区（复用配置页共享渲染器）
  async function toggleConfig(name) {
    const panel = document.querySelector('[data-config-panel][data-name="' + name + '"]');
    if (!panel) return;
    if (!panel.hidden) { panel.hidden = true; return; }
    panel.hidden = false;
    if (!store.configListLoaded) await Rock.views.config.loadList();
    const prefix = COMPONENT_PREFIX[name];
    let items = [];
    if (prefix) items = store.configList.filter(c => c.key.indexOf(prefix) === 0);
    if (store.configUnavailable && !items.length) {
      panel.innerHTML = '<div class="empty">配置接口暂不可用（/admin/config/list）</div>';
    } else {
      Rock.views.config.renderConfigItems(panel, items, { compact: true });
    }
  }

  window.Rock.views.components = {
    load,
    render,
    skeleton,
    toggle,
    toggleConfig,
  };
})();
