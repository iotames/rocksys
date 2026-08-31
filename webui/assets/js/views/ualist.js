/* ==========================================================================
 * RockSys 管理控制台 - views/ualist.js UA黑/白名单管理（WAF 子视图）
 * 无 DB 表：数据源为 WAF 规则文件（crawler_ua.txt / ua_whitelist.txt），读写走规则三端点：
 *   - GET  /admin/shield/rules/file?name=  查看当前生效内容（外挂覆写/内嵌兜底，含内嵌默认）
 *   - POST /admin/shield/rules/save        追加/删除/恢复默认 = 整文保存（原子写，≤3s 热更）
 * 页面形态借鉴 IP 黑白名单：生效模式行级表格 + 单条删除 + 追加 + 恢复默认；
 * 注释行原样保留不参与匹配，整文编辑（改注释/排序/批量）请前往「文件编辑」页签（两处粒度不同、不冲突）。
 * kind（uablack/uawhite）页内私有；主 Tab 与页面级渲染由 waf.js 协调（bindPage 模式）。
 * 挂载到全局命名空间 window.Rock.views.ualist。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const api = Rock.api;
  const toast = Rock.ui.toast;
  const confirmDialog = Rock.ui.confirmDialog;

  // 子页签 → 规则文件（与后端 ruleFileMetas 同名）
  const FILES = {
    uablack: { name: 'crawler_ua.txt', label: 'UA黑名单', fileTitle: 'crawler_ua.txt（UA黑名单）' },
    uawhite: { name: 'ua_whitelist.txt', label: 'UA白名单', fileTitle: 'ua_whitelist.txt（UA白名单）' },
  };

  const state = {
    kind: 'uawhite',   // 'uablack' | 'uawhite'
    content: '',       // 当前生效文本原文（含注释行）
    embedded: '',      // 内嵌默认文本（恢复默认数据源）
    override: false,   // 是否外挂覆写
    patterns: [],      // 生效模式清单（小写、非注释非空行）
    loaded: false,
    error: '',
    saving: false,     // 保存请求进行中（禁用输入/按钮，防双击双追加）
  };

  function meta() { return FILES[state.kind]; }

  // 页面上下文（waf.js 注入）：tabsHTML() 主 Tab、iplistTabs() 四子页签、activeTab()
  let pageCtx = { tabsHTML: function () { return ''; }, iplistTabs: function () { return ''; }, activeTab: function () { return 'stats'; } };
  function bindPage(ctx) { pageCtx = ctx || pageCtx; }

  // 生效模式清单（小写、忽略注释与空行——与后端 parseRuleLines 语义一致）
  function parsePatterns(text) {
    return String(text || '').split('\n')
      .map(l => l.trim().toLowerCase())
      .filter(l => l && l.charAt(0) !== '#');
  }

  async function load() {
    state.loaded = true;
    try {
      const r = await api.get('/admin/shield/rules/file?name=' + encodeURIComponent(meta().name));
      state.content = r.content || '';
      state.embedded = r.embedded || '';
      state.override = !!r.override;
      state.patterns = parsePatterns(state.content);
      state.error = '';
    } catch (e) {
      state.error = e.message || '加载失败';
      if (e.status !== 0 && e.status !== 503) toast(meta().label + '加载失败：' + state.error + '，可稍后重试或前往「文件编辑」页签查看', 'error');
    }
    render($('#page-waf'));
  }

  async function ensureLoaded() {
    if (!state.loaded) await load();
  }

  async function switchKind(kind) {
    if (state.kind === kind) return;
    state.kind = FILES[kind] ? kind : 'uawhite';
    state.loaded = false;
    await load();
  }

  // 整文保存公共出口：请求中禁用输入/按钮（防双击），成功/失败 toast，完成重拉
  async function save(next, okMsg) {
    state.saving = true;
    setInputsDisabled(true);
    try {
      await api.post('/admin/shield/rules/save')({ name: meta().name, content: next });
      toast(okMsg, 'success');
      // 乐观更新：ScriptHub 缓存有 ≤3s 热更窗口，先按本次保存内容渲染；
      // 窗口过后（3.5s）再重拉对账（提前拉会读到热更前的旧缓存，把界面打回旧状态）
      state.saving = false;
      state.content = next;
      state.override = true; // 保存必然落外挂覆写文件
      state.patterns = parsePatterns(next);
      render($('#page-waf'));
      scheduleReconcile();
    } catch (e) {
      state.saving = false;
      setInputsDisabled(false);
      toast((state.kind === 'uawhite' ? '保存白名单失败' : '保存黑名单失败') + '：' + (e.message || '未知错误') + '。内容未写入，请稍后重试；仍失败可前往「文件编辑」页签手动编辑', 'error');
    }
  }

  function setInputsDisabled(disabled) {
    const i = $('#ualist-append-input'), b = $('#ualist-append-btn');
    if (i) i.disabled = disabled;
    if (b) b.disabled = disabled;
  }

  // 保存后的延迟对账（等 ScriptHub ≤3s 热更窗口过去，重拉真实生效内容）
  let reconcileTimer = null;
  function scheduleReconcile() {
    if (reconcileTimer) clearTimeout(reconcileTimer);
    reconcileTimer = setTimeout(function () {
      reconcileTimer = null;
      if (!state.saving) load();
    }, 3500);
  }

  // 追加一条模式：末尾插行整体保存（last-write-wins，单管理员场景）
  async function append() {
    if (state.saving) return;
    const input = $('#ualist-append-input');
    const val = (input || {}).value || '';
    const pattern = val.trim().toLowerCase();
    if (!pattern) { toast('请输入要追加的 UA 模式（子串匹配，非空）', 'error'); return; }
    if (pattern.charAt(0) === '#') { toast('模式不能以 # 开头（会被当作注释而不生效）；注释说明请前往「文件编辑」页签编辑', 'error'); return; }
    if (state.patterns.indexOf(pattern) >= 0) {
      toast('模式已存在，未重复追加：' + pattern, 'warn');
      (input || {}).value = '';
      return;
    }
    const content = state.content || '';
    const next = (content && !content.endsWith('\n') ? content + '\n' : content) + pattern + '\n';
    await save(next, '已追加 ' + pattern + '，将在 ≤3s 内热更生效');
  }

  // 删除一条模式：仅移除命中该模式的生效行（注释行原样保留），整文保存
  async function del(pattern) {
    const ok = await confirmDialog({
      title: '删除' + meta().label + '模式',
      message: '将删除模式「' + pattern + '」并 ≤3s 内热更生效；该模式的注释行保留。确认删除？',
      confirmText: '删除',
      danger: true,
    });
    if (!ok || state.saving) return;
    const lines = String(state.content || '').split('\n');
    const next = lines.filter(l => l.trim().toLowerCase() !== pattern).join('\n');
    await save(next, '已删除 ' + pattern + '，将在 ≤3s 内热更生效');
  }

  // 恢复默认：整文回退到编译期内嵌内容（外挂覆写文件内容被替换，文件本身保留）
  async function restoreDefault() {
    const ok = await confirmDialog({
      title: '恢复默认' + meta().label,
      message: '将把 ' + meta().name + ' 的内容整体恢复为编译期内嵌默认（当前自定义修改会丢失，含追加与删除），≤3s 内热更生效。确认恢复？',
      confirmText: '恢复默认',
      danger: true,
    });
    if (!ok || state.saving) return;
    await save(state.embedded || '', '已恢复默认内容，将在 ≤3s 内热更生效');
  }

  function tableHTML() {
    if (!state.loaded) return '<div class="empty">加载中…</div>';
    if (state.error) return '<div class="empty">' + esc(state.error) + '</div>';
    if (!state.patterns.length) return '<div class="empty">暂无生效模式（文件仅含注释或为空）</div>';
    const rows = state.patterns.map(p =>
      '<tr><td class="mono">' + esc(p) + '</td>' +
      '<td style="width:80px"><button class="btn btn-sm btn-text" data-act="waf-ualist-del" data-pattern="' + esc(p) + '">删除</button></td></tr>'
    ).join('');
    return '<div class="table-wrap"><table class="table"><thead><tr><th>生效模式（子串匹配 · 小写）</th><th>操作</th></tr></thead><tbody>' + rows + '</tbody></table></div>';
  }

  // 渲染整个 WAF 页面（UA 名单视图：主 Tab + 四子页签 + 行级表格 + 追加/恢复默认）
  function render(host) {
    host = host || $('#page-waf');
    if (!host) return;
    const isWhite = state.kind === 'uawhite';
    host.innerHTML =
      Rock.comp.head.headHTML({
        title: 'WAF 防护',
        desc: '黑白名单管理：IP 黑白名单为 DB 表 CRUD；UA黑/白名单为规则文件（行级管理），黑名单开关 SHIELD_WAF_CRAWLER_UA、白名单优先且仅豁免爬虫 UA 拦截',
        actions: '<button class="btn btn-sm" data-act="waf-iplist-reload">⟳ 刷新</button>',
      }) +
      pageCtx.tabsHTML() +
      pageCtx.iplistTabs(state.kind) +
      '<div class="card"><div class="card-title">追加' + esc(meta().label) + '模式 <span class="card-sub">追加后 ≤3s 热更生效；改注释/排序等整文编辑请前往<a data-act="waf-tab" data-tab="files" style="cursor:pointer">「文件编辑」</a>页签</span></div>' +
      '<div class="log-toolbar">' +
      '<input class="input input-sm" id="ualist-append-input" placeholder="UA 子串模式（如 googlebot，追加时转小写）" style="width:280px"' + (state.saving ? ' disabled' : '') + '>' +
      '<button class="btn btn-sm btn-primary" id="ualist-append-btn" data-act="waf-ualist-append"' + (state.saving ? ' disabled' : '') + '>追加</button>' +
      '</div></div>' +
      '<div class="card"><div class="card-title" data-tip="' + esc(isWhite
        ? '规则文件 rules/ua_whitelist.txt：无开关、有数据即生效；UA 黑名单开关开启后命中即在黑名单判定前放行，仅豁免爬虫 UA 拦截这一步，其余检测照常'
        : '规则文件 rules/crawler_ua.txt：开关 SHIELD_WAF_CRAWLER_UA 开启后按子串匹配拦截；命中 ua_whitelist.txt 的 UA 优先放行') + '">' +
      esc(meta().fileTitle) +
      ' <span class="card-sub">' + (state.override ? '外挂覆写生效' : '内嵌默认') + ' · 生效模式 ' + esc(String(state.patterns.length)) + ' 条 · 注释行不参与匹配</span>' +
      '<button class="btn btn-sm btn-text" style="float:right" data-act="waf-ualist-restore" data-tip="整体恢复为编译期内嵌默认内容（当前自定义修改丢失）">恢复默认</button></div>' +
      tableHTML() +
      '</div>';
    const input = $('#ualist-append-input');
    if (input) input.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' && !state.saving) append();
    });
  }

  window.Rock.views.ualist = {
    bindPage: bindPage,
    render: render,
    ensureLoaded: ensureLoaded,
    switchKind: switchKind,
    load: load,
    append: append,
    del: del,
    restoreDefault: restoreDefault,
  };
})();
