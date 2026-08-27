/* ==========================================================================
 * RockSys 管理控制台 - views/blacklist.js 动态 IP 黑白名单管理（WAF 子视图）
 * 列表 / 分页 / 过滤、新增 / 软删 / 恢复、批量导入；
 * kind（black/white）与筛选状态页内私有。
 * 主 Tab（拦截统计 / 黑白名单）与页面级渲染由 waf.js 协调：
 * 经 bindPage 注入主 Tab HTML 与当前 Tab 读取器，本模块不感知 WAF 统计细节。
 * 挂载到全局命名空间 window.Rock.views.blacklist。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const fmtInt = Rock.util.fmtInt;
  const api = Rock.api;
  const toast = Rock.ui.toast;
  const confirmDialog = Rock.ui.confirmDialog;
  const BLOCK_TYPES = Rock.state.BLOCK_TYPES;
  const typeName = Rock.state.blockTypeName;

  // 查询 / 分页 / 数据状态（页内私有）
  const ipListState = {
    kind: 'black', // 'black' | 'white'
    ip: '', blockType: '', validOnly: false,
    limit: 20, offset: 0,
    rows: [], total: 0, loaded: false, error: '',
  };

  // 页面上下文（waf.js 注入）：tabsHTML() 主 Tab HTML、activeTab() 当前主 Tab
  let pageCtx = { tabsHTML: function () { return ''; }, activeTab: function () { return 'stats'; } };
  function bindPage(ctx) { pageCtx = ctx || pageCtx; }

  function ipListBase() {
    return ipListState.kind === 'black' ? '/admin/shield/blacklist' : '/admin/shield/whitelist';
  }

  async function loadIPList() {
    const p = [];
    if (ipListState.ip) p.push('ip=' + encodeURIComponent(ipListState.ip));
    if (ipListState.blockType) p.push('block_type=' + ipListState.blockType);
    if (ipListState.validOnly) p.push('valid_only=1');
    p.push('limit=' + ipListState.limit, 'offset=' + ipListState.offset);
    ipListState.loaded = true;
    try {
      const r = await api.get(ipListBase() + '?' + p.join('&'));
      ipListState.rows = r.rows || [];
      ipListState.total = Number(r.total) || 0;
      ipListState.error = '';
    } catch (e) {
      ipListState.error = e.status === 503 ? '黑白名单未启用（DB 未配置）' : (e.message || '加载失败');
    }
    render($('#page-waf'));
  }

  // 首次进入黑白名单 Tab 时确保数据已加载（幂等）
  async function ensureLoaded() {
    if (!ipListState.loaded) await loadIPList();
  }

  function ipListStatusHTML(row) {
    if (row.deleted_at) return '<span class="badge badge-danger">已删除</span>';
    if (row.expires_at) return '<span class="badge badge-warn">已过期</span>';
    return '<span class="badge badge-ok">有效</span>';
  }

  function ipListRowsHTML() {
    if (!ipListState.loaded) return '<div class="empty">加载中…</div>';
    if (ipListState.error) return '<div class="empty">' + esc(ipListState.error) + '</div>';
    if (!ipListState.rows.length) return '<div class="empty">暂无条目</div>';
    const isBlack = ipListState.kind === 'black';
    const head = isBlack
      ? '<th>ID</th><th>IP / CIDR</th><th>备注</th><th>类别</th><th>命中</th><th>过期时间</th><th>状态</th><th>操作</th>'
      : '<th>ID</th><th>IP / CIDR</th><th>备注</th><th>状态</th><th>操作</th>';
    const rows = ipListState.rows.map(function (row) {
      const id = row.id;
      const del = row.deleted_at
        ? '<button class="btn btn-sm btn-text" data-act="waf-iplist-restore" data-id="' + id + '">恢复</button>'
        : '<button class="btn btn-sm btn-text" data-act="waf-iplist-del" data-id="' + id + '">删除</button>';
      const base = isBlack
        ? '<td>' + id + '</td><td><b>' + esc(row.ip) + '</b></td><td>' + esc(row.title || '') + '</td>' +
          '<td>' + esc(typeName(row.block_type)) + '</td><td>' + fmtInt(Number(row.hit_count) || 0) + '</td>' +
          '<td>' + esc(row.expires_at || '永久') + '</td><td>' + ipListStatusHTML(row) + '</td>'
        : '<td>' + id + '</td><td><b>' + esc(row.ip) + '</b></td><td>' + esc(row.title || '') + '</td><td>' + ipListStatusHTML(row) + '</td>';
      return '<tr>' + base + '<td>' + del + '</td></tr>';
    }).join('');
    return '<div class="table-wrap"><table class="table"><thead><tr>' + head + '</tr></thead><tbody>' + rows + '</tbody></table></div>';
  }

  function ipListPagingHTML() {
    return Rock.comp.dataTable.pagingHTML({
      total: ipListState.total,
      offset: ipListState.offset,
      limit: ipListState.limit,
      act: 'waf-iplist-page',
    });
  }

  // 渲染整个 WAF 页面（黑白名单视图：主 Tab + 黑/白切换 + 列表/新增/导入）
  function render(host) {
    host = host || $('#page-waf');
    if (!host) return;
    const isBlack = ipListState.kind === 'black';
    const btOptions = BLOCK_TYPES.map(function (t) {
      return '<option value="' + t[0] + '"' + (String(t[0]) === ipListState.blockType ? ' selected' : '') + '>' + esc(t[1]) + '</option>';
    }).join('');
    host.innerHTML =
      Rock.comp.head.headHTML({
        title: 'WAF 防护',
        desc: '动态 IP 黑白名单：黑名单 = DB 表 ∪ 外挂 rules/ip_blacklist.txt；白名单 = DB 表 ∪ .env SHIELD_IP_WHITELIST；白名单优先、变更即时生效',
        actions: '<button class="btn btn-sm" data-act="waf-iplist-reload">⟳ 刷新</button>',
      }) +
      pageCtx.tabsHTML() +
      Rock.comp.tabs.tabsHTML(
        [{ name: 'black', label: '黑名单' }, { name: 'white', label: '白名单' }],
        ipListState.kind,
        { act: 'waf-iplist-kind', nameAttr: 'data-kind' }
      ) +
      '<div class="card"><div class="card-title">' + (isBlack ? '黑名单' : '白名单') + '条目 <span class="card-sub">' + (isBlack ? 'DB 表 ∪ 外挂 rules/ip_blacklist.txt（.env 已不再支持黑名单）' : 'DB 表 ∪ .env SHIELD_IP_WHITELIST') + '</span></div>' +
      '<div class="log-toolbar">' +
      '<input class="input input-sm" id="iplist-filter-ip" placeholder="IP 模糊" style="width:140px" value="' + esc(ipListState.ip) + '">' +
      (isBlack
        ? '<select class="select select-sm" id="iplist-filter-bt">' +
          '<option value="">全部类别</option>' + btOptions + '</select>'
        : '') +
      '<label class="chk"><input type="checkbox" id="iplist-filter-valid"' + (ipListState.validOnly ? ' checked' : '') + '> 仅有效</label>' +
      '<button class="btn btn-sm btn-primary" data-act="waf-iplist-query">查询</button>' +
      '<button class="btn btn-sm btn-text" data-act="waf-iplist-reset">重置</button>' +
      '</div>' +
      ipListRowsHTML() +
      ipListPagingHTML() +
      '</div>' +
      '<div class="card"><div class="card-title">新增' + (isBlack ? '黑名单' : '白名单') + '条目</div>' +
      '<div class="log-toolbar">' +
      '<input class="input input-sm" id="iplist-add-ip" placeholder="精确 IP 或 CIDR（必填）" style="width:180px">' +
      '<input class="input input-sm" id="iplist-add-title" placeholder="备注（可选）" style="width:160px">' +
      (isBlack
        ? '<select class="select select-sm" id="iplist-add-bt">' + btOptions + '</select>' +
          '<input class="input input-sm" id="iplist-add-expires" placeholder="过期 RFC3339（空=永久）" style="width:200px">'
        : '') +
      '<button class="btn btn-sm btn-primary" data-act="waf-iplist-add">新增</button>' +
      '</div></div>' +
      '<div class="card"><div class="card-title">批量导入 <span class="card-sub">每行一个 IP/CIDR，重复自动跳过</span></div>' +
      '<textarea class="input" id="iplist-import-text" rows="10" placeholder="每行一个：精确 IP 或 CIDR（兼容外挂文件格式）&#10;示例：192.168.1.100、2001:db8::1、10.0.0.0/8、2001:db8::/32&#10;# 开头为注释、空行忽略"></textarea>' +
      '<div class="log-toolbar">' +
      (isBlack
        ? '<select class="select select-sm" id="iplist-import-bt">' + btOptions + '</select>'
        : '') +
      '<button class="btn btn-sm" data-act="waf-iplist-import">批量导入</button>' +
      '</div></div>';
  }

  async function switchKind(kind) {
    if (ipListState.kind === kind) return;
    ipListState.kind = kind;
    ipListState.offset = 0;
    ipListState.rows = [];
    ipListState.total = 0;
    ipListState.loaded = false;
    await loadIPList();
  }

  async function query() {
    ipListState.ip = ($('#iplist-filter-ip') || {}).value || '';
    ipListState.blockType = ($('#iplist-filter-bt') || {}).value || '';
    ipListState.validOnly = !!($('#iplist-filter-valid') || {}).checked;
    ipListState.offset = 0;
    await loadIPList();
  }

  function reset() {
    ipListState.ip = ''; ipListState.blockType = ''; ipListState.validOnly = false; ipListState.offset = 0;
    loadIPList();
  }

  async function page(dir) {
    const next = ipListState.offset + dir * ipListState.limit;
    if (next < 0 || next >= ipListState.total) return;
    ipListState.offset = next;
    await loadIPList();
  }

  async function add() {
    const isBlack = ipListState.kind === 'black';
    const ip = ($('#iplist-add-ip') || {}).value || '';
    if (!ip) { toast('ip 必填（精确 IP 或 CIDR）', 'error'); return; }
    const body = { ip: ip.trim(), title: ($('#iplist-add-title') || {}).value || '' };
    if (isBlack) {
      body.block_type = Number(($('#iplist-add-bt') || {}).value) || 1;
      const exp = ($('#iplist-add-expires') || {}).value || '';
      if (exp) body.expires_at = exp.trim();
    }
    try {
      await api.post(ipListBase())(body);
      toast('已新增 ' + ip.trim(), 'success');
      ipListState.offset = 0;
      await loadIPList();
    } catch (e) {
      toast('新增失败：' + e.message, 'error');
    }
  }

  async function del(id) {
    const ok = await confirmDialog({
      title: '删除条目',
      message: '将软删除该条目（可恢复），立即从拦截生效集合移除。确认？',
      confirmText: '删除',
      danger: true,
    });
    if (!ok) return;
    try {
      await api.post(ipListBase() + '/delete')({ id: Number(id) });
      toast('已删除', 'success');
      await loadIPList();
    } catch (e) {
      toast('删除失败：' + e.message, 'error');
    }
  }

  async function restore(id) {
    try {
      await api.post(ipListBase() + '/restore')({ id: Number(id) });
      toast('已恢复', 'success');
      await loadIPList();
    } catch (e) {
      toast('恢复失败：' + e.message, 'error');
    }
  }

  async function importRows() {
    const text = ($('#iplist-import-text') || {}).value || '';
    if (!text.trim()) { toast('导入内容为空', 'error'); return; }
    const isBlack = ipListState.kind === 'black';
    const q = isBlack ? ('?block_type=' + (Number(($('#iplist-import-bt') || {}).value) || 1)) : '';
    try {
      const r = await api.post(ipListBase() + '/import' + q)(text);
      toast('已导入 ' + fmtInt(Number(r && r.imported) || 0) + ' 条，跳过 ' + fmtInt(Number(r && r.skipped) || 0) + ' 条', 'success');
      ($('#iplist-import-text') || {}).value = '';
      ipListState.offset = 0;
      await loadIPList();
    } catch (e) {
      toast('导入失败：' + e.message, 'error');
    }
  }

  window.Rock.views.blacklist = {
    bindPage: bindPage,
    render: render,
    ensureLoaded: ensureLoaded,
    switchKind: switchKind,
    query: query,
    reset: reset,
    page: page,
    add: add,
    del: del,
    restore: restore,
    importRows: importRows,
  };
})();
