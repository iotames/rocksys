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
  const debounce = Rock.util.debounce;
  const fmtDate = Rock.util.fmtDate;
  const fmtDateTime = Rock.util.fmtDateTime;
  const fmtBytes = Rock.util.fmtBytes;
  const truncate = Rock.util.truncate;
  const store = Rock.state.store;
  const api = Rock.api;
  const toast = Rock.ui.toast;
  const skeletonHTML = Rock.ui.skeletonHTML;
  const noteUpdated = Rock.ui.noteUpdated;

  // 查询条件（提交后端） / 本地筛选条件 / 展开状态（页内私有）
  const logsQuery = { fromDate: '', fromTime: '', toDate: '', toTime: '', path: '', pathLike: '' };
  const logsFilter = { status: '', onlyError: false, sortBy: 'time_desc' };
  let logsExpanded = {};

  function today() { return fmtDate(new Date()); }
  // 组装后端时间参数（精确到分）
  function qFrom() { return (logsQuery.fromDate || today()) + 'T' + (logsQuery.fromTime || '00:00'); }
  function qTo() { return (logsQuery.toDate || today()) + 'T' + (logsQuery.toTime || '23:59'); }

  function statusGroup(code) {
    code = Number(code) || 0;
    if (code >= 500) return '5xx';
    if (code >= 400) return '4xx';
    if (code >= 300) return '3xx';
    if (code >= 200) return '2xx';
    return '';
  }

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

  // NDJSON 按行解析（坏行容错跳过）
  function parseNdjson(txt) {
    const out = [];
    const lines = String(txt || '').split('\n');
    for (let i = 0; i < lines.length; i++) {
      const t = lines[i].trim();
      if (!t) continue;
      try {
        out.push(normalizeLogRow(JSON.parse(t)));
      } catch (e) {
        // 容错：跳过无法解析的行
      }
    }
    return out;
  }

  // 加载日志（默认当天全天；首次进入且页面为空时展示骨架屏）
  async function loadPage(opts) {
    const host = $('#page-logs');
    if (!store.logsLoaded && host && !host.innerHTML.trim()) {
      host.innerHTML = skeletonHTML(5);
    }
    const params = new URLSearchParams();
    params.set('from', qFrom());
    params.set('to', qTo());
    if (logsQuery.path) params.set('path', logsQuery.path);
    if (logsQuery.pathLike) params.set('path_like', logsQuery.pathLike);
    loadStorage(); // 存储占用（不阻塞日志主流程）
    try {
      const txt = await api.text('/admin/logs?' + params.toString());
      store.logs = parseNdjson(txt);
      store.logsLoaded = true;
      store.logsError = null;
      logsExpanded = {};
      noteUpdated();
    } catch (e) {
      store.logsLoaded = true;
      store.logs = [];
      logsExpanded = {};
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
    return [
      '文件日志 <b>' + fmtBytes(Number(s.file_bytes) || 0) + '</b>',
      '数据库表 <b>' + fmtBytes(Number(s.db_bytes) || 0) + '</b>',
      '总计 <b>' + fmtBytes(Number(s.total_bytes) || 0) + '</b>',
    ].join(' · ');
  }

  function renderStorage() {
    const el = $('#log-storage');
    if (el) el.innerHTML = storageHTML();
  }

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

  // 按当前筛选条件过滤 + 排序（path 与时间已由后端查询过滤；耗时排序为本地排序）
  function filteredLogs() {
    let rows = store.logs || [];
    if (logsFilter.status) {
      rows = rows.filter(r => statusGroup(r.status_code) === logsFilter.status);
    }
    if (logsFilter.onlyError) {
      rows = rows.filter(r => Number(r.status_code) >= 400);
    }
    rows = rows.slice();
    if (logsFilter.sortBy === 'total_desc') {
      rows.sort((a, b) => (b.total_ms || 0) - (a.total_ms || 0));
    } else if (logsFilter.sortBy === 'total_asc') {
      rows.sort((a, b) => (a.total_ms || 0) - (b.total_ms || 0));
    }
    // time_desc（默认）：后端已按最新在前返回
    return rows;
  }

  // 详情：核心字段 + 扩展维度（extra 平铺字段，非核心字段自动列出）
  const KNOWN = new Set(['time', 'trace_id', 'tenant_id', 'path', 'method', 'client_ip', 'status_code', 'upstream', 'shield_ms', 'biz_ms', 'total_ms', 'req_bytes', 'resp_bytes']);

  function logDetailHTML(r) {
    const core = [
      ['请求标识', r.trace_id],
      ['租户', r.tenant_id],
      ['请求来源', r.client_ip],
      ['方法', r.method],
      ['路径', r.path],
      ['状态码', r.status_code],
      ['防护耗时', r.shield_ms + ' ms'],
      ['业务/转发耗时', r.biz_ms + ' ms'],
      ['总耗时', r.total_ms + ' ms'],
      ['转发目标', r.upstream],
      ['请求流量', fmtBytes(r.req_bytes)],
      ['响应流量', fmtBytes(r.resp_bytes)],
    ];
    // 扩展维度（后端平铺返回的负载字段，如 request_body）
    const extras = [];
    const flat = r.extras || {};
    Object.keys(flat).forEach(k => {
      if (!KNOWN.has(k)) {
        let v = flat[k];
        if (typeof v === 'object' && v !== null) v = JSON.stringify(v);
        extras.push([k, String(v)]);
      }
    });
    const items = core.concat(extras);
    return '<div class="detail-grid">' + items.map(it =>
      '<div class="detail-item"><span class="k">' + esc(it[0]) + '：</span><span class="v">' + esc(it[1] === '' ? '—' : it[1]) + '</span></div>'
    ).join('') + '</div>';
  }

  // 展开状态以行标识（time|trace_id）为键：排序/过滤变化后展开状态跟随行，不错位
  function expKey(r) { return (r.time || '') + '|' + (r.trace_id || ''); }

  function logRowHTML(r, idx) {
    const expanded = !!logsExpanded[expKey(r)];
    const st = r.status_code;
    const stCls = st >= 500 ? 'status-red' : (st >= 400 ? 'status-warn' : (st >= 300 ? 'status-info' : (st >= 200 ? 'status-ok' : '')));
    const methodCls = 'method method-' + (r.method || '').toLowerCase();
    return '<tr class="log-row' + (st >= 400 ? ' is-error' : '') + '" data-act="log-expand" data-idx="' + esc(expKey(r)) + '">' +
      '<td class="mono" title="' + esc(r.time) + '">' + esc(fmtDateTime(r.time)) + '</td>' +
      '<td><span class="' + methodCls + '">' + esc(r.method) + '</span></td>' +
      '<td class="log-path" title="' + esc(r.path) + '">' + esc(truncate(r.path, 60)) + '</td>' +
      '<td><span class="status ' + stCls + '">' + (st || '-') + '</span></td>' +
      '<td class="mono">' + r.total_ms + 'ms</td>' +
      '<td class="row-arrow">' + (expanded ? '▾' : '▸') + '</td>' +
      '</tr>' +
      (expanded ? '<tr class="log-detail-row"><td colspan="6">' + logDetailHTML(r) + '</td></tr>' : '');
  }

  function renderTable() {
    const wrap = $('#log-table-wrap');
    if (!wrap) return;
    const rows = filteredLogs();
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
    if (!rows.length) {
      wrap.innerHTML = '<div class="card">' + Rock.comp.empty.message({ text: '没有符合筛选条件的日志' }) + '</div>';
      return;
    }
    const shown = rows.slice(0, 2000);
    const html = '<div class="table-wrap" style="max-height:640px">' +
      '<table class="table"><thead><tr>' +
      '<th>时间</th><th>方法</th><th>路径</th><th>状态</th><th>耗时</th><th style="width:28px"></th>' +
      '</tr></thead><tbody>' + shown.map((r, i) => logRowHTML(r, i)).join('') + '</tbody></table></div>' +
      (rows.length >= 2000 || (store.logs || []).length >= 2000 ? '<div class="form-hint" style="margin-top:8px">已达 2000 条展示上限，请收窄时间范围或筛选条件。</div>' : '');
    wrap.innerHTML = html;
  }

  function render() {
    const host = $('#page-logs');
    if (!host) return;
    if (!logsQuery.fromDate) logsQuery.fromDate = today();
    if (!logsQuery.fromTime) logsQuery.fromTime = '00:00';
    if (!logsQuery.toDate) logsQuery.toDate = today();
    if (!logsQuery.toTime) logsQuery.toTime = '23:59';
    host.innerHTML =
      Rock.comp.head.headHTML({
        title: '访问日志',
        desc: '按时间范围与路径查看 HTTP 数据请求日志，定位单个请求（与系统运行日志分开）',
        actions: '<button class="btn btn-sm" data-act="logs-reload">⟳ 手动刷新</button>',
      }) +
      '<div class="card storage-card"><span class="storage-label">存储占用：</span><span id="log-storage">' + storageHTML() + '</span></div>' +
      '<div class="card">' +
      '<div class="log-toolbar">' +
      '<div class="tool-group"><span class="muted">开始</span>' +
      '<input type="date" class="input input-sm" id="log-from-date" value="' + esc(logsQuery.fromDate) + '">' +
      '<input type="time" class="input input-sm" id="log-from-time" value="' + esc(logsQuery.fromTime) + '">' +
      '</div>' +
      '<div class="tool-group"><span class="muted">结束</span>' +
      '<input type="date" class="input input-sm" id="log-to-date" value="' + esc(logsQuery.toDate) + '">' +
      '<input type="time" class="input input-sm" id="log-to-time" value="' + esc(logsQuery.toTime) + '">' +
      '</div>' +
      '<button class="btn btn-sm btn-primary" data-act="log-query">查询</button>' +
      '<button class="btn btn-sm" data-act="log-export">导出下载</button>' +
      '<span class="toolbar-divider"></span>' +
      '<input class="input input-sm log-path" id="log-path" placeholder="路径精确匹配，如 /api/order/1" title="path 精确匹配" value="' + esc(logsQuery.path) + '">' +
      '<input class="input input-sm log-path" id="log-path-like" placeholder="路径模糊匹配，如 /api/order" title="path 模糊搜索（包含）" value="' + esc(logsQuery.pathLike) + '">' +
      '<button class="btn btn-sm btn-text" data-act="log-reset">重置</button>' +
      '</div>' +
      '<div class="log-toolbar log-toolbar-filters">' +
      '<select class="select select-sm" id="log-status">' + Rock.comp.select.options(STATUS_OPTIONS, logsFilter.status) + '</select>' +
      '<select class="select select-sm" id="log-sort">' + Rock.comp.select.options(SORT_OPTIONS, logsFilter.sortBy) + '</select>' +
      '<label class="chk"><input type="checkbox" id="log-only-error"' + (logsFilter.onlyError ? ' checked' : '') + '><span>只看异常（≥4xx）</span></label>' +
      '</div>' +
      '<div id="log-table-wrap"></div>' +
      '</div>';
    renderTable();

    // 开始/结束日期时间：变更即查询（防抖）
    const fromDate = $('#log-from-date');
    const fromTime = $('#log-from-time');
    const toDate = $('#log-to-date');
    const toTime = $('#log-to-time');
    const syncTime = debounce(() => {
      logsQuery.fromDate = fromDate.value;
      logsQuery.fromTime = fromTime.value;
      logsQuery.toDate = toDate.value;
      logsQuery.toTime = toTime.value;
      query();
    }, 300);
    [fromDate, fromTime, toDate, toTime].forEach(el => el.addEventListener('change', syncTime));

    // path 精确/模糊：即时（防抖）后端查询
    const pathInput = $('#log-path');
    pathInput.addEventListener('input', debounce(() => {
      logsQuery.path = pathInput.value.trim();
      query();
    }, 300));
    const pathLikeInput = $('#log-path-like');
    pathLikeInput.addEventListener('input', debounce(() => {
      logsQuery.pathLike = pathLikeInput.value.trim();
      query();
    }, 300));

    // 状态码 / 耗时排序 / 只看异常：本地筛选
    const stSel = $('#log-status');
    stSel.addEventListener('change', () => {
      logsFilter.status = stSel.value;
      renderTable();
    });
    const sortSel = $('#log-sort');
    sortSel.addEventListener('change', () => {
      logsFilter.sortBy = sortSel.value;
      renderTable();
    });
    const onlyErr = $('#log-only-error');
    onlyErr.addEventListener('change', () => {
      logsFilter.onlyError = onlyErr.checked;
      renderTable();
    });
  }

  // 按时间范围 + path 条件查询（读取工具栏输入）
  async function query() {
    logsQuery.fromDate = $('#log-from-date').value || today();
    logsQuery.fromTime = $('#log-from-time').value || '00:00';
    logsQuery.toDate = $('#log-to-date').value || today();
    logsQuery.toTime = $('#log-to-time').value || '23:59';
    if (qFrom() > qTo()) {
      toast('开始时间不能晚于结束时间', 'error');
      return;
    }
    store.logsLoaded = false;
    render();
    await loadPage({ force: true });
  }

  // 导出当前筛选结果为 JSONL 文本下载
  function exportLogs() {
    const rows = filteredLogs();
    if (!rows.length) { toast('没有可导出的日志', 'warning'); return; }
    const lines = rows.map(r => JSON.stringify(r.extras));
    const blob = new Blob([lines.join('\n')], { type: 'application/x-ndjson;charset=utf-8' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'access-' + qFrom().replace(/[:T]/g, '-') + '_' + qTo().replace(/[:T]/g, '-') + '.jsonl';
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(a.href);
    toast('已导出 ' + rows.length + ' 条日志', 'success');
  }

  // 重置筛选与查询条件（时间回当天全天）
  function resetFilter() {
    logsFilter.status = '';
    logsFilter.onlyError = false;
    logsFilter.sortBy = 'time_desc';
    logsQuery.fromDate = today();
    logsQuery.fromTime = '00:00';
    logsQuery.toDate = today();
    logsQuery.toTime = '23:59';
    logsQuery.path = '';
    logsQuery.pathLike = '';
    store.logsLoaded = false;
    render();
    loadPage({ force: true });
  }

  // 行展开 / 收起（key = time|trace_id，跟随行不错位）
  function toggleExpand(key) {
    if (!key) return;
    logsExpanded[key] = !logsExpanded[key];
    renderTable();
  }

  window.Rock.views.logs = {
    loadPage,
    loadStorage,
    render,
    renderTable,
    query,
    exportLogs,
    resetFilter,
    toggleExpand,
    parseNdjson,
    statusGroup,
  };
})();
