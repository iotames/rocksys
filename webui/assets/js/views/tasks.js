/* ==========================================================================
 * RockSys 管理控制台 - views/tasks.js 后台任务页
 * 数据源 GET /admin/tasks（任务中心：运行中 + 保留期内终态，终态仅留最近 500 条流水）。
 * 展示：任务列表（标题/来源/状态/进度/结果/创建时间/结束时间与耗时/操作；ID 不列表展示，
 *       行点击详情内可见）+
 *       并发管控卡（内存态配置原样透出）：来源白名单 / 互斥键字段 / 互斥公共集 / 指定互斥集。
 * 交互：来源/状态筛选下拉（本地过滤，选项即白名单/固定枚举）；轮询/加载走服务端轻量分页——
 *       默认只拉最近 20 条终态（+全部运行中），「显示更早记录」才拉全量（终态保留 500 条）；
 *       运行中任务可取消
 *       （确认弹窗 → 统一取消端点）；存在运行中任务时每 3 秒静默轮询，全部终态后自动
 *       停止（不无限刷），离开页面时由路由清理钩子停表。
 * UX 红线：load 透传 refreshPage 的 opts（含 silent）；非引导态加载失败弹统一 error toast。
 * 挂载到全局命名空间 window.Rock.views.tasks。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const notify = Rock.ui.notify;
  const api = Rock.api;
  const confirmDialog = Rock.ui.confirmDialog;
  const fmtTaskCost = Rock.ui.fmtTaskCost;

  let rows = [];
  let allowCreators = [];
  let mutexTaskField = '';
  let mutexList = [];
  let mutexMap = {};
  let filterCreator = '';
  let filterStatus = '';
  let showAll = false; // 默认只拉最近 TASK_PAGE_SIZE 条终态，点「显示更早记录」才拉全量
  let hasMore = false; // 服务端提示终态数超出当前 limit（还有更早记录）
  let loaded = false;
  let loading = false;
  let errText = '';
  let pollTimer = null;

  function fmtTime(v) {
    if (!v) return '—';
    return esc(String(v).replace('T', ' ').slice(0, 19));
  }

  function statusTag(st) {
    const map = {
      running: ['tag-blue', '运行中'],
      done: ['tag-green', '已完成'],
      failed: ['tag-red', '失败'],
      cancelled: ['tag-gray', '已取消'],
    };
    const it = map[st] || ['tag', st || '—'];
    return '<span class="tag ' + it[0] + '">' + esc(it[1]) + '</span>';
  }

  function tags(list, cls) {
    if (!list || !list.length) return '<span class="form-hint">空（未配置规则，除同来源外不互斥）</span>';
    return list.map(function (v) { return '<span class="tag ' + (cls || '') + '">' + esc(v) + '</span>'; }).join(' ');
  }

  function mutexMapText(m) {
    const keys = Object.keys(m || {}).filter(function (k) { return m[k] && m[k].length; });
    if (!keys.length) return '<span class="form-hint">空（未配置点对点互斥）</span>';
    return keys.map(function (k) {
      return '<code>' + esc(k) + '</code> ⟂ ' + m[k].map(function (v) { return '<code>' + esc(v) + '</code>'; }).join('、');
    }).join('；');
  }

  // 并发管控卡：任务中心内存态配置原样透出（只读展示，规则由装配期注册）。
  function mutexCardHTML() {
    return '<div class="card"><div class="card-title">并发管控<button class="btn btn-sm" data-act="tasks-reload" style="float:right">⟳ 刷新</button></div>' +
      '<div class="form-hint" style="margin-bottom:8px">以下为任务执行中心的内存态配置（重启恢复默认），提交任务时即时比对：满足任一互斥规则即拒绝并提示，未命中规则的任务并行不设限；任务默认无超时，停止仅经人工取消。</div>' +
      '<div>互斥键字段：<code>' + esc(mutexTaskField || '—') + '</code>' +
      '<span class="form-hint">（提交比对时取任务该字段的值作互斥标签，默认=来源）</span></div>' +
      '<div style="margin-top:6px">互斥公共集（集内至多 1 个在跑）：' + tags(mutexList, 'tag-orange') + '</div>' +
      '<div style="margin-top:6px">指定互斥集（双向生效）：' + mutexMapText(mutexMap) + '</div>' +
      '<div style="margin-top:6px">来源白名单（未注册来源拒绝提交）：' + tags(allowCreators) + '</div>' +
      '</div>';
  }

  // 默认展示条数：与服务端 tasksDefaultLimit 对齐——轮询只拉最近 20 条终态（+全部运行中），
  // 高频轮询不搬 500 条死数据；「显示更早记录」才拉全量。
  const TASK_PAGE_SIZE = 20;

  // 状态筛选枚举（与 Status tag 同一套取值）。
  const STATUS_OPTIONS = [
    ['', '全部状态'],
    ['running', '运行中'],
    ['done', '已完成'],
    ['failed', '失败'],
    ['cancelled', '已取消'],
  ];

  function filtersHTML() {
    function opts(list, cur) {
      return list.map(function (o) {
        return '<option value="' + esc(o[0]) + '"' + (o[0] === cur ? ' selected' : '') + '>' + esc(o[1]) + '</option>';
      }).join('');
    }
    return '<span class="form-hint">来源筛选 <select id="tasks-creator-filter" class="select select-sm" style="margin-left:4px">' +
      opts([['', '全部来源']].concat(allowCreators.map(function (c) { return [c, c]; })), filterCreator) +
      '</select></span>' +
      '<span class="form-hint" style="margin-left:12px">状态筛选 <select id="tasks-status-filter" class="select select-sm" style="margin-left:4px">' +
      opts(STATUS_OPTIONS, filterStatus) + '</select></span>';
  }

  function tableHTML() {
    const shown = rows.filter(function (t) {
      if (filterCreator && t.created_by !== filterCreator) return false;
      if (filterStatus && t.status !== filterStatus) return false;
      return true;
    });
    if (!shown.length) {
      return '<div class="card">' + Rock.comp.empty.emptyCard({ text: (filterCreator || filterStatus) ? '当前筛选条件下暂无任务记录' : '暂无任务记录（提交数据迁移/GeoIP 同步/SQL 后台执行后此处可见）' }) + '</div>';
    }
    const visible = shown; // 条数已由服务端 limit 控制（默认 20 条终态），前端不再二次截断
    const head = '<tr><th>标题</th><th>来源</th><th>状态</th><th>进度</th><th>结果 / 耗时</th><th>创建时间</th><th>结束时间</th><th>操作</th></tr>';
    const body = visible.map(function (t) {
      const prog = t.progress && t.progress.text
        ? esc(t.progress.text) + (t.progress.detail ? ' <span class="tag tag-blue">明细</span>' : '')
        : '<span class="form-hint">—</span>';
      // 结果列只承载增量信息：failed 显示错误详情；done/cancelled 的模板文案与状态列重复，只留耗时
      const result = t.status === 'failed' && t.result
        ? '<div style="max-width:320px;white-space:normal"><span class="form-hint">' + esc(t.result) + '</span></div>'
        : '';
      const cost = fmtTaskCost(t);
      const op = t.status === 'running'
        ? '<button class="btn btn-sm btn-danger" data-act="tasks-cancel" data-id="' + esc(t.id) + '">取消</button>'
        : '';
      return '<tr data-act="tasks-detail" data-id="' + esc(t.id) + '" style="cursor:pointer" title="点击查看任务详情（含进度明细）">' +
        '<td>' + esc(t.title || '—') + '</td>' +
        '<td><span class="tag">' + esc(t.created_by || '—') + '</span></td>' +
        '<td>' + statusTag(t.status) + '</td>' +
        '<td>' + prog + '</td>' +
        '<td>' + result + (cost ? '<div class="form-hint">耗时 ' + esc(cost) + '</div>' : '') + '</td>' +
        '<td>' + fmtTime(t.created_at) + '</td>' +
        '<td>' + fmtTime(t.finished_at) + '</td>' +
        '<td>' + op + '</td>' +
        '</tr>';
    }).join('');
    let table = '<div class="card"><div class="table-wrap"><table class="table">' + head + body + '</table></div></div>';
    if (hasMore && !showAll) {
      table += '<div style="text-align:center;margin-top:8px">' +
        '<span class="form-hint">已显示最近 ' + shown.filter(function (t) { return t.status !== 'running'; }).length + ' 条终态记录，</span>' +
        '<button class="btn btn-sm" data-act="tasks-show-earlier">显示更早记录</button></div>';
    }
    return table;
  }

  function render() {
    const host = $('#page-tasks');
    if (!host) return;
    let body;
    if (errText && !rows.length) {
      body = Rock.comp.empty.emptyCard({ text: '后台任务列表加载失败：' + errText, br: true,
        action: '<button class="btn btn-sm btn-primary" data-act="tasks-reload">重试</button>' });
    } else if (!loaded) {
      body = '<div class="empty" style="padding:16px">加载中…</div>';
    } else {
      body = mutexCardHTML() + '<div class="card"><div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">' +
        '<b>任务记录</b>' + filtersHTML() + '</div>' + tableHTML() + '</div>';
    }
    host.innerHTML = '<div class="page-head"><h2>后台任务</h2></div>' +
      '<div class="alert alert-info"><b>口径说明：</b>长任务（数据迁移、GeoIP 数据同步、表结构对齐、SQL 后台执行等）' +
      '统一在此观测：提交即返回任务 ID 并后台执行（默认无超时，跑多久由数据量决定），取消经人工触发在批次边界收工。' +
      '终态记录仅保留最近 500 条（更早的不可再查，服务重启即清空；列表默认展示最近 20 条，可展开查看全部）。</div>' + body;
    bindFilter();
  }

  // 来源/状态筛选：change 委托（重渲染后重挂）；筛选变化时收起展开态，从最近一段重新看。
  function bindFilter() {
    const sel = $('#tasks-creator-filter');
    if (sel) sel.addEventListener('change', function () {
      filterCreator = sel.value;
      showAll = false;
      render();
    });
    const st = $('#tasks-status-filter');
    if (st) st.addEventListener('change', function () {
      filterStatus = st.value;
      showAll = false;
      render();
    });
  }

  // 自动轮询：存在运行中任务时每 3 秒静默刷新，全部终态后停止（防无限刷）。
  function syncPollTimer() {
    const hasRunning = rows.some(function (t) { return t.status === 'running'; });
    if (hasRunning && !pollTimer) {
      pollTimer = setInterval(function () { load({ silent: true }); }, 3000);
    } else if (!hasRunning && pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  // 离开页面清理：停掉残留的列表轮询定时器（经 main.js 路由切换钩子调用）
  function leave() {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  async function load(opts) {
    const o = opts || {};
    if (loading) return;
    loading = true;
    if (!loaded) render(); // 首次进入先渲染骨架
    try {
      // 服务端轻量分页：默认只拉最近 20 条终态（running 始终全带）；展开时 limit=0 拉全量
      const url = showAll ? '/admin/tasks?limit=0' : '/admin/tasks?limit=' + TASK_PAGE_SIZE;
      const res = await api.get(url);
      rows = (res && Array.isArray(res.items)) ? res.items : [];
      hasMore = !!(res && res.has_more);
      allowCreators = (res && Array.isArray(res.allow_creators)) ? res.allow_creators : [];
      mutexTaskField = (res && res.mutex_task_field) || '';
      mutexList = (res && Array.isArray(res.mutex_list)) ? res.mutex_list : [];
      mutexMap = (res && res.mutex_map) || {};
      loaded = true;
      errText = '';
    } catch (e) {
      errText = (e && e.message) || '未知错误';
      // 程序化静默轮询失败不弹 toast（防刷屏，保留旧数据）；手动刷新失败必须弹统一 error toast
      if (!o.silent) {
        notify.error('后台任务列表加载失败：' + errText + '，请确认服务可达后点击「重试」');
      }
    } finally {
      loading = false;
      render();
      syncPollTimer();
    }
  }

  async function cancelTask(el) {
    const id = el.getAttribute('data-id');
    const ok = await confirmDialog({
      title: '取消后台任务',
      message: '确定取消任务 <code>' + esc(id) + '</code> 吗？任务将在当前批次/语句边界收工，已完成部分保留（幂等可续跑）。',
      confirmText: '取消任务',
      danger: true,
    });
    if (!ok) return;
    try {
      const res = await api.post('/admin/tasks/' + encodeURIComponent(id) + '/cancel')({});
      notify.success((res && res.message) ? res.message : '取消请求已送达');
    } catch (e) {
      notify.error('取消失败：' + ((e && e.message) || '未知错误') + '。任务可能已完成或记录已淘汰，请刷新列表确认');
    }
    load({ silent: true });
  }

  // 行详情弹窗：任务元数据 + 进度明细（Detail 为业务自填结构，等宽 JSON 呈现并支持一键复制）。
  // 列表接口对终态任务剥除了进度明细（轮询瘦身），故点击行时经单查取完整快照；单查失败回退列表快照。
  async function showDetail(el) {
    const id = el.getAttribute('data-id');
    let t = rows.find(function (x) { return x.id === id; });
    if (!t) { notify.warn('任务记录不存在（可能已被终态限量淘汰），请刷新列表'); return; }
    try {
      const full = await api.get('/admin/tasks/' + encodeURIComponent(id));
      if (full && full.id) t = full;
    } catch (e) { /* 单查失败（如刚被淘汰）：回退列表快照展示 */ }
    const fields = [
      { key: 'id', label: '任务 ID', render: r => '<span class="mono">' + esc(r.id) + '</span>' },
      { key: 'title', label: '标题' },
      { key: 'created_by', label: '来源', render: r => '<span class="tag">' + esc(r.created_by) + '</span>' },
      { key: 'status', label: '状态', render: r => statusTag(r.status) },
      { key: 'result', label: '结果' },
      { key: 'cost', label: '耗时', render: r => esc(fmtTaskCost(t)) },
      { key: 'created_at', label: '创建时间', render: r => esc(fmtTime(r.created_at)) },
      { key: 'finished_at', label: '结束时间', render: r => esc(fmtTime(r.finished_at)) },
    ];
    if (t.progress && t.progress.text) {
      fields.push({ key: '_ptext', label: '进度', render: () => esc(t.progress.text) });
    }
    if (t.progress && t.progress.detail !== undefined && t.progress.detail !== null) {
      fields.push({
        key: '_pdetail', label: '进度明细', pre: true, copy: true,
        render: function () {
          let d = t.progress.detail;
          if (typeof d !== 'string') {
            try { d = JSON.stringify(d, null, 2); } catch (e) { d = String(d); }
          }
          return '<pre style="max-height:280px;overflow:auto;margin:0;white-space:pre-wrap">' + esc(d) + '</pre>';
        },
      });
    }
    Rock.comp.detailModal.show({ title: '后台任务详情', width: 680, row: Object.assign({}, t, { _ptext: 1, _pdetail: 1 }), fields: fields });
  }

  window.Rock.views.tasks = {
    load: load,
    render: render,
    leave: leave,
    actions: {
      'tasks-reload': function () { load({ force: true }); },
      'tasks-show-earlier': function () { showAll = true; load(); },
      'tasks-cancel': function (el) { cancelTask(el); },
      'tasks-detail': function (el) { showDetail(el); },
    },
  };
})();
