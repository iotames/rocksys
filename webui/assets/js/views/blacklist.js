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

  // 查询 / 分页 / 数据状态（页内私有；筛选值在 filterBar 实例、分页在 dataTable 实例）
  const ipListState = {
    kind: 'black', // 'black' | 'white'
    limit: 20, offset: 0,
    rows: [], total: 0, loaded: false, error: '',
  };

  // 筛选栏与表格实例：黑/白切换时重建（类别字段仅黑名单有，不做动态显隐配置）
  let ipBar = null;
  let ipTable = null;

  // RFC3339 → 本地 'YYYY-MM-DD HH:mm:ss'；解析失败回原文（空值回空串）
  function fmtDT(v) {
    var d = Rock.util.fmtDateTime(v);
    return d === '--:--:--' ? (v || '') : d;
  }

  function buildInstances() {
    const isBlack = ipListState.kind === 'black';
    // 类别筛选选项：排除 0（0=其他仅为存储兜底，查询语境 0=全部，不提供筛选）
    const btOptions = [['', '全部类别']].concat(
      BLOCK_TYPES.filter(function (t) { return t[0] !== 0; }).map(function (t) { return [String(t[0]), t[1]]; })
    );
    // 排序选项（仅黑名单）：与后端 sort 白名单映射（ip_list_store.go blacklistSortWhitelist）一一对应，均固定 DESC
    const sortOptions = [
      ['', '排序：默认（最近添加）'],
      ['hit_count', '按命中次数'],
      ['warn_times', '按封禁次数'],
      ['created_at', '按封禁时间'],
      ['expires_at', '按解封时间'],
      ['updated_at', '按最后更新'],
      ['block_type', '按封禁原因类别'],
    ];
    const fields = [{ type: 'text', key: 'ip', placeholder: 'IP 模糊', width: '140px' }];
    if (isBlack) fields.push({ type: 'select', key: 'blockType', options: btOptions });
    if (isBlack) fields.push({ type: 'select', key: 'sort', options: sortOptions });
    fields.push({ type: 'check', key: 'validOnly', label: '仅有效' });
    ipBar = Rock.comp.filterBar.create({
      ns: 'waf-iplist-filter',
      live: true,
      onQuery: function () { query(); },
      fields: fields,
    });
    const cols = [
      { key: 'id', label: 'ID' },
      { key: 'created_at', label: '添加时间', render: function (r) { return esc(fmtDT(r.created_at)); } },
    ];
    if (isBlack) {
      cols.push({ key: 'expires_at', label: '过期时间', render: function (r) {
        if (!r.expires_at) return '永久';
        return esc(fmtDT(r.expires_at));
      } });
    }
    cols.push(
      { key: 'ip', label: 'IP / CIDR', render: function (r) { return '<b>' + esc(r.ip) + '</b>'; } }
    );
    if (isBlack) {
      cols.push(
        { key: 'block_type', label: '类别', render: function (r) { return esc(typeName(r.block_type)); } },
        { key: 'hit_count', label: '命中数', render: function (r) { return fmtInt(Number(r.hit_count) || 0); } },
        { key: 'warn_times', label: '封禁次数', render: function (r) { return fmtInt(Number(r.warn_times) || 0); } }
      );
    }
    cols.push(
      { key: 'status', label: '状态', render: ipListStatusHTML },
      { key: 'title', label: '标题' }
    );
    ipTable = Rock.comp.dataTable.create({
      ns: 'waf-iplist',
      columns: cols,
      rowKey: function (r) { return String(r.id); },
      rowActions: function (row) {
        return row.deleted_at
          ? '<button class="btn btn-sm btn-text" data-act="waf-iplist-restore" data-id="' + row.id + '">恢复</button>'
          : '<button class="btn btn-sm btn-text" data-act="waf-iplist-del" data-id="' + row.id + '">删除</button>';
      },
      // 点行打开详情弹层（操作列按钮事件优先于行点击，互不影响）
      detail: { title: function () { return isBlack ? '黑名单条目详情' : '白名单条目详情'; } },
      paging: { mode: 'server', pageSize: 20 },
      emptyText: '暂无条目',
      onPaging: function (st) {
        ipListState.offset = st.offset;
        ipListState.limit = st.pageSize;
        loadIPList();
      },
    });
    // 覆盖 dataTable 缺省只读详情：点行打开可编辑弹层（黑/白切换重建实例后需重挂）
    ipTable.onDetail = function (row) { openDetail(row.id); };
  }
  buildInstances();

  // 页面上下文（waf.js 注入）：tabsHTML() 主 Tab HTML、activeTab() 当前主 Tab
  let pageCtx = { tabsHTML: function () { return ''; }, activeTab: function () { return 'stats'; } };
  function bindPage(ctx) { pageCtx = ctx || pageCtx; }

  function ipListBase() {
    return ipListState.kind === 'black' ? '/admin/shield/blacklist' : '/admin/shield/whitelist';
  }

  // 当前是否黑名单视图（类别/排序/封禁次数/同步按钮均仅黑名单有）
  function isBlack() { return ipListState.kind === 'black'; }

  async function loadIPList() {
    const s = ipBar.state();
    const st = ipTable.state();
    const p = [];
    if (s.ip) p.push('ip=' + encodeURIComponent(s.ip));
    if (s.blockType) p.push('block_type=' + s.blockType);
    if (isBlack() && s.sort) p.push('sort=' + encodeURIComponent(s.sort)); // 排序仅黑名单（服务端白名单映射，非法回默认）
    if (s.validOnly) p.push('valid_only=1');
    ipListState.limit = st.pageSize;
    ipListState.offset = st.offset;
    p.push('limit=' + st.pageSize, 'offset=' + st.offset);
    ipListState.loaded = true;
    try {
      const r = await api.get(ipListBase() + '?' + p.join('&'));
      ipListState.rows = r.rows || [];
      ipListState.total = Number(r.total) || 0;
      ipListState.error = '';
    } catch (e) {
      // 503 = DB 未配置，属降级引导态（页内有引导文案），不弹 toast；其余失败统一弹 error toast
      ipListState.error = e.status === 503 ? '黑白名单未启用（DB 未配置）' : (e.message || '加载失败');
      if (e.status !== 0 && e.status !== 503) toast('黑白名单加载失败：' + (e.message || '加载失败') + '，可稍后重试', 'error');
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
    return ipTable.html(ipListState.rows, { total: ipListState.total });
  }

  // 渲染整个 WAF 页面（黑白名单视图：主 Tab + 黑/白切换 + 列表/新增/导入）
  function render(host) {
    host = host || $('#page-waf');
    if (!host) return;
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
      (pageCtx.iplistTabs
        ? pageCtx.iplistTabs(ipListState.kind === 'white' ? 'ipwhite' : 'ipblack')
        : Rock.comp.tabs.tabsHTML(
            [{ name: 'black', label: '黑名单' }, { name: 'white', label: '白名单' }],
            ipListState.kind,
            { act: 'waf-iplist-kind', nameAttr: 'data-kind' }
          )) +
      '<div class="card"><div class="card-title" data-tip="' + esc(isBlack()
        ? '数据来自数据库 ip_blacklist 表；外挂 rules/ip_blacklist.txt 仅参与拦截判定、不在此展示，可经「从文件同步」入库统一管理'
        : '数据来自数据库 ip_blacklist 表（白名单侧）') + '">' + (isBlack() ? '黑名单条目（DB表）' : '白名单条目（DB表）') +
      ' <span class="card-sub">' + (isBlack() ? 'DB 表 ∪ 外挂 rules/ip_blacklist.txt（.env 已不再支持黑名单）' : 'DB 表 ∪ .env SHIELD_IP_WHITELIST') + '</span></div>' +
      ipBar.html() +
      '<div class="log-toolbar" style="margin-top:-6px">' +
      '<button class="btn btn-sm btn-primary" data-act="waf-iplist-query">查询</button>' +
      '<button class="btn btn-sm btn-text" data-act="waf-iplist-reset">重置</button>' +
      (isBlack()
        ? '<button class="btn btn-sm btn-text" data-act="waf-iplist-sync-file" data-tip="从外挂规则文件 rules/ip_blacklist.txt 同步 IP 入库。因文件无过期时间/标题等维护字段，同步入数据库后便于统一管理、统计与自动拉黑">从文件同步</button>'
        : '') +
      '</div>' +
      '<div id="iplist-table-wrap">' + ipListRowsHTML() + '</div>' +
      '</div>' +
      '<div class="card"><div class="card-title">新增' + (isBlack() ? '黑名单' : '白名单') + '条目</div>' +
      '<div class="log-toolbar">' +
      '<input class="input input-sm" id="iplist-add-ip" placeholder="精确 IP 或 CIDR（必填）" style="width:180px">' +
      '<input class="input input-sm" id="iplist-add-title" placeholder="标题（可选）" style="width:160px">' +
      (isBlack()
        ? '<span class="muted" data-tip="入库记录的拉黑原因归类（block_type 枚举），用于黑名单列表过滤与拦截统计；自由文字请填标题栏">拉黑原因类别</span>' +
          '<select class="select select-sm" id="iplist-add-bt" data-tip="入库记录的拉黑原因归类（block_type 枚举），用于黑名单列表过滤与拦截统计；自由文字请填标题栏">' + btOptions + '</select>' +
          '<span class="tool-group"><span class="muted">过期时间</span>' +
          '<input class="input input-sm" type="datetime-local" id="iplist-add-expires" title="留空 = 永久有效"></span>'
        : '') +
      '<button class="btn btn-sm btn-primary" data-act="waf-iplist-add">新增</button>' +
      '</div></div>' +
      '<div class="card"><div class="card-title">批量导入 <span class="card-sub">每行一个 IP/CIDR，重复自动跳过</span></div>' +
      '<textarea class="input" id="iplist-import-text" rows="10" placeholder="每行一个：精确 IP 或 CIDR（兼容外挂文件格式）&#10;示例：192.168.1.100、2001:db8::1、10.0.0.0/8、2001:db8::/32&#10;# 开头为注释、空行忽略"></textarea>' +
      '<div class="log-toolbar">' +
      (isBlack()
        ? '<span class="muted" data-tip="入库记录的拉黑原因归类（block_type 枚举），用于黑名单列表过滤与拦截统计；自由文字请填标题栏">拉黑原因类别</span>' +
          '<select class="select select-sm" id="iplist-import-bt" data-tip="入库记录的拉黑原因归类（block_type 枚举），用于黑名单列表过滤与拦截统计；自由文字请填标题栏">' + btOptions + '</select>'
        : '') +
      '<button class="btn btn-sm" data-act="waf-iplist-import">批量导入</button>' +
      '</div></div>';
    // 筛选栏即改即查（组件内防抖）+ 分页控件委托（wrap 每次渲染重建，随渲染重绑安全）
    ipBar.bind(host);
    ipTable.bind($('#iplist-table-wrap'));
  }

  async function switchKind(kind) {
    if (ipListState.kind === kind) return;
    ipListState.kind = kind;
    ipListState.offset = 0;
    ipListState.rows = [];
    ipListState.total = 0;
    ipListState.loaded = false;
    buildInstances(); // 类别字段仅黑名单有：黑/白切换重建筛选栏与表格实例
    await loadIPList();
  }

  async function query() {
    ipBar.collect();
    ipTable.go(1); // 新查询回第 1 页
    await loadIPList();
  }

  function reset() {
    ipBar.reset(); // 回默认值并触发 onQuery → query()
  }

  // 输入框错误态标记（输入即清除）
  function markInvalid(el) {
    if (!el) return;
    el.classList.add('is-invalid');
    el.addEventListener('input', function onInput() { el.classList.remove('is-invalid'); el.removeEventListener('input', onInput); });
  }

  async function add() {
    const ipEl = $('#iplist-add-ip');
    const ip = (ipEl || {}).value || '';
    if (!ip) { toast('ip 必填（精确 IP 或 CIDR）', 'error'); markInvalid(ipEl); return; }
    if (!Rock.util.validIPOrCIDR(ip.trim())) {
      toast('IP/CIDR 格式非法：' + ip.trim(), 'error');
      markInvalid(ipEl);
      return;
    }
    const body = { ip: ip.trim(), title: ($('#iplist-add-title') || {}).value || '' };
    if (isBlack()) {
      // 缺省兜底 11 = 人工收录（决策 1：人工添加默认不再写 1）
      body.block_type = Number(($('#iplist-add-bt') || {}).value);
      if (!(body.block_type >= 0 && body.block_type <= 11)) body.block_type = 11;
      // 过期时间：datetime-local 本地时间 → RFC3339（UTC）；留空 = 永久
      const exp = ($('#iplist-add-expires') || {}).value || '';
      if (exp) {
        const d = new Date(exp);
        if (isNaN(d.getTime())) { toast('过期时间非法', 'error'); markInvalid($('#iplist-add-expires')); return; }
        body.expires_at = d.toISOString();
      }
    }
    try {
      await api.post(ipListBase())(body);
      toast('已新增 ' + ip.trim(), 'success');
      ipTable.go(1); // 回第 1 页
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
    const textEl = $('#iplist-import-text');
    const text = (textEl || {}).value || '';
    if (!text.trim()) { toast('导入内容为空', 'error'); return; }
    // 逐行前端校验（# 开头注释与空行忽略）：存在非法行直接整体拦截，避免半成功
    const bad = [];
    text.split('\n').forEach(function (line) {
      const t = line.trim();
      if (!t || t.indexOf('#') === 0) return;
      if (!Rock.util.validIPOrCIDR(t)) bad.push(t);
    });
    if (bad.length) {
      toast('存在 ' + bad.length + ' 行非法 IP/CIDR（如：' + bad.slice(0, 2).join('、') + '），请修正后再导入', 'error');
      markInvalid(textEl);
      return;
    }
    // 缺省兜底 11 = 人工收录；注意 0（其他）是合法存储值，不能简单 `|| 11`（会被吞掉）
    let bt = Number(($('#iplist-import-bt') || {}).value);
    if (!(bt >= 0 && bt <= 11)) bt = 11;
    const q = isBlack() ? ('?block_type=' + bt) : '';
    try {
      const r = await api.post(ipListBase() + '/import' + q)(text);
      toast('已导入 ' + fmtInt(Number(r && r.imported) || 0) + ' 条，跳过 ' + fmtInt(Number(r && r.skipped) || 0) + ' 条', 'success');
      ($('#iplist-import-text') || {}).value = '';
      ipTable.go(1); // 回第 1 页
      await loadIPList();
    } catch (e) {
      toast('导入失败：' + e.message, 'error');
    }
  }

  // 从外挂规则文件 rules/ip_blacklist.txt 同步 IP 入库（后端解析导入，响应 {imported, skipped}）
  async function syncFile() {
    try {
      const r = await api.post('/admin/shield/blacklist/sync_file')('');
      toast('已从文件同步导入 ' + fmtInt(Number(r && r.imported) || 0) + ' 条，跳过 ' + fmtInt(Number(r && r.skipped) || 0) + ' 条', 'success');
      ipTable.go(1); // 回第 1 页
      await loadIPList();
    } catch (e) {
      // 异常常驻提示（文案三要素）：说清发生了什么 + 为什么 + 下一步怎么办
      toast('从文件同步失败：' + (e.message || '未知错误') + '。请确认外挂文件 rules/ip_blacklist.txt 存在且内容为合法 IP/CIDR（# 注释与空行忽略），修正后重试', 'error');
    }
  }

  // ── 行详情弹层（点数据行打开；只读信息 + 可编辑字段）──────────────────

  // RFC3339 → datetime-local 输入值（本地时区 'YYYY-MM-DDTHH:mm'）；解析失败回空串
  function rfc3339ToLocalInput(v) {
    const d = new Date(v || '');
    if (!v || isNaN(d.getTime())) return '';
    const p = n => String(n).padStart(2, '0');
    return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) +
      'T' + p(d.getHours()) + ':' + p(d.getMinutes());
  }

  function openDetail(id) {
    const row = ipListState.rows.find(r => String(r.id) === String(id));
    if (!row) return;
    const black = isBlack();
    const btOptions = BLOCK_TYPES.map(function (t) {
      return '<option value="' + t[0] + '"' + (Number(row.block_type) === t[0] ? ' selected' : '') + '>' + esc(t[0] + ' ' + t[1]) + '</option>';
    }).join('');
    // 只读区：对照 DATA_DICT ip_blacklist 字段尽量齐全（id/ip/类别/计数/各时间戳）；
    // 编辑区：标题/类别/过期时间（title 以编辑框承载，默认值为当前标题）
    const body =
      '<div class="form-row"><label class="form-label">ID</label>' +
      '<span class="v mono">' + esc(row.id) + '</span></div>' +
      '<div class="form-row"><label class="form-label">IP / CIDR</label>' +
      '<span class="v mono">' + esc(row.ip) + '</span></div>' +
      (black
        ? '<div class="form-row"><label class="form-label">命中数 / 封禁次数</label><span class="v">' +
          esc(fmtInt(Number(row.hit_count) || 0)) + ' / ' + esc(fmtInt(Number(row.warn_times) || 0)) + '</span></div>'
        : '') +
      '<div class="form-row"><label class="form-label">状态</label><span class="v">' + ipListStatusHTML(row) + '</span>' +
      (row.deleted_at ? '<span class="v muted" style="margin-left:10px">软删于 ' + esc(fmtDT(row.deleted_at)) + '</span>' : '') +
      '</div>' +
      '<div class="form-row"><label class="form-label">添加时间</label>' +
      '<span class="v">' + esc(fmtDT(row.created_at) || '—') + '</span></div>' +
      '<div class="form-row"><label class="form-label">最后更新</label>' +
      '<span class="v">' + esc(fmtDT(row.updated_at) || '—') + '</span></div>' +
      '<div class="form-row"><label class="form-label">标题</label>' +
      '<input class="input" id="iplist-edit-title" style="width:100%" maxlength="200" value="' + esc(row.title || '') + '" placeholder="拉黑原因标题（可空）"></div>' +
      (black
        ? '<div class="form-row"><label class="form-label">拉黑原因类别（可改）</label>' +
          '<select class="select" id="iplist-edit-bt" style="width:260px">' + btOptions + '</select></div>' +
          '<div class="form-row"><label class="form-label">过期时间（可改）</label>' +
          '<input class="input" type="datetime-local" id="iplist-edit-expires" style="width:230px" value="' +
          esc(rfc3339ToLocalInput(row.expires_at)) + '">' +
          '<div class="form-hint" style="margin-top:4px">留空 = 永久有效（保存即按此生效，永久条目留空即可）</div></div>'
        : '') +
      '<div class="form-hint" style="margin-top:8px">更新立即重建拦截快照并生效；命中数/封禁次数为系统累计，不支持手工修改。</div>';
    const overlay = Rock.ui.openModal({
      title: black ? '黑名单条目详情' : '白名单条目详情',
      body: body,
      width: 480,
      footer: '<button class="btn btn-primary" data-act="waf-iplist-update">保存</button>',
    });
    overlay.addEventListener('click', async function (e) {
      if (!e.target.closest('[data-act="waf-iplist-update"]')) return;
      const bodyReq = { id: Number(row.id), title: (overlay.querySelector('#iplist-edit-title') || {}).value || '' };
      if (black) {
        bodyReq.block_type = Number((overlay.querySelector('#iplist-edit-bt') || {}).value);
        if (!(bodyReq.block_type >= 0 && bodyReq.block_type <= 11)) bodyReq.block_type = 11;
        const exp = (overlay.querySelector('#iplist-edit-expires') || {}).value || '';
        if (exp) {
          const d = new Date(exp);
          if (isNaN(d.getTime())) { toast('过期时间非法，请重新选择', 'error'); return; }
          bodyReq.expires_at = d.toISOString();
        }
      }
      try {
        await api.post(ipListBase() + '/update')(bodyReq);
        toast('已更新 ' + row.ip, 'success');
        overlay.remove();
        await loadIPList();
      } catch (err) {
        toast('更新失败：' + (err.message || '未知错误') + '。请修正后重试，或刷新页面查看条目当前状态', 'error');
      }
    });
  }

  window.Rock.views.blacklist = {
    bindPage: bindPage,
    render: render,
    ensureLoaded: ensureLoaded,
    switchKind: switchKind,
    query: query,
    reset: reset,
    add: add,
    del: del,
    restore: restore,
    importRows: importRows,
    syncFile: syncFile,
    openDetail: openDetail,
  };
})();
