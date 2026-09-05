/* ==========================================================================
 * RockSys 管理控制台 - views/database.js 数据库页（服务 → 数据库）
 * 页签「表结构」：期望结构（当前运行 SQL 源，外挂优先、内嵌兜底）与实际结构
 * （当前数据连接 catalog）比对 → 差异分级表 + 生成 SQL 预填编辑器 →
 * danger 强确认执行 → 逐条结果（失败标红、常驻 toast 引导复核）。
 * 页签「执行历史」：sql_exec_log 表审计记录（每条语句一行）分页展示，
 * 谁在何时执行了什么、成败与耗时，刷新/换会话均可追溯。
 * 后端契约：GET /admin/db/schema、POST /admin/db/exec、GET /admin/db/execlog。
 * 挂载到全局命名空间 window.Rock.views.database。
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
  const skeletonHTML = Rock.ui.skeletonHTML;
  const codeEditor = Rock.comp.codeEditor;
  const fmtDateTime = Rock.util.fmtDateTime;
  const truncate = Rock.util.truncate;

  // SQL 编辑器实例 id（codeEditor.html/wire/setValue 均按此 id 定位节点与状态）
  const EDITOR_ID = 'db-sql-editor';

  // 执行历史分页页大小
  const HIST_PAGE_SIZE = 20;

  // 页面内部状态
  const state = {
    loaded: false,      // 首次进入拉取后为 true（路由往返走缓存渲染，手动刷新才重拉）
    checking: false,    // 检查请求进行中（按钮防重）
    driver: '',         // 数据方言（后端返回，如 sqlite/mysql/postgres）
    items: [],          // 差异列表（level A-F）
    sql: '',            // 后端按自动项生成的 SQL（预填编辑器）
    exec: null,         // 最近一次执行结果 { results, executed, failed }
    tab: 'schema',      // 当前页签：'schema' 表结构 | 'history' 执行历史
    hist: {             // 执行历史（服务端分页）
      loaded: false, loading: false,
      items: [], total: 0, offset: 0,
    },
  };

  // 差异分级展示配置：级别 → { label 差异类型, tag 分级标签（绿=自动/橙=需人工/灰=仅提示） }
  const LEVEL_META = {
    A: { label: '缺表', tag: '<span class="tag tag-green">自动</span>' },
    B: { label: '缺普通列', tag: '<span class="tag tag-green">自动</span>' },
    C: { label: '缺 PK/UNIQUE/自增列', tag: '<span class="tag tag-orange">需人工</span>' },
    D: { label: '缺索引', tag: '<span class="tag tag-green">自动</span>' },
    E: { label: '结构不一致', tag: '<span class="tag tag-gray">仅提示</span>' },
    F: { label: '多余对象', tag: '<span class="tag tag-gray">仅提示</span>' },
  };

  // 差异结果表（client 模式实例；bind 一次挂在页容器上，重渲染不受影响）
  const diffTable = Rock.comp.dataTable.create({
    ns: 'db-diff',
    columns: [
      { key: 'table', label: '表' },
      { key: 'object', label: '对象' },
      { key: 'level', label: '差异类型', render: r => {
          const m = LEVEL_META[r.level] || { label: r.level, tag: '<span class="tag tag-gray">未知</span>' };
          return m.tag + ' <span class="muted">' + esc(m.label) + '</span>';
        } },
      { key: 'expected', label: '期望' },
      { key: 'actual', label: '实际' },
      { key: 'note', label: '建议' },
    ],
    paging: { mode: 'client' },
    emptyText: '未发现差异',
  });

  // 执行结果表（client 模式；失败行标红复用 logs 页的 is-error 行样式）
  const execTable = Rock.comp.dataTable.create({
    ns: 'db-exec',
    columns: [
      { key: 'idx', label: '#', width: '48px', render: r => esc(r.idx) },
      { key: 'sql', label: '语句' },
      { key: 'ok', label: '结果', width: '120px', render: r => r.ok
          ? '<span class="tag tag-green">成功</span>' + (r.rows != null ? ' <span class="muted">' + esc(String(r.rows)) + ' 行</span>' : '')
          : '<span class="tag tag-orange">失败</span>' },
      { key: 'error', label: '说明' },
    ],
    paging: { mode: 'client' },
    rowClass: r => (r.ok ? '' : 'is-error'),
    emptyText: '尚未执行',
  });

  // 执行历史表（client 模式展示当前页；分页由页内上一页/下一页按钮驱动服务端 offset）
  const histTable = Rock.comp.dataTable.create({
    ns: 'db-hist',
    columns: [
      { key: 'time', label: '执行时间', cls: 'mono', render: r => esc(fmtDateTime(r.time)) },
      { key: 'batch_id', label: '批次/#', cls: 'mono', render: r =>
          '<span title="批次 ' + esc(r.batch_id) + '">' + esc(String(r.batch_id || '').slice(0, 8)) +
          '</span> <span class="muted">#' + esc(r.seq) + '</span>' },
      { key: 'sql_text', label: '语句', render: r =>
          '<span class="mono" title="' + esc(r.sql_text) + '">' + esc(truncate(r.sql_text, 90)) + '</span>' },
      { key: 'ok', label: '结果', width: '90px', render: r => r.ok
          ? '<span class="tag tag-green">成功</span>'
          : '<span class="tag tag-orange">失败</span>' },
      { key: 'rows_affected', label: '行数', width: '70px', cls: 'mono' },
      { key: 'duration_ms', label: '耗时', width: '80px', cls: 'mono', render: r => esc(r.duration_ms) + 'ms' },
      { key: 'client_ip', label: '来源 IP', cls: 'mono', width: '130px' },
      { key: 'error', label: '失败原因', render: r => r.error
          ? '<span title="' + esc(r.error) + '">' + esc(truncate(r.error, 60)) + '</span>' : '' },
    ],
    paging: { mode: 'client' },
    rowClass: r => (r.ok ? '' : 'is-error'),
    emptyText: '暂无执行记录（执行 SQL 后自动留痕）',
  });

  // 首次进入挂分页控件事件（页容器为持久元素）
  let bound = false;
  function ensureBind() {
    if (bound) return;
    const host = $('#page-database');
    if (host) { diffTable.bind(host); execTable.bind(host); histTable.bind(host); bound = true; }
  }

  // 页面加载：首次拉一次表结构检查，其余路由往返走缓存（手动刷新按钮 force 重拉）
  async function load(opts) {
    ensureBind();
    if (state.loaded && !opts.force) { render(); return; }
    const host = $('#page-database');
    if (!state.loaded && host && !host.innerHTML.trim()) host.innerHTML = skeletonHTML(5);
    try {
      const res = await api.get('/admin/db/schema');
      state.driver = String(res.driver || '');
      state.items = Array.isArray(res.items) ? res.items : [];
      state.sql = String(res.sql || '');
      state.loaded = true;
    } catch (e) {
      if (e.status !== 0) {
        toast('表结构检查失败：' + e.message + '。请确认服务可达后点击「表结构检查」重试', 'error');
      }
    }
    render();
  }

  // 说明区：口径说明 + 当前方言
  function infoHTML() {
    const drv = state.driver ? '<span class="tag tag-blue">' + esc(state.driver) + '</span>' : '';
    return '<div class="alert alert-info">' +
      '<b>口径说明：</b>期望结构 = 当前运行 SQL 源（外挂 <code>HOT_SCRIPTS_DIR/sql/</code> 优先、内嵌兜底）；' +
      '实际结构 = 当前数据连接 catalog ' + drv + '。' +
      '检查只读不写；「执行SQL」将直接作用于当前数据库，DDL 不可回滚，执行前建议先备份。</div>';
  }

  // 操作区：检查主按钮 + 执行危险按钮（编辑器无内容时禁用）
  function actionsHTML() {
    const hasSQL = !!(state.sql && state.sql.trim());
    return '<div class="comp-actions" style="margin-bottom:12px">' +
      '<button class="btn btn-primary" data-act="db-check"' + (state.checking ? ' disabled' : '') + '>' +
      (state.checking ? '检查中…' : '表结构检查') + '</button>' +
      '<button class="btn btn-danger" data-act="db-exec"' + (hasSQL ? '' : ' disabled') +
      ' title="执行编辑器中的 SQL 语句（直接作用于当前数据库）">执行SQL</button>' +
      '</div>';
  }

  function schemaHTML() {
    let html = infoHTML() + actionsHTML();
    if (state.items.length) {
      const autoCnt = state.items.filter(i => i.auto).length;
      html += '<div class="card"><div class="card-title">差异结果' +
        '<span class="tag tag-orange">' + state.items.length + ' 处差异（自动 ' + autoCnt + ' / 人工 ' + (state.items.length - autoCnt) + '）</span>' +
        '</div>' + diffTable.html(state.items) + '</div>';
      html +=
        '<div class="card">' +
        '<div class="card-title">SQL 预览与执行' +
        '<span class="comp-actions">' +
        '<button class="btn btn-sm" data-act="db-copy-sql">复制</button>' +
        '<button class="btn btn-sm btn-danger" data-act="db-exec"' + (state.sql.trim() ? '' : ' disabled') + '>执行SQL</button>' +
        '</span></div>' +
        codeEditor.html(EDITOR_ID, { lang: 'sql', height: '320px', value: state.sql }) +
        '<div class="form-hint" style="margin-top:8px">已按自动差异（缺表 / 缺列 / 缺索引）预填生成 SQL，可自由编辑（如只保留部分语句、手工补写救急语句）；非自动差异（PK/UNIQUE/自增列、类型不一致、多余对象）不自动生成，请参考差异表建议人工处理。</div>' +
        '</div>';
    } else if (state.loaded) {
      html += '<div class="card">' + Rock.comp.empty.message({ text: '表结构一致，未发现差异' }) + '</div>';
    }
    // 执行结果区（最近一次执行后展示，失败行标红）
    if (state.exec) {
      html += '<div class="card"><div class="card-title">最近一次执行结果' +
        '<span class="' + (state.exec.failed ? 'tag tag-orange' : 'tag tag-green') + '">' +
        '成功 ' + state.exec.executed + ' / 失败 ' + state.exec.failed + '</span></div>' +
        execTable.html(state.exec.results) + '</div>';
    }
    return html;
  }

  function render() {
    const host = $('#page-database');
    if (!host) return;
    ensureBind();
    host.innerHTML =
      Rock.comp.head.headHTML({
        title: '数据库',
        desc: '表结构比对：检查差异、生成 SQL 并执行同步；执行 SQL 全量留痕可审计',
        actions: '<button class="btn btn-sm" data-act="db-check"' + (state.checking ? ' disabled' : '') + '>⟳ 重新检查</button>',
      }) +
      Rock.comp.tabs.tabsHTML(
        [{ name: 'schema', label: '表结构' }, { name: 'history', label: '执行历史' }],
        state.tab,
        { act: 'db-tab', nameAttr: 'data-tab' }
      ) +
      '<div class="tab-pane">' + (state.tab === 'history' ? histHTML() : schemaHTML()) + '</div>';
    // 编辑器联动：内容变化即时同步「执行SQL」按钮可用态（不整页重绘，避免打断输入）
    if (state.tab === 'schema' && state.items.length) {
      codeEditor.wire(EDITOR_ID, {
        onChange: function (src) {
          state.sql = src;
          const btn = document.querySelector('#page-database [data-act="db-exec"]');
          if (btn) btn.disabled = !src.trim();
        },
      });
    }
  }

  // ── 执行历史页签：sql_exec_log 审计记录分页展示 ────────────────────────

  // 拉取执行历史（服务端分页；切换页签或翻页/刷新按钮触发）
  async function loadHist(opts) {
    const o = opts || {};
    if (state.hist.loading) return;
    if (state.hist.loaded && !o.force && !o.move) { render(); return; }
    state.hist.loading = true;
    if (o.move) render(); // 翻页时保留旧内容就地刷新（无骨架屏闪烁）
    try {
      const res = await api.get('/admin/db/execlog?limit=' + HIST_PAGE_SIZE + '&offset=' + state.hist.offset);
      state.hist.items = Array.isArray(res.items) ? res.items : [];
      state.hist.total = Number(res.total) || 0;
      state.hist.loaded = true;
    } catch (e) {
      if (e.status !== 0) {
        toast('执行历史查询失败：' + e.message + '。请确认数据连接正常后点「⟳ 刷新」重试', 'error');
      }
    }
    state.hist.loading = false;
    render();
  }

  function histHTML() {
    const h = state.hist;
    let html = '<div class="card"><div class="card-title">SQL 执行历史' +
      '<span class="tag tag-gray">每条语句一行 · 完整留痕</span>' +
      '<span class="comp-actions"><button class="btn btn-sm" data-act="db-hist-refresh"' +
      (h.loading ? ' disabled' : '') + '>⟳ 刷新</button></span></div>' +
      '<div class="form-hint" style="margin-bottom:8px">记录「执行SQL」的每条语句：时间、批次、原文、结果与耗时，永久保留，可审计追溯。</div>' +
      histTable.html(h.loaded || h.items.length ? h.items : []);
    if (h.total > HIST_PAGE_SIZE) {
      const page = Math.floor(h.offset / HIST_PAGE_SIZE) + 1;
      const pages = Math.ceil(h.total / HIST_PAGE_SIZE);
      html += '<div class="comp-actions" style="margin-top:8px">' +
        '<button class="btn btn-sm" data-act="db-hist-prev"' + (h.offset > 0 && !h.loading ? '' : ' disabled') + '>‹ 上一页</button>' +
        '<span class="muted">第 ' + page + ' / ' + pages + ' 页 · 共 ' + h.total + ' 条</span>' +
        '<button class="btn btn-sm" data-act="db-hist-next"' + (h.offset + HIST_PAGE_SIZE < h.total && !h.loading ? '' : ' disabled') + '>下一页 ›</button>' +
        '</div>';
    }
    html += '</div>';
    return html;
  }

  // 「表结构检查」：GET /admin/db/schema → 差异表 + SQL 预填；无差异成功 toast 自动消失
  async function check() {
    if (state.checking) return;
    state.checking = true;
    render();
    try {
      const res = await api.get('/admin/db/schema');
      state.driver = String(res.driver || '');
      state.items = Array.isArray(res.items) ? res.items : [];
      state.sql = String(res.sql || '');
      state.loaded = true;
      if (!state.items.length) {
        toast('表结构检查完成：无差异，数据库结构与当前 SQL 源一致', 'success');
      } else {
        const autoCnt = state.items.filter(i => i.auto).length;
        toast('表结构检查完成：发现 ' + state.items.length + ' 处差异（可自动处理 ' + autoCnt +
          ' 处，已生成 SQL 供确认；其余请人工处理）', 'info');
      }
    } catch (e) {
      toast('表结构检查失败：' + e.message + '。请确认服务可达后重试', 'error');
    }
    state.checking = false;
    render();
  }

  // 估算编辑器中的语句条数（与后端拆句口径近似：剥离 -- 注释行后按分号切分计数）
  function countStatements(sql) {
    const stripped = sql.split('\n').filter(l => l.trim().indexOf('--') !== 0).join('\n');
    return stripped.split(';').map(s => s.trim()).filter(Boolean).length;
  }

  // 「执行SQL」：danger 强确认 → POST /admin/db/exec → 逐条结果（失败标红 + 常驻 toast 引导复核）
  async function execSQL() {
    const sql = codeEditor.value(EDITOR_ID).trim();
    if (!sql) { toast('编辑器内容为空：请先执行「表结构检查」生成 SQL，或手工输入要执行的语句', 'warning'); return; }
    const n = countStatements(sql);
    const ok = await confirmDialog({
      title: '执行 SQL 确认',
      message: '即将执行编辑器中的 <b>' + n + '</b> 条 SQL 语句，<b>直接作用于当前数据库' +
        (state.driver ? '（' + esc(state.driver) + '）' : '') + '</b>。' +
        'DDL 操作<b>不可回滚</b>，执行前建议先备份数据库。确定继续吗？',
      confirmText: '确认执行',
      danger: true,
      width: 480,
    });
    if (!ok) return;
    try {
      const res = await api.post('/admin/db/exec')({ sql: sql });
      state.exec = {
        results: (res.results || []).map((r, i) => ({
          idx: i + 1,
          sql: r.sql || '',
          ok: !!r.ok,
          rows: r.rows,
          error: r.error || '',
        })),
        executed: Number(res.executed) || 0,
        failed: Number(res.failed) || 0,
      };
      if (state.exec.failed > 0) {
        // 失败常驻 toast：发生了什么 + 为什么 + 下一步（后端 message 已含三要素）
        toast('SQL 执行部分失败：' + (res.message || ('有 ' + state.exec.failed + ' 条语句执行失败，已遇错即停；成功 ' +
          state.exec.executed + ' 条已生效')) + '。请根据下方结果修正编辑器内容后重发，完成后再次「表结构检查」复核', 'error');
      } else {
        toast('全部 ' + state.exec.executed + ' 条语句执行成功。建议再次点击「表结构检查」复核差异已消除', 'success');
      }
    } catch (e) {
      toast('SQL 执行请求失败：' + e.message + '。请确认服务可达后重试', 'error');
      return;
    }
    state.hist.loaded = false; // 执行后失效缓存：下次进执行历史页签重拉（含本次留痕）
    render();
  }

  // 复制编辑器 SQL 到剪贴板
  async function copySQL() {
    const sql = codeEditor.value(EDITOR_ID);
    if (!sql.trim()) { toast('编辑器内容为空，无可复制内容', 'warning'); return; }
    try {
      await navigator.clipboard.writeText(sql);
      toast('已复制 SQL 到剪贴板', 'success');
    } catch (e) {
      toast('复制失败：' + e.message + '。请手动选中编辑器内容复制', 'error');
    }
  }

  window.Rock.views.database = {
    load,
    render,
    check,
    execSQL,
    copySQL,
    actions: {
      'db-tab': function (el) {
        const tab = el.getAttribute('data-tab') || 'schema';
        if (tab === state.tab) return;
        state.tab = tab;
        if (tab === 'history' && !state.hist.loaded) loadHist();
        else render();
      },
      'db-check': function () { check(); },
      'db-exec': function () { execSQL(); },
      'db-copy-sql': function () { copySQL(); },
      'db-hist-refresh': function () { state.hist.offset = 0; loadHist({ force: true }); },
      'db-hist-prev': function () {
        state.hist.offset = Math.max(0, state.hist.offset - HIST_PAGE_SIZE);
        loadHist({ move: true });
      },
      'db-hist-next': function () {
        state.hist.offset += HIST_PAGE_SIZE;
        loadHist({ move: true });
      },
    },
  };
})();
