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

  // 任务轮询与耗时文案复用全局共用组件（Rock.ui.pollTask / findRunningTask / fmtTaskCost，
  // 同一实现亦服务概览页的 GeoIP「立即同步」，避免两处各写一套轮询）。
  const pollTask = Rock.ui.pollTask;
  const findRunningTask = Rock.ui.findRunningTask;
  const fmtTaskCost = Rock.ui.fmtTaskCost;

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
        : '<div class="muted" style="margin-bottom:8px">暂无外部数据源。添加目标库（如开发用 SQLite → 生产 MySQL）后即可做结构对齐与数据迁移。</div>';
    }
    return '<div class="card"><div class="card-title">数据源' +
      '<span class="tag tag-blue">迁移目标与备选源</span>' +
      '<span class="comp-actions"><button class="btn btn-sm" data-act="db-dsn-reload"' + (d.loading ? ' disabled' : '') + '>⟳</button></span></div>' +
      body +
      '<div class="comp-actions" style="margin-top:12px;flex-wrap:wrap;gap:8px">' +
      '<input id="db-dsn-name" placeholder="连接名（唯一，如 prod-mysql）" style="width:200px">' +
      '<select id="db-dsn-driver"><option value="sqlite">sqlite</option><option value="mysql">mysql</option><option value="postgres">postgres</option></select>' +
      '<input id="db-dsn-dsn" placeholder="连接串 DSN（含凭据，服务端脱敏存储展示）" style="width:340px" class="mono">' +
      '<button class="btn btn-sm" data-act="db-dsn-test"' + (d.testing ? ' disabled' : '') + '>' + (d.testing ? '测试中…' : '连通测试') + '</button>' +
      '<button class="btn btn-sm btn-primary" data-act="db-dsn-add">添加</button>' +
      '</div>' +
      '<div class="form-hint">连通测试不落盘，可先测试未保存的连接串；MySQL 密码含 @ 会被拦截（需先 URL 编码）。</div>' +
      (d.testMsg ? '<div class="form-hint" style="margin-top:4px">' + esc(d.testMsg) + '</div>' : '') +
      '</div>';
  }

  // 读取添加表单输入（按 id 取值，不做双向绑定——保持页面既有轻量风格）。
  function dsnFormValues() {
    return {
      name: (document.getElementById('db-dsn-name') || {}).value || '',
      driver: (document.getElementById('db-dsn-driver') || {}).value || '',
      dsn: ((document.getElementById('db-dsn-dsn') || {}).value || '').trim(),
    };
  }

  async function addDsn() {
    const f = dsnFormValues();
    if (!f.name.trim() || !f.dsn) {
      toast('连接名与 DSN 均不能为空：请补全后重试', 'warning');
      return;
    }
    try {
      await api.post('/admin/db/dsn')({ name: f.name.trim(), driver: f.driver, dsn: f.dsn });
      toast('数据源「' + f.name.trim() + '」已添加并持久化（重启保留）', 'success');
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
    const opts = state.data.dsn.items.map(function (it) {
      return '<option value="' + esc(it.code) + '"' + (a.code === it.code ? ' selected' : '') + '>' +
        esc(it.name) + '（' + esc(it.driver) + '）</option>';
    }).join('');
    let body = '<div class="comp-actions" style="margin-bottom:8px">' +
      '<select id="db-align-target"' + (state.data.dsn.items.length ? '' : ' disabled') + '>' + (opts || '<option value="">请先在上方添加数据源</option>') + '</select>' +
      '<button class="btn btn-sm btn-primary" data-act="db-align-check"' + (!a.code || a.checking ? ' disabled' : '') + '>' +
      (a.checking ? '检查中…' : '检查差异') + '</button>' +
      (a.checked ? '<button class="btn btn-sm btn-danger" data-act="db-align-apply"' + (a.applying ? ' disabled' : '') + '>' +
        (a.applying ? '对齐中…' : '执行对齐') + '</button>' : '') +
      '</div>';
    if (a.checked) {
      body += !a.items.length
        ? '<div class="alert alert-info">目标库结构与期望一致，无需对齐。</div>'
        : '<div class="form-hint" style="margin-bottom:6px">发现 ' + a.items.length + ' 处差异，将对目标库逐条执行以下 DDL（遇错即停；不写本机审计表）：</div>' +
          '<pre class="mono" style="max-height:200px;overflow:auto;background:var(--bg,#f7f7f7);padding:8px;border-radius:6px">' + esc(a.sql) + '</pre>';
    }
    return '<div class="card"><div class="card-title">表结构对齐' +
      '<span class="tag tag-blue">迁移前置</span></div>' +
      '<div class="form-hint" style="margin-bottom:8px">目标 = 外部数据源；期望结构 = 本系统内嵌 SQL 脚本（与「表同步」同源）。' +
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
    const srcOpts = '<option value="self"' + (m.source === 'self' ? ' selected' : '') + '>本机运行库</option>' +
      dsns.map(it => '<option value="' + esc(it.code) + '"' + (m.source === it.code ? ' selected' : '') + '>' + esc(it.name) + '</option>').join('');
    const tgtOpts = dsns.map(it => '<option value="' + esc(it.code) + '"' + (m.target === it.code ? ' selected' : '') + '>' + esc(it.name) + '（' + esc(it.driver) + '）</option>').join('');
    const tables = migrateTables();
    const tableList = tables.length
      ? tables.map(function (name) {
          const checked = !!m.selected[name];
          return '<label style="margin-right:12px;white-space:nowrap"><input type="checkbox" data-act="db-mig-table" data-table="' + esc(name) + '"' +
            (checked ? ' checked' : '') + '> <span class="mono">' + esc(name) + '</span></label>';
        }).join('')
      : '<span class="muted">表清单不可用：请先到「表概览」页签加载空间统计（表清单取自运行库业务表）。</span>';
    let body =
      '<div class="comp-actions" style="flex-wrap:wrap;gap:8px;margin-bottom:8px">' +
      '<label>源 <select id="db-mig-source">' + srcOpts + '</select></label>' +
      '<label>目标 <select id="db-mig-target"' + (dsns.length ? '' : ' disabled') + '>' + (tgtOpts || '<option value="">请先添加数据源</option>') + '</select></label>' +
      '<label>冲突策略 <select id="db-mig-mode"><option value="replace"' + (m.mode === 'replace' ? ' selected' : '') + '>清空重灌</option>' +
      '<option value="skip"' + (m.mode === 'skip' ? ' selected' : '') + '>跳过冲突</option></select></label>' +
      '<label>批次 <input id="db-mig-batch" type="number" value="' + esc(String(m.batch)) + '" min="100" max="10000" style="width:90px"></label>' +
      '</div>' +
      '<div class="form-hint" style="margin-bottom:8px">批次为性能参考值：批越大吞吐越高、内存与目标库单语句负载越高；' +
      '超出目标库参数上限时服务端自动切分执行，无需手工计算。批次仅本次会话生效，刷新复位 1000。</div>' +
      '<div style="margin-bottom:8px"><b>迁移表：</b><div style="margin-top:4px;display:flex;flex-wrap:wrap">' + tableList + '</div></div>' +
      '<button class="btn btn-danger" data-act="db-mig-start"' + (m.running ? ' disabled' : '') + '>' +
      (m.running ? '迁移进行中…' : '启动迁移') + '</button>' +
      (m.running ? ' <button class="btn btn-sm" data-act="db-mig-cancel">取消整个任务（当前批完成后停止）</button>' : '');
    if (m.summary || m.errText) {
      body += '<div class="form-hint" style="margin-top:8px">' +
        (m.errText ? '<span style="color:var(--danger,#c0392b)">' + esc(m.errText) + '</span>　' : '') +
        esc(m.summary) + '</div>';
    }
    if (m.progress && m.progress.length) {
      body += '<table class="table" style="margin-top:8px"><thead><tr><th>表</th><th>状态</th><th>进度</th><th>说明</th><th>操作</th></tr></thead><tbody>' +
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
      '<div class="form-hint" style="margin-bottom:8px">同名字段跨方言直迁（先完成表结构对齐）；目标不可选本机运行库（防误覆盖生产数据）。' +
      '单表失败不阻塞后续表；同一时刻全局仅一个长任务运行。</div>' + body + '</div>';
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
      (h.failed ? '<div class="form-hint" style="color:var(--danger,#c0392b)">本次刷新失败（' +
        esc(h.err) + '），以下为上次结果</div>' : '') +
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
