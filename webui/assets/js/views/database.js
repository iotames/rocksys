/* ==========================================================================
 * RockSys 管理控制台 - views/database.js 数据库页（服务 → 数据库）
 * 页签「表同步」：期望结构（当前运行 SQL 源，外挂优先、内嵌兜底）与实际结构
 * （当前数据连接 catalog）比对 → 差异分级表 + 生成 SQL 预填编辑器 →
 * danger 强确认执行 → 逐条结果（失败标红、常驻 toast 引导复核）。
 * 页签「SQL历史」：sql_exec_log 表审计记录（每条语句一行）分页展示，
 * 谁在何时执行了什么、成败与耗时，刷新/换会话均可追溯；点行弹详情查看完整语句与执行快照。
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
  const form = Rock.comp.form;          // 统一表单控件（input/select/checkbox，禁裸控件与内联样式）
  const fmtDateTime = Rock.util.fmtDateTime;
  const truncate = Rock.util.truncate;
  const fmtBytes = Rock.util.fmtBytes;

  // SQL 编辑器实例 id（codeEditor.html/wire/setValue 均按此 id 定位节点与状态）
  const EDITOR_ID = 'db-sql-editor';

  // 执行历史分页页大小
  const HIST_PAGE_SIZE = 20;

  // 页面内部状态
  const state = {
    loaded: false,      // 首次进入拉取后为 true（路由往返走缓存渲染，手动刷新才重拉）
    checking: false,    // 检查请求进行中（按钮防重）
    executing: false,   // 执行 SQL 请求进行中（danger DDL 防抖：不可回滚，禁止并发下发）
    execBackground: false, // 「后台执行」开关（长语句走任务中心，摆脱请求超时）：入状态防重渲染复位
    driver: '',         // 数据方言（后端返回，如 sqlite/mysql/postgres）
    items: [],          // 差异列表（level A-F）
    sql: '',            // 后端按自动项生成的 SQL（预填编辑器）
    exec: null,         // 最近一次执行结果 { results, executed, failed }
    tab: 'schema',      // 当前页签：'schema' 表同步 | 'overview' 表概览 | 'history' SQL历史
    hist: {             // 执行历史（服务端分页）
      loaded: false, loading: false,
      failed: false, err: '',   // 失败态与空态区分：加载失败不得渲染成「暂无执行记录」
      items: [], total: 0, offset: 0,
    },
    size: {             // 空间占用（公共状态区 + 数据表概览页签共用）
      loaded: false, loading: false, failed: false,
      calcTable: '',    // 正在按需计算占用的表名（逐表精确占用，避免全库页遍历）
      totalBytes: 0, tables: [],
    },
    geo: {              // GeoIP 关联表同步（geoip_list 增量构建/刷新；后台任务模式）
      running: false, result: null, error: null,
      taskId: '',         // 进行中同步任务 ID（仅内存；页面恢复经 /admin/tasks 查询）
      lastRunAt: '',      // 上次同步时间（schedule_list.geoip_sync 行 last_run_at）
      lastStatus: '',     // 上次同步状态 success/failed
    },
    data: {             // 「表数据」页签：数据源 / 表结构对齐 / 数据迁移
      dsn: {              // 外部数据源列表与添加表单
        loaded: false, loading: false, failed: false, items: [],
        testing: false, testMsg: '',
        // 表单值入状态：连通测试/列表刷新等会整卡重绘，输入内容只存 DOM 会被清空（须回显）
        form: { name: '', driver: 'sqlite', dsn: '' },
      },
      align: {            // 目标库表结构对齐
        code: '', checking: false, checked: false, items: [], sql: '', applying: false,
      },
      mig: {              // 数据迁移
        source: 'self', target: '', batch: 1000, mode: 'replace',
        selected: {},       // 表名 → 是否选中
        running: false, taskId: '', progress: null, summary: '', errText: '',
      },
    },
  };

  // ── 后台任务通用交互（提交 → 轮询 → 进度/结果；四处复用）────────────────
  // 后端契约：GET /admin/tasks 列表、GET /admin/tasks/{id} 详情（含 progress/status/result）、
  // POST /admin/tasks/{id}/cancel。任务记录不持久化（重启清空），不存在即 404。
  // 页面恢复：各卡加载时查 /admin/tasks，存在 created_by 匹配且 running 的任务即续上轮询，
  // 任务 ID 不落前端存储，切页签/刷新不失联。

  // 任务轮询与耗时文案复用全局共用组件（Rock.ui.pollTask / findRunningTask / fmtTaskCost）：
  // 同一实现亦服务概览页的 GeoIP「立即同步」，两页共用避免各写一套轮询。
  const pollTask = Rock.ui.pollTask;
  const findRunningTask = Rock.ui.findRunningTask;
  const fmtTaskCost = Rock.ui.fmtTaskCost;

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

  // 执行历史表（client 模式展示当前页；分页由页内上一页/下一页按钮驱动服务端 offset；点行弹详情）
  const histTable = Rock.comp.dataTable.create({
    ns: 'db-hist',
    rowKey: r => String(r.id),
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
    detail: { title: 'SQL 执行详情' }, // fields 由 onDetail 动态给出
  });
  // 行详情弹层：完整语句 + 执行快照（列表仅截断展示，详情给全量审计字段）
  histTable.onDetail = function (row) {
    Rock.comp.detailModal.show({
      title: 'SQL 执行详情',
      width: 720,
      row: row,
      fields: [
        { key: 'time', label: '执行时间', render: r => '<span class="mono">' + esc(fmtDateTime(r.time)) + '</span>' },
        { key: 'batch_id', label: '批次 / 序号', render: r =>
            '<span class="mono">' + esc(r.batch_id) + '</span> <span class="muted">#' + esc(r.seq) + '</span>' },
        { key: 'sql_text', label: '完整语句', pre: true, copy: true },
        { key: 'ok', label: '结果', render: r => r.ok
            ? '<span class="tag tag-green">成功</span>'
            : '<span class="tag tag-orange">失败</span>' },
        { key: 'rows_affected', label: '影响行数', render: r => '<span class="mono">' + esc(r.rows_affected) + '</span>' },
        { key: 'duration_ms', label: '耗时', render: r => '<span class="mono">' + esc(r.duration_ms) + 'ms</span>' },
        { key: 'client_ip', label: '来源 IP', render: r => '<span class="mono">' + esc(r.client_ip || '—') + '</span>' },
        { key: 'source', label: '执行渠道', render: r => esc(r.source || '—') },
        { key: 'error', label: '失败原因', render: r => r.error
            ? '<span class="mono is-error-text">' + esc(r.error) + '</span>' : '<span class="muted">—</span>' },
      ],
    });
  };

  // 数据表概览表（空间占用统计：表名/备注/条数/占用空间，含占比条）
  const overviewTable = Rock.comp.dataTable.create({
    ns: 'db-overview',
    columns: [
      { key: 'name', label: '表名', cls: 'mono', render: r => '<span class="log-path" title="' + esc(r.name) + '">' + esc(truncate(r.name, 40)) + '</span>' },
      { key: 'comment', label: '表备注', render: r => esc(r.comment || '—') },
      { key: 'rows', label: '数据条数', cls: 'mono', render: r => esc(fmtIntNA(r.rows)) },
      // 占用空间拆两列：数据 / 索引（SQLite 表 B-tree 含溢出页；MySQL 聚簇索引与二级索引；PG 表堆与索引）。
      // 未计算的表在两列统一以「计算」按钮呈现（SQLite 逐表占用需遍历页树，约数秒~数十秒）。
      // 占比条各自同口径：数据列按「已统计数据合计」、索引列按「已统计索引合计」，两列不共用分母。
      { key: 'data_bytes', label: '数据', render: r => {
          if (!r.bytes_known) {
            // 计算是全局串行的（一次只允许一张表在算）：任何一行在算时其余行一并禁用，
            // 否则点击被静默忽略，用户以为页面卡死。
            const calc = state.size.calcTable;
            const me = calc === r.name;
            return '<button class="btn btn-sm" data-act="db-table-size" data-table="' + esc(r.name) + '"' +
              (calc ? ' disabled' : '') + '>' + (me ? '计算中…' : '计算') + '</button>';
          }
          return sizeCellHTML(r.data_bytes, 'data_bytes', r, '数据');
        } },
      { key: 'index_bytes', label: '索引', render: r => r.bytes_known ? sizeCellHTML(r.index_bytes, 'index_bytes', r, '索引') : '<span class="muted">—</span>' },
    ],
    paging: { mode: 'client' },
    emptyText: '库内暂无业务表',
  });

  // 条数千分位（0 显示 0；空值显示 —）
  function fmtIntNA(n) {
    n = Number(n) || 0;
    return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  }

  // 数据/索引单元格：数值 + 各自口径的占比条。
  // 分母按列同口径求和（数据列=已统计表的数据合计，索引列=已统计表的索引合计），
  // 且只统计 bytes_known 的表——未计算的表不计入分母，避免把「未统计」当成小表。
  function sizeCellHTML(part, key, r, label) {
    const known = (state.size.tables || []).filter(t => t.bytes_known);
    const denom = known.reduce((s, t) => s + (Number(t[key]) || 0), 0);
    const pct = denom > 0 ? Math.min(100, (Number(part) / denom) * 100) : 0;
    const total = Number(r.bytes) || 0;
    const share = total > 0 ? (Number(part) / total * 100) : 0;
    const title = label + ' ' + fmtBytes(part) +
      '：占已统计 ' + known.length + ' 张表的' + label + '合计（' + fmtBytes(denom) + '）的 ' +
      (pct >= 1 ? pct.toFixed(1) : '<1') + '%；占该表合计（' + fmtBytes(total) + '）的 ' +
      (share >= 1 ? share.toFixed(1) : '<1') + '%';
    return '<span class="mono">' + esc(fmtBytes(part)) + '</span>' +
      '<div class="db-size-bar" title="' + esc(title) + '">' +
      '<div class="db-size-bar-fill" style="width:' + pct.toFixed(2) + '%"></div></div>';
  }

  // 首次进入挂分页控件事件（页容器为持久元素）
  let bound = false;
  function ensureBind() {
    if (bound) return;
    const host = $('#page-database');
    if (host) {
      diffTable.bind(host); execTable.bind(host); histTable.bind(host); overviewTable.bind(host);
      bindFormSync();
      bound = true;
    }
  }

  // 页面加载：首次拉一次表结构检查，其余路由往返走缓存（手动刷新按钮 force 重拉）
  async function load(opts) {
    ensureBind();
    if (state.loaded && !opts.force) { render(); return; }
    const host = $('#page-database');
    if (!state.loaded && host && !host.innerHTML.trim()) host.innerHTML = skeletonHTML(5);
    loadSize(); // 空间占用异步加载（不阻塞表结构检查主流程，失败静默走占位）
    loadGeoSyncMeta(); // 上次同步时间（定时任务登记行，失败静默走「未登记」占位）
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
    return '<div class="comp-actions comp-actions-lead">' +
      '<button class="btn btn-primary" data-act="db-check"' + (state.checking ? ' disabled' : '') + '>' +
      (state.checking ? '检查中…' : '表结构检查') + '</button>' +
      '<button class="btn btn-danger" data-act="db-exec"' + (hasSQL && !state.executing ? '' : ' disabled') +
      ' title="执行编辑器中的 SQL 语句（直接作用于当前数据库）">' + (state.executing ? '执行中…' : '执行SQL') + '</button>' +
      form.checkbox({ id: 'db-exec-bg', label: '后台执行', checked: state.execBackground, attrs: { 'data-act': 'db-exec-bg' } }) +
      '<span class="form-hint">长语句勾选后台执行，摆脱请求超时；提交后经任务查询取逐条结果</span>' +
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
        '<button class="btn btn-sm btn-danger" data-act="db-exec"' + (state.sql.trim() && !state.executing ? '' : ' disabled') + '>' +
        (state.executing ? '执行中…' : '执行SQL') + '</button>' +
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
      sizeBarHTML() +
      Rock.comp.tabs.tabsHTML(
        [{ name: 'schema', label: '表同步' }, { name: 'overview', label: '表概览' }, { name: 'data', label: '表数据' }, { name: 'history', label: 'SQL历史' }],
        state.tab,
        { act: 'db-tab', nameAttr: 'data-tab' }
      ) +
      '<div class="tab-pane">' + (state.tab === 'history' ? histHTML()
        : (state.tab === 'overview' ? overviewHTML()
          : (state.tab === 'data' ? dataHTML() : schemaHTML()))) + '</div>';
    // 编辑器联动：内容变化即时同步「执行SQL」按钮可用态（不整页重绘，避免打断输入）
    if (state.tab === 'schema' && state.items.length) {
      codeEditor.wire(EDITOR_ID, {
        onChange: function (src) {
          state.sql = src;
          const btn = document.querySelector('#page-database [data-act="db-exec"]');
          if (btn) btn.disabled = state.executing || !src.trim();
        },
      });
    }
  }

  // ── 空间占用：公共状态区（页签上方，总空间常驻）+ 数据表概览页签 ─────────┐

  // 拉取空间占用统计（GET /admin/db/size，只读；页面加载与概览页签刷新共用）
  // 该端点为精确统计（大库 COUNT(*) 秒级~十秒级），显式放宽超时到 60 秒，避免被默认 5 秒误掐断。
  // force=true 表示用户主动触发（点「加载」/「⟳」）：失败必须给出统一报错提示；
  // 自动加载（页面进入）失败仅在服务端有响应时提示，网络不可达静默并由状态栏占位承载。
  // 上次同步时间：读 schedule_list 登记行 geoip_sync 的 last_run_at；
  // 静默刷新（失败不弹 toast，卡片显示「未登记」占位），同步成功后由 runGeoSync 触发重拉。
  async function loadGeoSyncMeta() {
    try {
      const r = await api.get('/admin/schedule/list');
      const row = ((r && r.tasks) || []).find(function (t) { return t.name === 'geoip_sync'; });
      if (row) {
        state.geo.lastRunAt = String(row.last_run_at || '');
        state.geo.lastStatus = String(row.last_status || '');
        render();
      }
    } catch (e) { /* 静默：保持占位文案 */ }
  }

  async function loadSize(force) {
    if (state.size.loading) return;
    if (state.size.loaded && !force) { render(); return; }
    state.size.loading = true;
    render();
    try {
      const res = await api.get('/admin/db/size', 60000);
      state.size.totalBytes = Number(res.total_bytes) || 0;
      state.size.tables = Array.isArray(res.tables) ? res.tables : [];
      state.size.loaded = true;
      state.size.failed = false;
    } catch (e) {
      state.size.failed = true;
      if (force || e.status !== 0) {
        toast('空间统计查询失败：' + e.message + '。请确认数据连接正常后点「重试」', 'error');
      }
    }
    state.size.loading = false;
    render();
  }

  // 单表精确占用按需计算（GET /admin/db/table_size）：SQLite 下逐表占用需遍历全库页树
  // （大库首次数秒~数十秒），故不随页面默认统计；用户点「计算」时才触发，结果服务端缓存 10 分钟。
  async function calcTableSize(table) {
    if (!table || state.size.calcTable) return;
    state.size.calcTable = table;
    render();
    try {
      const res = await api.get('/admin/db/table_size?table=' + encodeURIComponent(table), 120000);
      const bytes = Number(res && res.bytes) || 0;
      const data = Number(res && res.data_bytes) || 0;
      const idx = Number(res && res.index_bytes) || 0;
      let hit = false;
      (state.size.tables || []).forEach(function (t) {
        if (t.name === table) {
          t.bytes = bytes; t.data_bytes = data; t.index_bytes = idx; t.bytes_known = true; hit = true;
        }
      });
      if (!hit) loadSize(true); // 表清单已变化，重拉一次保证一致
      toast(table + ' 占用空间：数据 ' + fmtBytes(data) + ' + 索引 ' + fmtBytes(idx) + ' = ' + fmtBytes(bytes) +
        (res && res.cached ? '（服务端缓存）' : ''), 'success');
    } catch (e) {
      toast('「' + table + '」占用空间计算失败：' + e.message + '。可稍后点「计算」重试', 'error');
    }
    state.size.calcTable = '';
    render();
  }

  // 公共状态区：页签上方常驻展示库级空间占用（与全局配置页搜索栏同层级的公共数据状态）
  function sizeBarHTML() {
    const sz = state.size;
    let inner;
    if (sz.loading) {
      inner = '<span class="muted">空间统计中…</span>';
    } else if (sz.failed) {
      inner = '<span class="muted">空间占用：加载失败</span>' +
        '<button class="btn btn-sm" data-act="db-size-refresh">重试</button>';
    } else if (!sz.loaded) {
      inner = '<span class="muted">空间占用：未加载</span>' +
        '<button class="btn btn-sm" data-act="db-size-refresh">加载</button>';
    } else {
      inner = '<span>数据库占用总空间：<b class="mono">' + esc(fmtBytes(sz.totalBytes)) + '</b></span>' +
        '<span class="tag tag-blue">' + sz.tables.length + ' 张业务表</span>' +
        (state.driver ? '<span class="tag tag-gray">' + esc(state.driver) + '</span>' : '') +
        '<button class="btn btn-sm" data-act="db-size-refresh"' + (sz.loading ? ' disabled' : '') + '>⟳</button>';
    }
    return '<div class="db-statusbar">' + inner + '</div>';
  }

  function overviewHTML() {
    const sz = state.size;
    let html = '<div class="card"><div class="card-title">数据表概览' +
      '<span class="comp-actions"><button class="btn btn-sm" data-act="db-size-refresh"' +
      (sz.loading ? ' disabled' : '') + '>' + (sz.loaded ? '⟳ 刷新' : '加载') + '</button></span></div>' +
      '<div class="form-hint" style="margin-bottom:8px">数据条数为精确统计（动态 COUNT(*)）；总占用取自数据库系统表。' +
      '占用空间按「数据 + 索引」两列拆分：' +
      (state.driver === 'sqlite'
        ? 'SQLite 表数据为表 B-tree 页（含大字段溢出页），索引为各索引 B-tree 之和；逐表占用需遍历全库页树（大库首次数秒~数十秒），故按需点「计算」触发，结果服务端缓存 10 分钟。'
        : state.driver === 'mysql'
          ? 'MySQL/InnoDB 数据取 DATA_LENGTH（聚簇索引即数据本体），索引取 INDEX_LENGTH（二级索引）；碎片页 DATA_FREE 不计入。'
          : 'PostgreSQL 数据取表堆主体，索引取该表全部索引；不含 TOAST（合计口径含 TOAST，故可能略大于两列之和）。') + '</div>';
    // 三态区分（避免把「尚未加载」误报成「库里没有表」）：
    // 未加载 → 引导加载；加载失败 → 行内错误 + 重试；已加载且为空 → 才是真的无业务表。
    if (sz.loading) {
      html += '<div class="load-hint"><span class="load-spin"></span>空间统计中，大库首次统计需数秒</div>';
    } else if (sz.failed) {
      html += Rock.comp.empty.emptyCard({ text: '空间占用统计加载失败（表清单与占用空间暂不可用）', br: true,
        action: '<button class="btn btn-sm btn-primary" data-act="db-size-refresh">重试</button>' });
    } else if (!sz.loaded) {
      html += Rock.comp.empty.emptyCard({ text: '尚未加载空间占用（表清单、数据条数与占用空间按需统计，避免打开页面即对大库做全量统计）', br: true,
        action: '<button class="btn btn-sm btn-primary" data-act="db-size-refresh">加载</button>' });
    } else {
      html += overviewTable.html(sz.tables);
    }
    html += '</div>';
    return html;
  }

  // GeoIP 关联表同步卡：增量构建/刷新 geoip_list（一 IP 一行，
  // 与 access_log / shield_event 按 client_ip 关联），显示上次同步时间（schedule_list.geoip_sync 行）。
  // 同步为后台任务模式：提交即返回任务 ID，轮询任务详情展示进度与结果，不受 HTTP 超时限制。
  function geoSyncHTML() {
    const g = state.geo;
    const lastSync = g.lastRunAt
      ? '上次同步：' + esc(String(g.lastRunAt).replace('T', ' ').slice(0, 19)) + ' UTC' +
        (g.lastStatus ? '（' + esc(g.lastStatus) + '）' : '')
      : '上次同步：未登记（尚未执行过同步）';
    let body;
    if (g.error) {
      body = '<div class="alert alert-warn">' + esc(g.error) + '</div>' +
        '<button class="btn btn-sm btn-primary" data-act="db-geoip-sync">重试同步</button>';
    } else if (g.result) {
      body = '<div class="alert alert-info">' + esc(g.result.text || '完成') + '</div>' +
        '<button class="btn btn-sm" data-act="db-geoip-sync">再次同步（处理新增缺失 IP）</button>';
    } else {
      body = '<button class="btn btn-sm btn-primary" data-act="db-geoip-sync"' + (g.running ? ' disabled' : '') + '>' +
        (g.running ? '同步中…（提交后可在任务列表观察进度）' : '开始同步') + '</button>';
    }
    return '<div class="card"><div class="card-title">GeoIP 数据同步' +
      '<span class="tag tag-blue">维护工具</span></div>' +
      '<div class="form-hint">' + lastSync + '。</div>' +
      '<div class="form-hint">扫描 access_log / shield_event 中「尚未入 geoip_list 关联表」的 IP，' +
      '按当前已加载的 mmdb 数据逐 IP 解析后写入 geoip_list（一 IP 一行，明细与统计经 client_ip 关联取地理信息）。' +
      '私网/回环等无地理信息的 IP 跳过并计数；同步分趟执行：每趟有服务端时间上限（约 20 秒），' +
      '到点即从断点收工，重复执行自动续接。' +
      '自动同步间隔经 GEOIP_SYNC_INTERVAL 配置（见「定时任务」页）。</div>' +
      body + '</div>';
  }

  // 提交同步任务（POST /admin/db/geoip_sync → {task_id}）并轮询至终态。
  async function runGeoSync() {
    if (state.geo.running) return;
    const ok = await confirmDialog({
      title: 'GeoIP 数据同步',
      message: '将按当前 mmdb 数据，把 access_log / shield_event 中尚未入 geoip_list 的 IP ' +
        '解析后写入关联表（一 IP 一行；已入表 IP 不动，其他数据不变）。是否继续？',
      confirmText: '开始同步',
    });
    if (!ok) return;
    state.geo.running = true;
    state.geo.error = null;
    render();
    try {
      const r = await api.post('/admin/db/geoip_sync')();
      state.geo.taskId = (r && r.task_id) || '';
      pollTask(state.geo.taskId, {
        onRunning: function () {},
        onDone: finishGeoSync,
        onFailed: finishGeoSync,
      });
    } catch (e) {
      state.geo.running = false;
      state.geo.error = e.message || '同步失败';
      // mmdb 提示仅在服务端真返回 503（geo 未就绪）时附带，避免误导
      const hint = (e && e.status === 503) ? '。若提示 mmdb 未加载，请先放置数据文件并重启服务' : '';
      toast(state.geo.error + hint, 'error');
      render();
    }
  }

  // 同步任务终态：展示报告、刷新登记与概览。
  function finishGeoSync(task) {
    const g = state.geo;
    g.running = false;
    g.taskId = '';
    if (task.status === 'done') {
      const text = (task.progress && task.progress.text) || task.result || '完成';
      g.result = { text: text };
      toast('GeoIP 数据同步完成（' + fmtTaskCost(task) + '）', 'success');
      loadGeoSyncMeta(); // 上次同步时间随本次执行刷新
      loadSize(true);    // 同步不改两表行数但刷新概览无妨
    } else {
      g.error = task.result || '同步失败';
      toast('GeoIP 数据同步失败：' + g.error + '。同步分趟执行，已完成部分已写入，稍候再次点击即从断点继续', 'error');
    }
    render();
  }

  // ── 「表数据」页签：数据源 → 表结构对齐 → 数据迁移 → GeoIP 数据同步 ──────
  // 按迁移流程自上而下排布：先配置外部数据源，再对目标库对齐表结构，然后迁移数据；
  // GeoIP 数据同步同属数据维护工具，归入本页签。

  function dataHTML() {
    return dsnHTML() + alignHTML() + migrateHTML() + geoSyncHTML();
  }

  // 进入「表数据」页签：拉数据源列表 + 恢复进行中任务（迁移/GeoIP 同步）。
  function enterDataTab() {
    loadDsn();
    recoverRunningTasks();
    render();
  }

  // 页面恢复：查任务列表，按 created_by 匹配本页两处长任务，running 即续上轮询。
  function recoverRunningTasks() {
    findRunningTask('migrate').then(function (t) {
      if (!t || state.data.mig.running) return;
      const m = state.data.mig;
      m.running = true;
      m.taskId = t.id;
      applyMigrateProgress(t);
      pollTask(t.id, {
        onRunning: applyMigrateProgress,
        onDone: function (task) { finishMigrate(task); },
        onFailed: function (task) { finishMigrate(task); },
      });
      render();
    });
    findRunningTask('geoip_sync').then(function (t) {
      if (!t || state.geo.running) return;
      state.geo.running = true;
      state.geo.taskId = t.id;
      pollTask(t.id, {
        onRunning: function () {},
        onDone: function (task) { finishGeoSync(task); },
        onFailed: function (task) { finishGeoSync(task); },
      });
      render();
    });
  }

  // ── 卡 1：数据源 ────────────────────────────────────────────────────────
  async function loadDsn(force) {
    const d = state.data.dsn;
    if (d.loading) return;
    if (d.loaded && !force) { render(); return; }
    d.loading = true;
    render();
    try {
      const r = await api.get('/admin/db/dsn');
      d.items = Array.isArray(r.items) ? r.items : [];
      d.loaded = true; d.failed = false;
      // 对齐/迁移卡的目标下拉默认取第一个数据源。
      if (!state.data.align.code && d.items.length) state.data.align.code = d.items[0].code;
      if (!state.data.mig.target && d.items.length) state.data.mig.target = d.items[0].code;
    } catch (e) {
      d.failed = true;
      if (e.status !== 0) toast('数据源列表加载失败：' + e.message + '。请确认服务可达后重试', 'error');
    }
    d.loading = false;
    render();
  }

  function dsnHTML() {
    const d = state.data.dsn;
    let rows = d.items.map(function (it) {
      return '<tr><td class="mono">' + esc(it.name) + '</td>' +
        '<td><span class="tag tag-gray">' + esc(it.driver) + '</span></td>' +
        '<td class="mono" title="凭据已脱敏展示">' + esc(it.dsn) + '</td>' +
        '<td><button class="btn btn-sm btn-danger" data-act="db-dsn-del" data-code="' + esc(it.code) +
        '" data-name="' + esc(it.name) + '">删除</button></td></tr>';
    }).join('');
    let body;
    if (d.loading) {
      body = '<div class="load-hint"><span class="load-spin"></span>加载中…</div>';
    } else if (d.failed && !d.items.length) {
      body = Rock.comp.empty.emptyCard({ text: '数据源列表加载失败', br: true,
        action: '<button class="btn btn-sm btn-primary" data-act="db-dsn-reload">重试</button>' });
    } else {
      body = d.items.length
        ? '<table class="table"><thead><tr><th>连接名</th><th>驱动</th><th>DSN（脱敏）</th><th>操作</th></tr></thead><tbody>' + rows + '</tbody></table>'
        : '<div class="muted form-gap">暂无外部数据源。添加目标库（如开发用 SQLite → 生产 MySQL）后即可做结构对齐与数据迁移。</div>';
    }
    return '<div class="card"><div class="card-title">数据源' +
      '<span class="tag tag-blue">迁移目标与备选源</span>' +
      '<span class="comp-actions"><button class="btn btn-sm" data-act="db-dsn-reload"' + (d.loading ? ' disabled' : '') + '>⟳ 刷新</button></span></div>' +
      body +
      form.inline(
        form.input({ id: 'db-dsn-name', scope: 'dsn.form', field: 'name', width: 'sm',
          placeholder: '连接名（唯一，如 prod-mysql）', value: d.form.name }) +
        form.select({ id: 'db-dsn-driver', scope: 'dsn.form', field: 'driver', width: 'xs',
          options: [['sqlite', 'sqlite'], ['mysql', 'mysql'], ['postgres', 'postgres']], selected: d.form.driver }) +
        form.input({ id: 'db-dsn-dsn', scope: 'dsn.form', field: 'dsn', width: 'lg', mono: true,
          placeholder: '连接串 DSN（含凭据，服务端脱敏存储展示）', value: d.form.dsn }) +
        '<button class="btn btn-sm" data-act="db-dsn-test"' + (d.testing ? ' disabled' : '') + '>' +
        (d.testing ? '测试中…' : '连通测试') + '</button>' +
        '<button class="btn btn-sm btn-primary" data-act="db-dsn-add">添加</button>'
      ) +
      form.hint('连通测试不落盘，可先测试未保存的连接串；MySQL 密码含 @ 会被拦截（需先 URL 编码）。') +
      (d.testMsg ? form.hint(d.testMsg) : '') +
      '</div>';
  }

  // 读取添加表单输入：状态优先（输入即同步、重渲染不清空），DOM 仅作兜底。
  function dsnFormValues() {
    const f = state.data.dsn.form;
    const pick = (id, key) => {
      const el = document.getElementById(id);
      return el && el.value ? el.value : f[key];
    };
    return {
      name: (pick('db-dsn-name', 'name') || '').trim(),
      driver: pick('db-dsn-driver', 'driver') || 'sqlite',
      dsn: (pick('db-dsn-dsn', 'dsn') || '').trim(),
    };
  }

  // 表单值与状态同步：页内 input/change 委托（data-scope + data-field），
  // 任何整卡重渲染都从状态回显，用户已输入内容不因「连通测试」等操作丢失。
  function bindFormSync() {
    const host = $('#page-database');
    if (!host || host.getAttribute('data-form-sync') === '1') return;
    const onEdit = function (e) {
      const el = e.target && e.target.closest ? e.target.closest('[data-scope][data-field]') : null;
      if (!el) return;
      const target = (el.getAttribute('data-scope') || '').split('.').reduce(function (acc, k) {
        return acc ? acc[k] : undefined;
      }, state.data);
      if (target) target[el.getAttribute('data-field')] = el.value;
    };
    host.addEventListener('input', onEdit);
    host.addEventListener('change', onEdit);
    host.setAttribute('data-form-sync', '1');
  }

  async function addDsn() {
    const f = dsnFormValues();
    if (!f.name.trim() || !f.dsn) {
      toast('连接名与 DSN 均不能为空：请补全后重试', 'warning');
      return;
    }
    try {
      await api.post('/admin/db/dsn')({ name: f.name, driver: f.driver, dsn: f.dsn });
      toast('数据源「' + f.name + '」已添加并持久化（重启保留）', 'success');
      // 添加成功后仅清空名称与 DSN，驱动保留（便于连续添加同类数据源）
      state.data.dsn.form.name = '';
      state.data.dsn.form.dsn = '';
      loadDsn(true);
    } catch (e) {
      toast('添加数据源失败：' + e.message + '。请核对驱动/连接名/连接串后重试', 'error');
    }
  }

  async function testDsn() {
    const f = dsnFormValues();
    if (!f.dsn) { toast('请先填写连接串 DSN 再测试连通性', 'warning'); return; }
    const d = state.data.dsn;
    d.testing = true; d.testMsg = '';
    render();
    try {
      const r = await api.post('/admin/db/dsn/test')({ driver: f.driver, dsn: f.dsn });
      d.testMsg = '连通成功：' + (r.version || '未知版本');
      toast('连通测试成功：' + (r.version || ''), 'success');
    } catch (e) {
      d.testMsg = '连通失败：' + e.message;
      toast('连通测试失败：' + e.message, 'error');
    }
    d.testing = false;
    render();
  }

  async function delDsn(el) {
    const code = el.getAttribute('data-code') || '';
    const name = el.getAttribute('data-name') || '';
    const ok = await confirmDialog({
      title: '删除数据源',
      message: '将删除数据源「' + esc(name) + '」的配置记录（不影响目标库本身的数据）。有迁移任务进行中时服务端会拒绝删除。确定删除吗？',
      confirmText: '删除', danger: true,
    });
    if (!ok) return;
    try {
      await api.post('/admin/db/dsn/delete')({ code: code });
      toast('数据源「' + name + '」已删除', 'success');
      loadDsn(true);
    } catch (e) {
      toast('删除失败：' + e.message, 'error');
    }
  }

  // ── 卡 2：表结构对齐 ────────────────────────────────────────────────────
  function alignHTML() {
    const a = state.data.align;
    let body = form.inline(
      form.select({ id: 'db-align-target', scope: 'align', field: 'code',
        // 无数据源时给占位项：空下拉看不出「为什么不能选」，占位文案直接指路
        options: state.data.dsn.items.length
          ? state.data.dsn.items.map(k => [k.code, k.name + '（' + k.driver + '）'])
          : [['', '请先在上方添加数据源']],
        selected: a.code, disabled: !state.data.dsn.items.length, width: 'md' }) +
      '<button class="btn btn-sm btn-primary" data-act="db-align-check"' + (!a.code || a.checking ? ' disabled' : '') + '>' +
      (a.checking ? '检查中…' : '检查差异') + '</button>' +
      (a.checked ? '<button class="btn btn-sm btn-danger" data-act="db-align-apply"' + (a.applying ? ' disabled' : '') + '>' +
        (a.applying ? '对齐中…' : '执行对齐') + '</button>' : '')
    );
    if (a.checked) {
      body += !a.items.length
        ? '<div class="alert alert-info">目标库结构与期望一致，无需对齐。</div>'
        : '<div class="form-hint">发现 ' + a.items.length + ' 处差异，将对目标库逐条执行以下 DDL（遇错即停；不写本机审计表）：</div>' +
          '<pre class="mono code-block">' + esc(a.sql) + '</pre>';
    }
    return '<div class="card"><div class="card-title">表结构对齐' +
      '<span class="tag tag-blue">迁移前置</span></div>' +
      '<div class="form-hint">目标 = 外部数据源；期望结构 = 本系统内嵌 SQL 脚本（与「表同步」同源）。' +
      '迁移前先对齐目标库结构，保证双方同名列一致。</div>' + body + '</div>';
  }

  async function alignCheck() {
    const a = state.data.align;
    a.code = (document.getElementById('db-align-target') || {}).value || a.code;
    if (!a.code) { toast('请先在数据源卡添加目标库', 'warning'); return; }
    a.checking = true;
    render();
    try {
      const r = await api.get('/admin/db/migrate/schema?code=' + encodeURIComponent(a.code));
      a.items = Array.isArray(r.items) ? r.items : [];
      a.sql = String(r.sql || '');
      a.checked = true;
      toast(a.items.length ? '差异检查完成：发现 ' + a.items.length + ' 处差异，请确认 DDL 后执行对齐'
        : '差异检查完成：目标库结构与期望一致，无需对齐', 'info');
    } catch (e) {
      toast('差异检查失败：' + e.message + '。请确认目标库连接正常后重试', 'error');
    }
    a.checking = false;
    render();
  }

  async function alignApply() {
    const a = state.data.align;
    if (!a.checked || a.applying) return;
    const ok = await confirmDialog({
      title: '执行表结构对齐',
      message: '将对目标库逐条执行 <b>' + a.items.length + '</b> 处差异对应的 DDL，遇错即停；前序已执行的语句不可回滚。确定继续吗？',
      confirmText: '执行对齐', danger: true,
    });
    if (!ok) return;
    a.applying = true;
    render();
    try {
      const r = await api.post('/admin/db/migrate/schema_apply?code=' + encodeURIComponent(a.code))({});
      pollTask(r.task_id, {
        onRunning: function () {},
        onDone: function () {
          a.applying = false;
          toast('表结构对齐完成。建议点「检查差异」复核已无差异', 'success');
          alignCheck();
        },
        onFailed: function (task) {
          a.applying = false;
          toast('表结构对齐失败：' + (task.result || '未知错误') + '。已执行部分生效且不可回滚；可修正后重新检查并对齐剩余差异', 'error');
          render();
        },
      });
      return; // applying 由轮询回调复位
    } catch (e) {
      a.applying = false;
      toast('提交对齐任务失败：' + e.message, 'error');
      render();
    }
  }

  // ── 卡 3：数据迁移 ──────────────────────────────────────────────────────
  // 表清单 = 期望结构业务表（表同步覆盖的那批）。前端取自空间统计的表清单
  // （运行库业务表），未加载时提示先在「表概览」加载。
  function migrateTables() {
    return (state.size.tables || []).map(t => t.name);
  }

  function migrateHTML() {
    const m = state.data.mig;
    const dsns = state.data.dsn.items;
    const srcOptions = [['self', '本机运行库']].concat(dsns.map(it => [it.code, it.name]));
    const tgtOptions = dsns.length
      ? dsns.map(it => [it.code, it.name + '（' + it.driver + '）'])
      : [['', '请先在上方添加数据源']];
    const tables = migrateTables();
    const tableList = tables.length
      ? form.checkList(tables.map(function (name) {
          const checked = !!m.selected[name];
          return '<label class="form-check"><input type="checkbox" data-act="db-mig-table" data-table="' + esc(name) + '"' +
            (checked ? ' checked' : '') + '> <span class="mono">' + esc(name) + '</span></label>';
        }).join(''))
      : '<div class="muted">表清单不可用：请先到「表概览」页签加载空间统计（表清单取自运行库业务表）。</div>';
    let body =
      form.inline(
        '<label>源 ' + form.select({ id: 'db-mig-source', scope: 'mig', field: 'source',
          options: srcOptions, selected: m.source, width: 'sm' }) + '</label>' +
        '<label>目标 ' + form.select({ id: 'db-mig-target', scope: 'mig', field: 'target',
          options: tgtOptions, selected: m.target, disabled: !dsns.length, width: 'md' }) + '</label>' +
        '<label>冲突策略 ' + form.select({ id: 'db-mig-mode', scope: 'mig', field: 'mode',
          options: [['replace', '清空重灌'], ['skip', '跳过冲突']], selected: m.mode, width: 'sm' }) + '</label>' +
        '<label>批次 ' + form.input({ id: 'db-mig-batch', scope: 'mig', field: 'batch', type: 'number',
          value: String(m.batch), width: 'xs', attrs: { min: '100', max: '10000' } }) + '</label>'
      ) +
      form.hint('批次为性能参考值：批越大吞吐越高、内存与目标库单语句负载越高；' +
        '超出目标库参数上限时服务端自动切分执行，无需手工计算。批次仅本次会话生效，刷新复位 1000。') +
      '<div class="form-label">迁移表：</div>' + tableList +
      '<button class="btn btn-danger" data-act="db-mig-start"' + (m.running ? ' disabled' : '') + '>' +
      (m.running ? '迁移进行中…' : '启动迁移') + '</button>' +
      (m.running ? ' <button class="btn btn-sm" data-act="db-mig-cancel">取消整个任务（当前批完成后停止）</button>' : '');
    if (m.summary || m.errText) {
      body += '<div class="form-hint">' +
        (m.errText ? '<span class="is-error-text">' + esc(m.errText) + '</span>　' : '') +
        esc(m.summary) + '</div>';
    }
    if (m.progress && m.progress.length) {
      body += '<table class="table table-top"><thead><tr><th>表</th><th>状态</th><th>进度</th><th>说明</th><th>操作</th></tr></thead><tbody>' +
        m.progress.map(function (t) {
          const st = { pending: ['tag-gray', '待迁移'], running: ['tag-blue', '迁移中'], done: ['tag-green', '已完成'], failed: ['tag-orange', '失败'], cancelled: ['tag-gray', '已取消'] }[t.status] || ['tag-gray', t.status];
          const pct = t.rows_total > 0 ? Math.round((t.rows_done / t.rows_total) * 100) : (t.status === 'done' ? 100 : 0);
          return '<tr><td class="mono">' + esc(t.table) + '</td>' +
            '<td><span class="tag ' + st[0] + '">' + st[1] + '</span></td>' +
            '<td class="mono">' + fmtIntNA(t.rows_done) + ' / ' + (t.rows_total ? fmtIntNA(t.rows_total) : '?') + '（' + pct + '%）</td>' +
            '<td>' + esc(t.err || '') + '</td>' +
            '<td>' + (t.status === 'pending' && m.running ? '<button class="btn btn-sm" data-act="db-mig-cancel-table" data-table="' + esc(t.table) + '">移除</button>' : '') + '</td></tr>';
        }).join('') + '</tbody></table>';
    }
    return '<div class="card"><div class="card-title">数据迁移' +
      '<span class="tag tag-orange">danger · 作用于目标库</span></div>' +
      '<div class="form-hint">同名字段跨方言直迁（先完成表结构对齐）；目标不可选本机运行库（防误覆盖生产数据）。' +
      '单表失败不阻塞后续表；同互斥集任务串行（互斥规则见「后台任务」页并发管控卡），其余任务并行不设限。</div>' + body + '</div>';
  }

  // 把任务详情映射进迁移进度区。
  function applyMigrateProgress(task) {
    const m = state.data.mig;
    m.summary = (task.progress && task.progress.text) || '';
    if (task.progress && Array.isArray(task.progress.detail)) m.progress = task.progress.detail;
  }

  function finishMigrate(task) {
    const m = state.data.mig;
    m.running = false;
    applyMigrateProgress(task);
    if (task.status === 'done' || task.status === 'cancelled') {
      m.errText = '';
      toast(task.status === 'done'
        ? '数据迁移完成（' + fmtTaskCost(task) + '）。可在目标库侧核验数据'
        : '数据迁移已取消：已写入数据保留，重跑即幂等续接', task.status === 'done' ? 'success' : 'info');
    } else {
      m.errText = '存在失败表';
      toast('数据迁移结束但有失败表：' + (task.result || '') + '。详见进度区各行失败原因', 'error');
    }
    render();
  }

  async function startMigrate() {
    const m = state.data.mig;
    if (m.running) return;
    m.target = (document.getElementById('db-mig-target') || {}).value || m.target;
    m.source = (document.getElementById('db-mig-source') || {}).value || 'self';
    m.mode = (document.getElementById('db-mig-mode') || {}).value || 'replace';
    m.batch = Number((document.getElementById('db-mig-batch') || {}).value) || 1000;
    const tables = migrateTables().filter(name => m.selected[name]);
    if (!tables.length) { toast('请至少勾选一张要迁移的表', 'warning'); return; }
    if (!m.target) { toast('请先在数据源卡添加目标库', 'warning'); return; }
    const ok = await confirmDialog({
      title: '启动数据迁移',
      message: '将把 <b>' + tables.length + '</b> 张表从 ' + (m.source === 'self' ? '本机运行库' : '所选数据源') +
        ' 迁移到目标数据源（冲突策略：' + (m.mode === 'replace' ? '清空重灌' : '跳过冲突') + '）。' +
        '<b>目标表将被清空重灌或按策略跳过冲突</b>，确定继续吗？',
      confirmText: '启动迁移', danger: true,
    });
    if (!ok) return;
    m.running = true; m.errText = ''; m.progress = null;
    render();
    try {
      await api.post('/admin/db/migrate/start')({
        source: m.source, target: m.target, tables: tables, batch: m.batch, mode: m.mode,
      }).then(function (r) {
        m.taskId = r.task_id;
        pollTask(r.task_id, {
          onRunning: function (task) { applyMigrateProgress(task); render(); },
          onDone: finishMigrate,
          onFailed: finishMigrate,
        });
      });
    } catch (e) {
      m.running = false;
      // 互斥拒绝（有任务进行中）与参数错误在此统一报出
      toast('启动迁移失败：' + e.message, 'error');
      render();
    }
  }

  function cancelMigrate() {
    const m = state.data.mig;
    if (!m.taskId) return;
    api.post('/admin/tasks/' + encodeURIComponent(m.taskId) + '/cancel')({})
      .then(function (r) { toast(r.message || '取消请求已送达：当前批次完成后停止', 'info'); })
      .catch(function (e) { toast('取消失败：' + e.message, 'error'); });
  }

  function cancelMigrateTable(el) {
    const table = el.getAttribute('data-table') || '';
    api.post('/admin/db/migrate/cancel')({ table: table })
      .then(function (r) { toast(r.message || ('表 ' + table + ' 已移除'), 'info'); render(); })
      .catch(function (e) { toast('移除失败：' + e.message, 'error'); });
  }

  // ── 执行历史页签：sql_exec_log 审计记录分页展示 ────────────────────────┘

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
      state.hist.failed = false;
      state.hist.err = '';
    } catch (e) {
      state.hist.failed = true;
      state.hist.err = e.message || '未知错误';
      if (e.status !== 0) {
        toast('执行历史查询失败：' + state.hist.err + '。请确认数据连接正常后点「⟳ 刷新」重试', 'error');
      }
    }
    state.hist.loading = false;
    render();
  }

  function histHTML() {
    const h = state.hist;
    // 三态区分（加载失败 ≠ 没有记录，审计场景不得误导）：失败且无数据 → 错误卡 + 重试出口；
    // 失败但有旧页数据 → 展示旧数据并标注本次刷新失败；成功无记录 → 空态文案。
    if (h.failed && !h.items.length) {
      return '<div class="card"><div class="card-title">SQL 执行历史</div>' +
        Rock.comp.empty.message({
          text: '执行历史加载失败' + (h.err ? '：' + h.err : '') +
            '。这不代表没有执行记录，请确认数据库连接正常后重试',
          br: true,
          action: '<button class="btn btn-sm btn-primary" data-act="db-hist-refresh">重试</button>',
        }) + '</div>';
    }
    let html = '<div class="card"><div class="card-title">SQL 执行历史' +
      '<span class="tag tag-gray">每条语句一行 · 完整留痕</span>' +
      '<span class="comp-actions"><button class="btn btn-sm" data-act="db-hist-refresh"' +
      (h.loading ? ' disabled' : '') + '>⟳ 刷新</button></span></div>' +
      (h.failed ? '<div class="form-hint is-error-text">本次刷新失败（' +
        esc(h.err) + '），以下为上次结果</div>' : '') +
      '<div class="form-hint">记录「执行SQL」的每条语句：时间、批次、原文、结果与耗时，永久保留，可审计追溯。</div>' +
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
    if (state.executing) return; // 防抖：上一批 DDL 未返回前拒绝重复下发（不可回滚，二次下发是事故）
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
    // 确认后到响应返回之间按钮必须保持禁用（防连点二次下发）
    state.executing = true;
    render();
    const background = state.execBackground;
    if (background) {
      // 后台执行：提交任务即返回，轮询取逐条结果（长语句摆脱 HTTP 超时）。
      try {
        const r = await api.post('/admin/db/exec')({ sql: sql, background: true });
        pollTask(r.task_id, {
          onRunning: function () {},
          onDone: function (task) {
            state.executing = false;
            const detail = (task.progress && task.progress.detail) || [];
            state.exec = {
              results: detail.map(x => ({ idx: x.seq, sql: x.sql, ok: !!x.ok, rows: x.rows, error: x.error || '' })),
              executed: detail.filter(x => x.ok).length,
              failed: detail.filter(x => !x.ok).length,
            };
            if (state.exec.failed > 0) {
              toast('后台 SQL 执行部分失败：' + (task.result || '') + '。详见下方结果', 'error');
            } else {
              toast('后台 SQL 执行完成（' + fmtTaskCost(task) + '）：全部 ' + state.exec.executed + ' 条成功', 'success');
            }
            state.hist.loaded = false;
            state.size.loaded = false;
            render();
          },
          onFailed: function (task) {
            state.executing = false;
            toast('后台 SQL 执行失败：' + (task.result || '未知错误'), 'error');
            render();
          },
        });
      } catch (e) {
        state.executing = false;
        toast('提交后台执行任务失败：' + e.message + '。若提示任务进行中，请稍候再试', 'error');
        render();
      }
      return;
    }
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
      state.executing = false;
      render();
      return;
    }
    state.executing = false;
    state.hist.loaded = false;  // 执行后失效缓存：下次进执行历史页签重拉（含本次留痕）
    state.size.loaded = false; // 表结构可能已变（建表/加列）：空间统计一并失效
    render();
  }

  // 行详情：data-key = id 回查当前页行后弹详情（完整语句 + 执行快照）
  function openHistDetail(el) {
    const key = el.getAttribute('data-key') || '';
    const row = state.hist.items.find(r => String(r.id) === key);
    if (row) histTable.onDetail(row);
  }

  // 复制编辑器 SQL 到剪贴板
  async function copySQL() {    const sql = codeEditor.value(EDITOR_ID);
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
        else if (tab === 'overview' && (!state.size.loaded || state.size.failed)) loadSize();
        else if (tab === 'data') enterDataTab();
        else render();
      },
      'db-check': function () { check(); },
      'db-exec': function () { execSQL(); },
      'db-exec-bg': function (el) { state.execBackground = !!el.checked; },
      'db-copy-sql': function () { copySQL(); },
      'db-size-refresh': function () { loadSize(true); },
      'db-table-size': function (el) { calcTableSize(el.getAttribute('data-table') || ''); },
      'db-geoip-sync': function () { runGeoSync(); },
      'db-dsn-reload': function () { loadDsn(true); },
      'db-dsn-add': function () { addDsn(); },
      'db-dsn-test': function () { testDsn(); },
      'db-dsn-del': function (el) { delDsn(el); },
      'db-align-check': function () { alignCheck(); },
      'db-align-apply': function () { alignApply(); },
      'db-mig-start': function () { startMigrate(); },
      'db-mig-cancel': function () { cancelMigrate(); },
      'db-mig-cancel-table': function (el) { cancelMigrateTable(el); },
      'db-mig-table': function (el) {
        state.data.mig.selected[el.getAttribute('data-table') || ''] = !!el.checked;
      },
      'db-hist-refresh': function () { state.hist.offset = 0; loadHist({ force: true }); },
      'db-hist-prev': function () {
        state.hist.offset = Math.max(0, state.hist.offset - HIST_PAGE_SIZE);
        loadHist({ move: true });
      },
      'db-hist-next': function () {
        state.hist.offset += HIST_PAGE_SIZE;
        loadHist({ move: true });
      },
      'db-hist-detail': function (el) { openHistDetail(el); },
    },
  };
})();
