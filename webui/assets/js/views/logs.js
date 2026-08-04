/* ==========================================================================
 * RockSys 管理控制台 - views/logs.js 日志页
 * 按日期范围查询（NDJSON 按行解析）、trace_id 过滤、状态码筛选、
 * 只看异常、行展开详情、导出下载。挂载到全局命名空间 window.Rock.views.logs。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const debounce = Rock.util.debounce;
  const fmtDate = Rock.util.fmtDate;
  const fmtTime = Rock.util.fmtTime;
  const fmtDateTime = Rock.util.fmtDateTime;
  const fmtBytes = Rock.util.fmtBytes;
  const truncate = Rock.util.truncate;
  const store = Rock.state.store;
  const api = Rock.api;
  const toast = Rock.ui.toast;
  const skeletonHTML = Rock.ui.skeletonHTML;
  const noteUpdated = Rock.ui.noteUpdated;

  // 查询条件 / 筛选条件 / 展开状态（页内私有）
  const logsQuery = { from: '', to: '' };
  const logsFilter = { traceId: '', status: '', onlyError: false };
  let logsExpanded = {};

  function todayStr() {
    return fmtDate(new Date());
  }

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

  // 加载日志（默认当天；首次进入且页面为空时展示骨架屏）
  async function loadPage(opts) {
    const host = $('#page-logs');
    if (!logsQuery.from) logsQuery.from = todayStr();
    if (!logsQuery.to) logsQuery.to = todayStr();
    if (!store.logsLoaded && host && !host.innerHTML.trim()) {
      host.innerHTML = skeletonHTML(5);
    }
    const from = logsQuery.from;
    const to = logsQuery.to;
    try {
      const txt = await api.text('/admin/logs?from=' + encodeURIComponent(from) + '&to=' + encodeURIComponent(to));
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

  // 按当前筛选条件过滤（trace_id / 状态码 / 只看异常）
  function filteredLogs() {
    let rows = store.logs || [];
    if (logsFilter.traceId) {
      const q = logsFilter.traceId;
      rows = rows.filter(r => (r.trace_id || '').indexOf(q) >= 0);
    }
    if (logsFilter.status) {
      rows = rows.filter(r => statusGroup(r.status_code) === logsFilter.status);
    }
    if (logsFilter.onlyError) {
      rows = rows.filter(r => Number(r.status_code) >= 400);
    }
    return rows;
  }

  const STATUS_OPTIONS = [
    ['', '状态码：全部'],
    ['2xx', '2xx 成功'],
    ['3xx', '3xx 重定向'],
    ['4xx', '4xx 客户端错误'],
    ['5xx', '5xx 服务端错误'],
  ];

  function logDetailHTML(r) {
    const items = [
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
    return '<div class="detail-grid">' + items.map(it =>
      '<div class="detail-item"><span class="k">' + esc(it[0]) + '：</span><span class="v">' + esc(it[1] === '' ? '—' : it[1]) + '</span></div>'
    ).join('') + '</div>';
  }

  function logRowHTML(r, idx) {
    const expanded = !!logsExpanded[idx];
    const st = r.status_code;
    const stCls = st >= 500 ? 'status-red' : (st >= 400 ? 'status-warn' : (st >= 300 ? 'status-info' : (st >= 200 ? 'status-ok' : '')));
    const methodCls = 'method method-' + (r.method || '').toLowerCase();
    return '<tr class="log-row' + (st >= 400 ? ' is-error' : '') + '" data-act="log-expand" data-idx="' + idx + '">' +
      '<td class="mono">' + esc(fmtTime(r.time)) + '</td>' +
      '<td><span class="' + methodCls + '">' + esc(r.method) + '</span></td>' +
      '<td class="log-path" title="' + esc(r.path) + '">' + esc(truncate(r.path, 60)) + '</td>' +
      '<td><span class="status ' + stCls + '">' + (st || '-') + '</span></td>' +
      '<td class="mono">' + r.total_ms + 'ms</td>' +
      '<td class="mono" title="' + esc(r.trace_id) + '">' + esc(truncate(r.trace_id, 14)) + '</td>' +
      '<td class="row-arrow">' + (expanded ? '▾' : '▸') + '</td>' +
      '</tr>' +
      (expanded ? '<tr class="log-detail-row"><td colspan="7">' + logDetailHTML(r) + '</td></tr>' : '');
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
      wrap.innerHTML = '<div class="card"><div class="empty">观测组件未开启，无法查询日志。<button class="btn btn-sm btn-primary" data-act="go-obs">去组件页开启观测</button></div></div>';
      return;
    }
    if (store.logsError === 'bad-params') {
      wrap.innerHTML = '<div class="card"><div class="empty">日期参数不合法，请检查后重试。</div></div>';
      return;
    }
    if (store.logsError) {
      wrap.innerHTML = '<div class="card"><div class="empty">日志加载失败：' + esc(store.logsError) +
        '<br><button class="btn btn-sm btn-primary" data-act="logs-reload">重试</button></div></div>';
      return;
    }
    if (!store.logs.length) {
      wrap.innerHTML = '<div class="card"><div class="empty">所选日期无访问日志</div></div>';
      return;
    }
    if (!rows.length) {
      wrap.innerHTML = '<div class="card"><div class="empty">没有符合筛选条件的日志</div></div>';
      return;
    }
    const shown = rows.slice(0, 2000);
    const html = '<div class="table-wrap" style="max-height:640px">' +
      '<table class="table"><thead><tr>' +
      '<th>时间</th><th>方法</th><th>路径</th><th>状态</th><th>耗时</th><th>请求标识</th><th style="width:28px"></th>' +
      '</tr></thead><tbody>' + shown.map((r, i) => logRowHTML(r, i)).join('') + '</tbody></table></div>' +
      (rows.length > 2000 ? '<div class="form-hint" style="margin-top:8px">共 ' + rows.length + ' 条，仅展示前 2000 条，请收窄日期范围或筛选条件。</div>' : '');
    wrap.innerHTML = html;
  }

  function render() {
    const host = $('#page-logs');
    if (!host) return;
    if (!logsQuery.from) logsQuery.from = todayStr();
    if (!logsQuery.to) logsQuery.to = todayStr();
    const statusOpts = STATUS_OPTIONS.map(o =>
      '<option value="' + o[0] + '"' + (logsFilter.status === o[0] ? ' selected' : '') + '>' + o[1] + '</option>'
    ).join('');
    host.innerHTML =
      '<div class="page-head">' +
      '<div><div class="page-title">日志</div><div class="page-desc">按天查看访问日志，定位单个请求</div></div>' +
      '<button class="btn btn-sm" data-act="logs-reload">⟳ 手动刷新</button>' +
      '</div>' +
      '<div class="card">' +
      '<div class="log-toolbar">' +
      '<input type="date" class="input input-sm" id="log-from" value="' + esc(logsQuery.from) + '">' +
      '<span class="muted">至</span>' +
      '<input type="date" class="input input-sm" id="log-to" value="' + esc(logsQuery.to) + '">' +
      '<button class="btn btn-sm btn-primary" data-act="log-query">查询</button>' +
      '<button class="btn btn-sm" data-act="log-export">导出下载</button>' +
      '<span class="toolbar-divider"></span>' +
      '<input class="input input-sm log-trace" id="log-trace" placeholder="请求标识过滤（trace_id）" value="' + esc(logsFilter.traceId) + '">' +
      '<select class="select select-sm" id="log-status">' + statusOpts + '</select>' +
      '<label class="chk"><input type="checkbox" id="log-only-error"' + (logsFilter.onlyError ? ' checked' : '') + '><span>只看异常（≥4xx）</span></label>' +
      '<button class="btn btn-sm btn-text" data-act="log-reset">重置</button>' +
      '</div>' +
      '<div id="log-table-wrap"></div>' +
      '</div>';
    renderTable();
    // 绑定筛选控件
    const trace = $('#log-trace');
    trace.addEventListener('input', debounce(() => {
      logsFilter.traceId = trace.value.trim();
      renderTable();
    }, 300));
    const stSel = $('#log-status');
    stSel.addEventListener('change', () => {
      logsFilter.status = stSel.value;
      renderTable();
    });
    const onlyErr = $('#log-only-error');
    onlyErr.addEventListener('change', () => {
      logsFilter.onlyError = onlyErr.checked;
      renderTable();
    });
  }

  // 按日期范围查询（读取工具栏日期输入）
  async function query() {
    logsQuery.from = $('#log-from').value || todayStr();
    logsQuery.to = $('#log-to').value || todayStr();
    if (logsQuery.from > logsQuery.to) {
      toast('开始日期不能晚于结束日期', 'error');
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
    const lines = rows.map(r => JSON.stringify(r));
    const blob = new Blob([lines.join('\n')], { type: 'application/x-ndjson;charset=utf-8' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'access-' + logsQuery.from + '_' + logsQuery.to + '.jsonl';
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(a.href);
    toast('已导出 ' + rows.length + ' 条日志', 'success');
  }

  // 重置筛选条件
  function resetFilter() {
    logsFilter.traceId = '';
    logsFilter.status = '';
    logsFilter.onlyError = false;
    render();
  }

  // 行展开 / 收起
  function toggleExpand(idx) {
    logsExpanded[idx] = !logsExpanded[idx];
    renderTable();
  }

  window.Rock.views.logs = {
    loadPage,
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
