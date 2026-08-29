/* ==========================================================================
 * RockSys 管理控制台 - views/logs.js 日志页
 * 时间范围查询（date + time 组合，精确到分）、path 精确/模糊过滤（后端查询）、
 * 状态码筛选、耗时排序、只看异常、行展开详情、存储占用、导出下载。
 * 挂载到全局命名空间 window.Rock.views.logs。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const fmtDateTime = Rock.util.fmtDateTime;
  const fmtBytes = Rock.util.fmtBytes;
  const truncate = Rock.util.truncate;
  const store = Rock.state.store;
  const api = Rock.api;
  const dateRange = Rock.comp.dateRange;
  const toast = Rock.ui.toast;
  const skeletonHTML = Rock.ui.skeletonHTML;
  const noteUpdated = Rock.ui.noteUpdated;

  const STATUS_OPTIONS = [
    ['', '状态码：全部'],
    ['2xx', '2xx 成功'],
    ['3xx', '3xx 重定向'],
    ['4xx', '4xx 客户端错误'],
    ['5xx', '5xx 服务端错误'],
  ];

  const SORT_OPTIONS = [
    ['time_desc', '时间：最新在前'],
    ['total_desc', '耗时：从高到低'],
    ['total_asc', '耗时：从低到高'],
  ];

  // 双筛选栏（页内私有状态在组件内）：查询条件栏（提交后端）+ 本地筛选排序栏
  const queryBar = Rock.comp.filterBar.create({
    ns: 'logs-query',
    live: true,
    onQuery: function () { query(); },
    fields: [
      { type: 'dateRange', key: '', default: { fromDate: dateRange.today(), fromTime: '00:00', toDate: dateRange.today(), toTime: '23:59' } },
      { type: 'text', key: 'path', placeholder: '路径精确匹配，如 /api/order/1', width: '200px' },
      { type: 'text', key: 'pathLike', placeholder: '路径模糊匹配，如 /api/order', width: '200px' },
    ],
  });
  const filterBar = Rock.comp.filterBar.create({
    ns: 'logs-filter',
    live: true,
    onQuery: function () { logsTable.go(1); query(); }, // 状态分组/排序/仅异常已下沉后端，变更重新查询
    fields: [
      { type: 'select', key: 'status', options: STATUS_OPTIONS },
      { type: 'select', key: 'sortBy', options: SORT_OPTIONS, default: 'time_desc' },
      { type: 'check', key: 'onlyError', label: '只看异常（≥4xx）' },
    ],
  });

  // 明细表（服务端分页：limit/offset/筛选/排序全部由后端执行，总数经 X-Total-Count 回传）
  const logsTable = Rock.comp.dataTable.create({
    ns: 'logs',
    columns: [
      { key: 'time', label: '时间', cls: 'mono', render: r => esc(fmtDateTime(r.time)) },
      { key: 'method', label: '方法', render: r => '<span class="method method-' + esc((r.method || '').toLowerCase()) + '">' + esc(r.method) + '</span>' },
      { key: 'path', label: '路径', render: r => '<span class="log-path" title="' + esc(r.path) + '">' + esc(truncate(r.path, 60)) + '</span>' },
      { key: 'status_code', label: '状态', render: r => { const st = r.status_code; const cls = st >= 500 ? 'status-red' : (st >= 400 ? 'status-warn' : (st >= 300 ? 'status-info' : (st >= 200 ? 'status-ok' : ''))); return '<span class="status ' + cls + '">' + (st || '-') + '</span>'; } },
      { key: 'total_ms', label: '耗时', cls: 'mono', render: r => esc(r.total_ms) + 'ms' },
    ],
    rowClass: r => (Number(r.status_code) >= 400 ? 'is-error' : ''),
    rowKey: r => (r.time || '') + '|' + (r.trace_id || ''),
    detail: { title: () => '访问日志详情', fields: [] }, // fields 由 onDetail 动态给出（含扩展维度）
    paging: { mode: 'server', pageSize: 20 },
    emptyText: '没有符合筛选条件的日志',
    onPaging: function () { loadPage(); }, // 翻页/跳页/改条数：按新 limit/offset 重新拉数
  });
  logsTable.onDetail = function (row) {
    Rock.comp.detailModal.show({ title: '访问日志详情', fields: logDetailFields(row), row: row, width: 640 });
  };

  function normalizeLogRow(r) {
    return {
      time: r.time || '',
      trace_id: r.trace_id || '',
      tenant_id: r.tenant_id || '',
      path: r.path || '',
      method: r.method || '',
      client_ip: r.client_ip || '',
      status_code: Number(r.status_code) || 0,
      upstream: r.upstream || '',
      shield_ms: Number(r.shield_ms) || 0,
      biz_ms: Number(r.biz_ms) || 0,
      total_ms: Number(r.total_ms) || 0,
      req_bytes: Number(r.req_bytes) || 0,
      resp_bytes: Number(r.resp_bytes) || 0,
      extras: r, // 保留扩展维度（负载字段如 request_body），详情展开时平铺展示
    };
  }

  // 组装后端查询参数：时间范围 + path + 状态分组/仅异常/排序（原本地筛选已下沉后端）+ 分页
  function buildLogParams() {
    const q = queryBar.state();
    const f = filterBar.state();
    const st = logsTable.state();
    const params = new URLSearchParams();
    params.set('from', dateRange.from(q));
    params.set('to', dateRange.to(q));
    if (q.path) params.set('path', q.path);
    if (q.pathLike) params.set('path_like', q.pathLike);
    if (f.status) params.set('status_group', f.status[0]); // '4xx' → '4'
    if (f.onlyError) params.set('only_error', '1');
    params.set('sort', f.sortBy || 'time_desc');
    params.set('limit', String(st.pageSize));
    params.set('offset', String(st.offset));
    return params;
  }

  // 加载日志（默认当天全天；首次进入且页面为空时展示骨架屏）
  async function loadPage(opts) {
    const host = $('#page-logs');
    if (!store.logsLoaded && host && !host.innerHTML.trim()) {
      host.innerHTML = skeletonHTML(5);
    }
    const params = buildLogParams();
    loadStorage(); // 存储占用（不阻塞日志主流程）
    try {
      const r = await api.textMeta('/admin/logs?' + params.toString());
      store.logs = Rock.util.parseNdjson(r.text, normalizeLogRow);
      store.logsTotal = r.total;
      store.logsLoaded = true;
      store.logsError = null;
      noteUpdated();
    } catch (e) {
      store.logsLoaded = true;
      store.logs = [];
      store.logsTotal = 0;
      if (e.obsDisabled) {
        store.logsError = 'obs';
      } else if (e.status === 400) {
        store.logsError = 'bad-params';
      } else {
        store.logsError = e.message;
        if (e.status !== 0) toast('日志加载失败：' + e.message, 'error');
      }
    }
    render();
  }

  // 加载存储占用：文件日志 + 数据库日志表总空间（当前存储全量，与启用后端无关）
  async function loadStorage() {
    try {
      const s = await api.get('/admin/logs/storage');
      store.logsStorage = s || null;
      store.storageError = null;
    } catch (e) {
      store.logsStorage = null;
      store.storageError = e.obsDisabled ? 'obs' : e.message;
    }
    renderStorage();
  }

  function storageHTML() {
    if (store.storageError === 'obs') return '<span class="muted">观测组件未开启，无法统计存储占用</span>';
    if (store.storageError) return '<span class="muted">存储占用不可用：' + esc(store.storageError) + '</span>';
    if (!store.logsStorage) return '<span class="muted">存储占用加载中…</span>';
    const s = store.logsStorage;
    return '日志库占用 <b>' + fmtBytes(Number(s.total_bytes) || 0) + '</b>';
  }

  function renderStorage() {
    const el = $('#log-storage');
    if (el) el.innerHTML = storageHTML();
  }

  // 详情字段：核心字段 + 扩展维度（extra 平铺字段，非核心字段自动列出）
  const KNOWN = new Set(['time', 'trace_id', 'tenant_id', 'path', 'method', 'client_ip', 'status_code', 'upstream', 'shield_ms', 'biz_ms', 'total_ms', 'req_bytes', 'resp_bytes']);

  function logDetailFields(r) {
    const core = [
      { key: 'trace_id', label: '请求标识', copy: true },
      { key: 'tenant_id', label: '租户' },
      { key: 'client_ip', label: '请求来源', copy: true },
      { key: 'method', label: '方法' },
      { key: 'path', label: '路径', pre: true, copy: true },
      { key: 'status_code', label: '状态码' },
      { key: 'shield_ms', label: '防护耗时', render: row => esc(row.shield_ms) + ' ms' },
      { key: 'biz_ms', label: '业务/转发耗时', render: row => esc(row.biz_ms) + ' ms' },
      { key: 'total_ms', label: '总耗时', render: row => esc(row.total_ms) + ' ms' },
      { key: 'upstream', label: '转发目标', pre: true },
      { key: 'req_bytes', label: '请求流量', render: row => esc(fmtBytes(row.req_bytes)) },
      { key: 'resp_bytes', label: '响应流量', render: row => esc(fmtBytes(row.resp_bytes)) },
    ];
    // 扩展维度（后端平铺返回的负载字段，如 request_body）
    const flat = r.extras || {};
    Object.keys(flat).forEach(k => {
      if (KNOWN.has(k)) return;
      let v = flat[k];
      if (typeof v === 'object' && v !== null) v = JSON.stringify(v);
      core.push({ key: 'extra_' + k, label: k, render: () => esc(v == null || v === '' ? '—' : String(v)), pre: true, copy: true });
    });
    return core;
  }

  function renderTable() {
    const wrap = $('#log-table-wrap');
    if (!wrap) return;
    if (!store.logsLoaded) {
      wrap.innerHTML = skeletonHTML(4);
      return;
    }
    if (store.logsError === 'obs') {
      wrap.innerHTML = '<div class="card">' + Rock.comp.empty.message({
        text: '观测组件未开启，无法查询日志。',
        action: '<button class="btn btn-sm btn-primary" data-act="go-obs">去组件页开启观测</button>',
      }) + '</div>';
      return;
    }
    if (store.logsError === 'bad-params') {
      wrap.innerHTML = '<div class="card">' + Rock.comp.empty.message({ text: '时间参数不合法，请检查后重试。' }) + '</div>';
      return;
    }
    if (store.logsError) {
      wrap.innerHTML = '<div class="card">' + Rock.comp.empty.message({
        text: '日志加载失败：' + store.logsError,
        action: '<button class="btn btn-sm btn-primary" data-act="logs-reload">重试</button>',
        br: true,
      }) + '</div>';
      return;
    }
    if (!store.logs.length) {
      wrap.innerHTML = '<div class="card">' + Rock.comp.empty.message({ text: '所选时间范围无访问日志' }) + '</div>';
      return;
    }
    wrap.innerHTML = logsTable.html(store.logs, { total: store.logsTotal || 0 });
  }

  function render() {
    const host = $('#page-logs');
    if (!host) return;
    host.innerHTML =
      Rock.comp.head.headHTML({
        title: '入网数据',
        desc: '按时间范围与路径查看 HTTP 入网请求日志，定位单个请求（与系统日志分开）',
        actions: '<button class="btn btn-sm" data-act="logs-reload">⟳ 手动刷新</button>',
      }) +
      '<div class="card storage-card"><span class="storage-label">存储占用：</span><span id="log-storage">' + storageHTML() + '</span></div>' +
      '<div class="card">' +
      queryBar.html() +
      '<div class="log-toolbar" style="margin-top:-6px">' +
      '<button class="btn btn-sm btn-primary" data-act="log-query">查询</button>' +
      '<button class="btn btn-sm" data-act="log-export">导出下载</button>' +
      '<button class="btn btn-sm btn-text" data-act="log-reset">重置</button>' +
      '</div>' +
      '<span class="toolbar-divider"></span>' +
      filterBar.html() +
      '<div id="log-table-wrap"></div>' +
      '</div>';
    renderTable();

    // 筛选栏即改即查（组件内防抖）+ 分页控件委托（host 持久，只绑一次）
    queryBar.bind(host);
    filterBar.bind(host);
    if (!logsTableBound) { logsTable.bind(host); logsTableBound = true; }
  }

  // dataTable 分页控件在持久 host 上只绑一次（render 重渲染 innerHTML 不影响委托）
  let logsTableBound = false;

  // 按时间范围 + path + 状态分组/排序条件查询（条件已在筛选栏状态内，时间非法直接提示）
  async function query() {
    const q = queryBar.state();
    if (dateRange.from(q) > dateRange.to(q)) {
      toast('开始时间不能晚于结束时间', 'error');
      return;
    }
    store.logsLoaded = false;
    render();
    await loadPage({ force: true });
  }

  // 导出当前筛选条件的全量结果为 JSONL 文本下载（单次大 limit 拉取，不受分页影响）
  async function exportLogs() {
    const q = queryBar.state();
    if (dateRange.from(q) > dateRange.to(q)) {
      toast('开始时间不能晚于结束时间', 'error');
      return;
    }
    const params = buildLogParams();
    params.set('limit', '50000');
    params.set('offset', '0');
    let rows;
    try {
      const r = await api.textMeta('/admin/logs?' + params.toString());
      rows = Rock.util.parseNdjson(r.text, normalizeLogRow);
    } catch (e) {
      toast('导出失败：' + e.message, 'error');
      return;
    }
    if (!rows.length) { toast('没有可导出的日志', 'warning'); return; }
    const lines = rows.map(r => JSON.stringify(r.extras));
    const blob = new Blob([lines.join('\n')], { type: 'application/x-ndjson;charset=utf-8' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'access-' + dateRange.from(q).replace(/[:T]/g, '-') + '_' + dateRange.to(q).replace(/[:T]/g, '-') + '.jsonl';
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(a.href);
    toast('已导出 ' + rows.length + ' 条日志', 'success');
  }

  // 重置筛选与查询条件（回默认值：时间当天全天）并重新查询
  function resetFilter() {
    logsTable.go(1);
    filterBar.reset(); // 触发 onQuery → query()
    queryBar.reset();  // 触发 query()（两次触发同一查询，结果一致）
  }

  window.Rock.views.logs = {
    loadPage,
    loadStorage,
    render,
    renderTable,
    query,
    exportLogs,
    resetFilter,
    actions: {
      'logs-reload': function () { query(); },
      'log-query': function () { query(); },
      'log-export': function () { exportLogs(); },
      'log-reset': function () { resetFilter(); },
      'logs-detail': function (el) { openLogDetail(el); },
    },
  };

  // 行详情弹层（data-key = time|trace_id 回查当前页行）
  function openLogDetail(el) {
    const key = el.getAttribute('data-key') || '';
    const row = (store.logs || []).find(r => (r.time || '') + '|' + (r.trace_id || '') === key);
    if (row) logsTable.onDetail(row);
  }
})();
