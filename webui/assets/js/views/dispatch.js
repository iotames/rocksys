/* ==========================================================================
 * RockSys 管理控制台 - views/dispatch.js 路由分发页（ROUTE_DISPATCH STEP7）
 * 状态区（规则源/三计数/⟳ 重载）+ 三视图切换（路由规则/负载均衡器/上游节点）+
 * 命中测试器（规则视图右侧常驻卡）+ 三组表单弹层 + 节点健康三态（绿红灰）+
 * DISPATCH_ENABLED 未开启引导卡（豁免 toast）；DB 未就绪 503 按普通错误弹 error toast。
 * 交互口径唯一依据：docs/plan/ROUTE_DISPATCH_DESIGN_PLAN.md「WebUI 设计」章节；
 * 提示全走 Rock.ui.toast（§4.10），错误不自动消失，文案三要素。
 * 挂载到全局命名空间 window.Rock.views.dispatch。
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
  const openModal = Rock.ui.openModal;
  const skeletonHTML = Rock.ui.skeletonHTML;

  const PAGE = '#page-dispatch';
  const BASE = '/admin/dispatch';

  // ── 页内状态 ─────────────────────────────────────────────────────────
  const st = {
    view: 'rules',                 // 'rules' | 'upstreams' | 'nodes'
    enabled: null,                 // null=未知 / true / false（DISPATCH_ENABLED，经 /admin/switch/list）
    ready: null,                   // 快照是否构建成功（/admin/dispatch/health.ready）
    counts: { rules: null, upstreams: null, nodes: null },
    meta: null,                    // 枚举字典（/admin/dispatch/rules/meta）
    upstreams: [],                 // 均衡器缓存（规则表单下拉 + 列表名称映射）
    nodes: [],                     // 节点缓存（均衡器关系编辑器下拉，含实时健康）
    tags: [],                      // 标签缓存（规则表单多选）
    loadedOnce: false,
  };

  // 三个视图各自的列表状态与组件实例（筛选值在 filterBar、分页在 dataTable，组件 DOM 自动同步）
  const lists = {
    rules:     { rows: [], total: 0, loaded: false, error: '', bar: null, table: null },
    upstreams: { rows: [], total: 0, loaded: false, error: '', bar: null, table: null },
    nodes:     { rows: [], total: 0, loaded: false, error: '', bar: null, table: null },
  };

  // RFC3339 → 本地可读时间（解析失败回原文）
  function fmtDT(v) {
    const d = Rock.util.fmtDateTime(v);
    return d === '--:--:--' ? (v || '') : d;
  }

  // 健康三态 → 色点（绿=健康 红=不健康 灰=未探活；/admin/dispatch/health 单一事实源）
  function healthDot(h) {
    const map = {
      ok:      ['dot-ok',  '健康'],
      bad:     ['dot-bad', '不健康'],
      unknown: ['dot-off', '未探活'],
    };
    const m = map[h] || map.unknown;
    return '<span class="dot ' + m[0] + '" data-tip="' + m[1] + '"></span> ' + m[1];
  }

  // meta 兜底（meta 接口失败时保底可用；正常以接口下发的枚举字典为准）
  function metaOf() {
    return st.meta || {
      path_types: [
        { value: 1, name: '前缀', desc: '段对齐前缀匹配且命中自身：/api 命中 /api 与 /api/x，不匹配 /apix；/ 命中一切路径' },
        { value: 2, name: '精确', desc: '路径全等匹配，不做尾斜杠归一：/api 只命中 /api' },
        { value: 3, name: '模式', desc: '段匹配：:name 捕获单段参数（注入 X-Route-Param-* 请求头）、* 通配其后剩余路径' },
      ],
      algos: [
        { value: 1, name: 'round_robin', desc: '平滑加权轮询（权重默认 1 即纯轮询）' },
        { value: 2, name: 'least_conn', desc: '在途请求最少的节点优先（平局回落轮询游标）' },
      ],
      priorities: [
        { value: 0, name: '高优', desc: '参与常规负载均衡（默认）' },
        { value: 1, name: '备份', desc: '高优健康集全不健康时才启用（NGINX backup 同款语义）' },
      ],
      match_order: { min: 1, max: 999, step: 10, suggestion: '从 10 起按 10 递增预留插入空间；域名默认兜底规则建议 999 殿后' },
    };
  }

  function pathTypeName(t) {
    const m = metaOf().path_types.find(function (x) { return x.value === Number(t); });
    return m ? m.name : ('类型' + t);
  }

  function algoName(a) {
    const m = metaOf().algos.find(function (x) { return x.value === Number(a); });
    return m ? m.name : ('algo=' + a);
  }

  // 均衡器 id → 名称（列表展示映射；缓存缺失时回退 #id）
  function upstreamName(id) {
    const u = st.upstreams.find(function (x) { return Number(x.id) === Number(id); });
    return u ? u.name : ('#' + id);
  }

  // ── 数据加载 ─────────────────────────────────────────────────────────

  // 页面加载入口（main.js pageLoaders 调用；opts.silent=程序化静默刷新，失败不弹 toast）
  async function load(opts) {
    opts = opts || {};
    if (!st.loadedOnce && !opts.silent) skeleton();
    await loadStatus(opts);
    if (st.enabled === false) { st.loadedOnce = true; render(); return; }
    await Promise.all([
      ensureMeta(opts.silent),
      ensureCaches(opts.silent),
      loadView(st.view, opts),
    ]);
    st.loadedOnce = true;
    render();
  }

  // 状态区：组件开关（DISPATCH_ENABLED 经 /admin/switch/list 透出）+ 快照就绪态 + 三计数
  async function loadStatus(opts) {
    try {
      const list = await api.get('/admin/switch/list');
      const row = (Array.isArray(list) ? list : []).find(function (x) { return x && x.name === 'dispatch'; });
      st.enabled = row ? row.state === 'enabled' : null;
    } catch (e) {
      st.enabled = null; // 开关状态不可知：不阻塞页面，状态区显示未知
    }
    // 快照就绪态与节点计数经 health 端点（DB 未就绪时 503 → 按普通错误处理，见 loadView 同款口径）
    try {
      const h = await api.get(BASE + '/health');
      st.ready = !!h.ready;
      st.counts.nodes = Number(h.total) || 0;
      lists.nodes.loaded = false; // health 计数仅作状态区预览，进入节点视图仍拉全量列表
    } catch (e) {
      st.ready = null;
    }
    // 规则/均衡器计数：limit=1 轻查询取 X-Total-Count（失败静默，进入对应视图会补齐）
    try {
      const r = await api.get(BASE + '/rules?limit=1');
      st.counts.rules = Number(r && r.total) || 0;
    } catch (e) { /* 保持 null，状态区显示 — */ }
    try {
      const u = await api.get(BASE + '/upstreams?limit=1');
      st.counts.upstreams = Number(u && u.total) || 0;
    } catch (e) { /* 保持 null */ }
  }

  async function ensureMeta(silent) {
    if (st.meta) return;
    try {
      st.meta = await api.get(BASE + '/rules/meta');
    } catch (e) {
      if (!silent && e.status !== 0 && e.status !== 503) {
        toast('枚举字典加载失败：' + (e.message || '未知错误') + '。表单下拉将使用内置兜底枚举，不影响功能', 'error');
      }
    }
  }

  // 表单数据源缓存：均衡器（下拉+名称映射）/ 节点（关系编辑器下拉，含实时健康）/ 标签（多选）
  async function ensureCaches(silent) {
    const jobs = [];
    if (!st.upstreams.length) {
      jobs.push(api.get(BASE + '/upstreams?limit=10000').then(function (r) {
        st.upstreams = (r && r.rows) || [];
      }).catch(function () { /* 下拉保底为空，打开表单时重试 */ }));
    }
    if (!st.nodes.length) {
      jobs.push(api.get(BASE + '/nodes?limit=10000').then(function (r) {
        st.nodes = (r && r.rows) || [];
      }).catch(function () { /* 同上 */ }));
    }
    if (!st.tags.length) {
      jobs.push(api.get(BASE + '/tags').then(function (r) {
        st.tags = (r && r.rows) || [];
      }).catch(function () { /* 同上 */ }));
    }
    await Promise.all(jobs);
  }

  async function refreshCaches(silent) {
    st.upstreams = []; st.nodes = []; st.tags = [];
    await ensureCaches(silent);
  }

  function viewDefs() {
    return {
      rules: {
        list: function () { return lists.rules; },
        fetch: function (silent) { return loadRules(silent); },
      },
      upstreams: {
        list: function () { return lists.upstreams; },
        fetch: function (silent) { return loadUpstreams(silent); },
      },
      nodes: {
        list: function () { return lists.nodes; },
        fetch: function (silent) { return loadNodes(silent); },
      },
    };
  }

  // 当前视图列表加载（服务端分页：limit/offset + 筛选参数下沉后端）
  async function loadView(view, opts) {
    opts = opts || {};
    const def = viewDefs()[view];
    if (!def) return;
    await def.fetch(opts.silent);
    st.counts[view] = def.list().total;
  }

  // stateParams 状态筛选下拉值 → 列表查询参数：'del'=仅已删除（include_deleted=1，不限 enabled）；
  // 其余值透传 enabled（''=全部=仅活跃行，include_deleted 缺省 0）。
  function stateParams(s) {
    return s.enabled === 'del'
      ? { include_deleted: '1' }
      : { enabled: s.enabled || '' };
  }

  function qs(base, params) {
    const p = [];
    Object.keys(params).forEach(function (k) {
      const v = params[k];
      if (v !== '' && v != null) p.push(k + '=' + encodeURIComponent(v));
    });
    return base + (p.length ? '?' + p.join('&') : '');
  }

  // ── 规则视图 ─────────────────────────────────────────────────────────

  function buildRulesInstances() {
    const L = lists.rules;
    if (L.bar) return;
    L.bar = Rock.comp.filterBar.create({
      ns: 'dispatch-rules-filter',
      live: true,
      onQuery: function () { queryRules(); },
      fields: [
        { type: 'select', key: 'pathType', width: '120px', options: [['', '类型：全部']].concat(
          metaOf().path_types.map(function (t) { return [String(t.value), t.name]; })) },
        { type: 'text', key: 'domain', placeholder: '域名', width: '140px' },
        { type: 'select', key: 'enabled', width: '110px', options: [['', '状态：全部'], ['1', '仅启用'], ['0', '仅停用'], ['del', '仅已删除']] },
        { type: 'text', key: 'keyword', placeholder: '关键词（域名/路径/标题）', width: '180px' },
      ],
    });
    L.table = Rock.comp.dataTable.create({
      ns: 'dispatch-rules',
      columns: [
        { key: 'match_order', label: '序号', render: function (r) { return '<b>' + esc(r.match_order) + '</b>'; } },
        { key: 'domain', label: '域名', render: function (r) { return r.domain ? esc(r.domain) : '<span class="muted">任意域名</span>'; } },
        { key: 'path_type', label: '路径类型', render: function (r) { return esc(pathTypeName(r.path_type)); } },
        { key: 'path_value', label: '路径值', render: function (r) { return '<span class="mono">' + esc(r.path_value) + '</span>'; } },
        { key: 'upstream_id', label: '均衡器', render: function (r) { return esc(upstreamName(r.upstream_id)); } },
        { key: 'enabled', label: '状态', render: function (r) {
          return r.enabled ? '<span class="badge badge-ok">启用</span>' : '<span class="badge badge-warn">停用</span>';
        } },
        { key: 'updated_at', label: '更新时间', render: function (r) { return esc(fmtDT(r.updated_at)); } },
      ],
      rowKey: function (r) { return String(r.id); },
      rowActions: function (row) {
        return row.deleted_at
          ? '<button class="btn btn-sm btn-text" data-act="dispatch-rule-restore" data-id="' + row.id + '">恢复</button>'
          : '<button class="btn btn-sm btn-text" data-act="dispatch-rule-edit" data-id="' + row.id + '">编辑</button>' +
            '<button class="btn btn-sm btn-text" data-act="dispatch-rule-del" data-id="' + row.id + '">删除</button>';
      },
      paging: { mode: 'server', pageSize: 20 },
      emptyText: '暂无路由规则',
      onPaging: function (s) {
        lists.rules.pageSize = s.pageSize;
        lists.rules.offset = s.offset;
        loadView('rules', {});
      },
    });
  }

  async function loadRules(silent) {
    buildRulesInstances();
    const L = lists.rules;
    const s = L.bar.state();
    const t = L.table.state();
    const pageSize = (L.pageSize = L.pageSize || t.pageSize);
    const offset = (L.offset = L.offset || 0);
    L.loaded = true;
    try {
      const r = await api.get(qs(BASE + '/rules', Object.assign({
        path_type: s.pathType || '',
        domain: s.domain || '',
        keyword: s.keyword || '',
        limit: pageSize, offset: offset,
      }, stateParams(s))));
      L.rows = (r && r.rows) || [];
      L.total = Number(r && r.total) || 0;
      L.error = '';
    } catch (e) {
      L.error = (e.message || '加载失败');
      // DB 未就绪 503 属普通错误（设计红线：路由分发页不按"功能未开启"降级——开关在组件页已单独透出），
      // 与其他失败一致弹统一 error toast；仅程序化静默刷新（silent）失败不弹，行内错误必更新。
      if (!silent && e.status !== 0) {
        toast('路由规则加载失败：' + L.error + '。请确认数据库已配置且服务正常，稍后重试', 'error');
      }
    }
    render();
  }

  async function queryRules() {
    lists.rules.offset = 0;
    lists.rules.pageSize = lists.rules.table.state().pageSize;
    lists.rules.table.go(1);
    await loadRules(false);
  }

  // ── 均衡器视图 ───────────────────────────────────────────────────────

  function buildUpstreamInstances() {
    const L = lists.upstreams;
    if (L.bar) return;
    L.bar = Rock.comp.filterBar.create({
      ns: 'dispatch-ups-filter',
      live: true,
      onQuery: function () { queryUpstreams(); },
      fields: [
        { type: 'text', key: 'keyword', placeholder: '名称', width: '160px' },
        { type: 'select', key: 'enabled', width: '110px', options: [['', '状态：全部'], ['1', '仅启用'], ['0', '仅停用'], ['del', '仅已删除']] },
      ],
    });
    L.table = Rock.comp.dataTable.create({
      ns: 'dispatch-ups',
      columns: [
        { key: 'id', label: 'ID' },
        { key: 'name', label: '名称', render: function (r) { return '<b>' + esc(r.name) + '</b>'; } },
        { key: 'algo', label: '策略', render: function (r) { return esc(algoName(r.algo)); } },
        { key: 'sticky', label: '会话保持', render: function (r) {
          return Number(r.sticky_enabled) === 1
            ? '<span class="tag tag-blue">sticky</span> <span class="mono muted">' + esc(r.sticky_cookie || '') + '</span>'
            : '<span class="muted">—</span>';
        } },
        { key: 'nodes', label: '节点数（高优/备份）', render: function (r) {
          const ns = Array.isArray(r.nodes) ? r.nodes : [];
          const pri = ns.filter(function (n) { return Number(n.priority) === 0; }).length;
          const bak = ns.length - pri;
          return fmtInt(ns.length) + '（' + fmtInt(pri) + '/' + fmtInt(bak) + '）';
        } },
        { key: 'rule_refs', label: '被引用规则数', render: function (r) {
          const n = Number(r.rule_refs);
          return n < 0 ? '—' : fmtInt(n);
        } },
        { key: 'enabled', label: '状态', render: function (r) {
          return r.enabled ? '<span class="badge badge-ok">启用</span>' : '<span class="badge badge-warn">停用</span>';
        } },
      ],
      rowKey: function (r) { return String(r.id); },
      rowActions: function (row) {
        return row.deleted_at
          ? '<button class="btn btn-sm btn-text" data-act="dispatch-up-restore" data-id="' + row.id + '">恢复</button>'
          : '<button class="btn btn-sm btn-text" data-act="dispatch-up-edit" data-id="' + row.id + '">编辑</button>' +
            '<button class="btn btn-sm btn-text" data-act="dispatch-up-del" data-id="' + row.id + '">删除</button>';
      },
      paging: { mode: 'server', pageSize: 20 },
      emptyText: '暂无负载均衡器',
      onPaging: function (s) {
        lists.upstreams.pageSize = s.pageSize;
        lists.upstreams.offset = s.offset;
        loadView('upstreams', {});
      },
    });
  }

  async function loadUpstreams(silent) {
    buildUpstreamInstances();
    const L = lists.upstreams;
    const s = L.bar.state();
    const pageSize = (L.pageSize = L.pageSize || 20);
    const offset = (L.offset = L.offset || 0);
    L.loaded = true;
    try {
      const r = await api.get(qs(BASE + '/upstreams', Object.assign({
        keyword: s.keyword || '',
        limit: pageSize, offset: offset,
      }, stateParams(s))));
      L.rows = (r && r.rows) || [];
      L.total = Number(r && r.total) || 0;
      L.error = '';
      // 同步缓存（列表数据即表单下拉数据源；健康点随列表刷新）
      if (!st.upstreams.length) st.upstreams = L.rows.slice();
    } catch (e) {
      L.error = (e.message || '加载失败');
      if (!silent && e.status !== 0) {
        toast('负载均衡器加载失败：' + L.error + '。请确认数据库已配置且服务正常，稍后重试', 'error');
      }
    }
    render();
  }

  async function queryUpstreams() {
    lists.upstreams.offset = 0;
    lists.upstreams.pageSize = lists.upstreams.table.state().pageSize;
    lists.upstreams.table.go(1);
    await loadUpstreams(false);
  }

  // ── 节点视图 ─────────────────────────────────────────────────────────

  function buildNodeInstances() {
    const L = lists.nodes;
    if (L.bar) return;
    L.bar = Rock.comp.filterBar.create({
      ns: 'dispatch-nodes-filter',
      live: true,
      onQuery: function () { queryNodes(); },
      fields: [
        { type: 'text', key: 'keyword', placeholder: '名称', width: '140px' },
        { type: 'text', key: 'url', placeholder: '地址', width: '160px' },
        { type: 'select', key: 'enabled', width: '110px', options: [['', '状态：全部'], ['1', '仅启用'], ['0', '仅停用'], ['del', '仅已删除']] },
      ],
    });
    L.table = Rock.comp.dataTable.create({
      ns: 'dispatch-nodes',
      columns: [
        { key: 'id', label: 'ID' },
        { key: 'name', label: '名称', render: function (r) { return r.name ? '<b>' + esc(r.name) + '</b>' : '<span class="muted">—</span>'; } },
        { key: 'url', label: '地址', render: function (r) { return '<span class="mono">' + esc(r.url) + '</span>'; } },
        { key: 'hc', label: '探活', render: function (r) {
          return r.hc_path
            ? '<span class="mono">' + esc(r.hc_path) + '</span> <span class="muted">每 ' + fmtInt(r.hc_interval_ms) + 'ms</span>'
            : '<span class="muted" data-tip="未配置探活路径：始终视为健康——宕机不自动摘除">未配置</span>';
        } },
        { key: 'health', label: '实时健康', render: function (r) { return healthDot(r.health); } },
        { key: 'rel_refs', label: '被引用', render: function (r) {
          const n = Number(r.rel_refs);
          return n < 0 ? '—' : fmtInt(n);
        } },
        { key: 'enabled', label: '状态', render: function (r) {
          return r.enabled ? '<span class="badge badge-ok">启用</span>' : '<span class="badge badge-warn">停用</span>';
        } },
      ],
      rowKey: function (r) { return String(r.id); },
      rowActions: function (row) {
        return row.deleted_at
          ? '<button class="btn btn-sm btn-text" data-act="dispatch-node-restore" data-id="' + row.id + '">恢复</button>'
          : '<button class="btn btn-sm btn-text" data-act="dispatch-node-edit" data-id="' + row.id + '">编辑</button>' +
            '<button class="btn btn-sm btn-text" data-act="dispatch-node-del" data-id="' + row.id + '">删除</button>';
      },
      paging: { mode: 'server', pageSize: 20 },
      emptyText: '暂无上游节点',
      onPaging: function (s) {
        lists.nodes.pageSize = s.pageSize;
        lists.nodes.offset = s.offset;
        loadView('nodes', {});
      },
    });
  }

  async function loadNodes(silent) {
    buildNodeInstances();
    const L = lists.nodes;
    const s = L.bar.state();
    const pageSize = (L.pageSize = L.pageSize || 20);
    const offset = (L.offset = L.offset || 0);
    L.loaded = true;
    try {
      const r = await api.get(qs(BASE + '/nodes', Object.assign({
        keyword: s.keyword || '',
        url: s.url || '',
        limit: pageSize, offset: offset,
      }, stateParams(s))));
      L.rows = (r && r.rows) || [];
      L.total = Number(r && r.total) || 0;
      L.error = '';
      if (!st.nodes.length) st.nodes = L.rows.slice();
    } catch (e) {
      L.error = (e.message || '加载失败');
      if (!silent && e.status !== 0) {
        toast('上游节点加载失败：' + L.error + '。请确认数据库已配置且服务正常，稍后重试', 'error');
      }
    }
    render();
  }

  async function queryNodes() {
    lists.nodes.offset = 0;
    lists.nodes.pageSize = lists.nodes.table.state().pageSize;
    lists.nodes.table.go(1);
    await loadNodes(false);
  }

  // ── 页面渲染 ─────────────────────────────────────────────────────────

  function skeleton() {
    const host = $(PAGE);
    if (host) host.innerHTML = skeletonHTML(5);
  }

  function render() {
    const host = $(PAGE);
    if (!host) return;
    if (st.enabled === false) { host.innerHTML = guideHTML(); return; }
    const defs = viewDefs();
    const def = defs[st.view];
    const L = def.list();
    let body;
    if (!L.loaded) {
      body = '<div class="card">' + Rock.comp.empty.message({ text: '加载中…' }) + '</div>';
    } else if (L.error && !L.rows.length) {
      body = '<div class="card"><div class="alert alert-danger">列表加载失败：' + esc(L.error) +
        '。请确认数据库已配置且服务正常后点击「⟳ 重载」或刷新重试。</div></div>';
    } else {
      body = renderView(st.view, L);
    }
    host.innerHTML =
      Rock.comp.head.headHTML({
        title: '路由分发',
        desc: '三层路由与负载均衡：上游节点（登记与体检）→ 负载均衡器（策略与会话保持）→ 路由规则（匹配与引用）；规则按序号升序逐条匹配（域名维度可选参与），命中即停，全未命中走默认后端。组件启停开关在「组件 → 分发 dispatch」详情页。',
        actions: '<button class="btn btn-sm" data-act="dispatch-reload-all" data-tip="重新从数据库构建运行时快照（多实例/外部改库后的手动同步出口）；本页各写操作保存即自动热更，无需手动重载">⟳ 重载快照</button>',
      }) +
      statusHTML() +
      Rock.comp.tabs.tabsHTML(
        [
          { name: 'rules', label: '路由规则' },
          { name: 'upstreams', label: '负载均衡器' },
          { name: 'nodes', label: '上游节点' },
        ],
        st.view,
        { act: 'dispatch-view', nameAttr: 'data-view' }
      ) +
      body;
    // 重渲染后重绑分页/筛选委托（wrap 元素每次重建）
    bindView(st.view);
  }

  function bindView(view) {
    const host = $(PAGE);
    if (!host) return;
    const L = lists[view];
    if (L.bar) L.bar.bind(host);
    const wrap = host.querySelector('#dispatch-table-wrap');
    if (L.table && wrap) L.table.bind(wrap);
  }

  // DISPATCH_ENABLED 未开启：整页引导卡（豁免 toast——降级引导态，页内已给出开关位置与开启路径）
  function guideHTML() {
    return Rock.comp.head.headHTML({ title: '路由分发', desc: '' }) +
      '<div class="card"><div class="empty">' +
      '<p><b>路由分发组件未开启</b></p>' +
      '<p>本页管理的是路由数据（规则/均衡器/节点），组件未开启时数据仍可维护，但不参与请求转发。</p>' +
      '<p>开启路径：侧边栏「组件 → 分发 dispatch」详情页，打开组件开关（DISPATCH_ENABLED）即可，热更生效、无需重启。</p>' +
      '<button class="btn btn-primary" data-act="nav-detail" data-route="components/dispatch">前往组件页开启 →</button>' +
      '</div></div>';
  }

  function statusHTML() {
    const readyTxt = st.ready == null ? '' :
      (st.ready
        ? '<span class="tag tag-green">规则源：数据库 ● 已构建</span>'
        : '<span class="tag tag-red">规则源：数据库未就绪（快照未构建，全部走默认后端）</span>');
    const c = st.counts;
    const cnt = function (v) { return v == null ? '—' : fmtInt(v); };
    return '<div class="card"><div class="log-toolbar">' +
      readyTxt +
      '<span class="muted">规则 <b>' + cnt(c.rules) + '</b> 条 · 均衡器 <b>' + cnt(c.upstreams) + '</b> 个 · 节点 <b>' + cnt(c.nodes) + '</b> 个</span>' +
      '<span class="muted">规则按序号升序逐条匹配（域名维度可选参与），命中即停；全未命中走默认后端。</span>' +
      '</div></div>';
  }

  function renderView(view, L) {
    if (view === 'rules') return renderRulesView(L);
    if (view === 'upstreams') return renderUpstreamsView(L);
    return renderNodesView(L);
  }

  function tableHTML(view, L) {
    return '<div id="dispatch-table-wrap">' +
      (L.error ? '<div class="alert alert-warning">本次刷新失败：' + esc(L.error) + '，以下为上次数据</div>' : '') +
      L.table.html(L.rows, { total: L.total }) +
      '</div>';
  }

  // 规则视图：筛选 + 表格（左） + 命中测试器常驻卡（右）
  function renderRulesView(L) {
    // 左列表 + 右命中测试器常驻卡（flex 两栏，窄屏由浏览器自然换行）
    return '<div style="display:flex;gap:16px;align-items:flex-start">' +
      '<div class="card" style="flex:1;min-width:0">' +
      '<div class="card-title">路由规则 <span class="card-sub">序号 1-999 升序、命中即停；域名默认兜底规则建议 999</span>' +
      '<span class="comp-actions"><button class="btn btn-sm btn-primary" data-act="dispatch-rule-new">＋ 新增规则</button></span></div>' +
      L.bar.html() +
      tableHTML('rules', L) +
      '</div>' +
      testerHTML() +
      '</div>';
  }

  // 命中测试器（规则视图右侧常驻卡；POST /rules/match-test 只读无副作用）
  function testerHTML() {
    return '<div class="card" style="width:360px;flex-shrink:0">' +
      '<div class="card-title">命中测试器 <span class="card-sub">只读，不影响转发状态</span></div>' +
      '<div class="form-row"><label class="form-label">Host（可空 = 任意）</label>' +
      Rock.comp.form.input({ id: 'dispatch-test-host', sm: true, width: 'full', placeholder: '如 a.com（留空 = 任意域名）' }) + '</div>' +
      '<div class="form-row"><label class="form-label">Path</label>' +
      Rock.comp.form.input({ id: 'dispatch-test-path', sm: true, width: 'full', placeholder: '/api/order/1', value: '/' }) + '</div>' +
      '<div class="log-toolbar"><button class="btn btn-sm btn-primary" data-act="dispatch-test-run">测试</button></div>' +
      '<div id="dispatch-test-result" class="form-hint">输入 Host 与 Path 后点击「测试」。</div>' +
      '</div>';
  }

  function renderUpstreamsView(L) {
    return '<div class="card">' +
      '<div class="card-title">负载均衡器 <span class="card-sub">策略 + 会话保持 + 节点关系（多对多）；停用 = 引用它的规则命中即 503</span>' +
      '<span class="comp-actions"><button class="btn btn-sm btn-primary" data-act="dispatch-up-new">＋ 新增均衡器</button></span></div>' +
      L.bar.html() +
      tableHTML('upstreams', L) +
      '</div>';
  }

  function renderNodesView(L) {
    return '<div class="card">' +
      '<div class="card-title">上游节点 <span class="card-sub">登记一次、全局探活去重；健康状态由探活按周期巡检</span>' +
      '<span class="comp-actions"><button class="btn btn-sm btn-primary" data-act="dispatch-node-new">＋ 登记节点</button></span></div>' +
      '<div class="form-hint" style="margin:0 0 6px">健康状态由探活按周期巡检得出。节点刚发生故障时，最长要等约一个探活周期（默认 20s）才会转为不健康并摘除，窗口期内新请求仍会转发到该节点并失败——属正常现象，不是网关故障。</div>' +
      L.bar.html() +
      tableHTML('nodes', L) +
      '</div>';
  }

  // ── 视图切换 / 重载 / 命中测试 ────────────────────────────────────────

  async function switchView(view) {
    if (st.view === view) return;
    st.view = view;
    if (!lists[view].loaded) await loadView(view, {});
    else render();
  }

  // 手动重载：Rebuild 四表全量快照（多实例/外部改库出口）；成功后整页刷新
  async function reloadAll() {
    try {
      await api.post(BASE + '/reload')({});
      toast('快照已重载（四表全量重建）', 'success');
    } catch (e) {
      toast('快照重载失败：' + (e.message || '未知错误'), 'error');
    }
    st.loadedOnce = false;
    await load({ silent: false });
  }

  async function runTest() {
    const hostEl = $('#dispatch-test-host');
    const pathEl = $('#dispatch-test-path');
    const out = $('#dispatch-test-result');
    const host = ((hostEl || {}).value || '').trim();
    const path = ((pathEl || {}).value || '').trim() || '/';
    if (out) out.innerHTML = '<span class="muted">测试中…</span>';
    try {
      const r = await api.post(BASE + '/rules/match-test')({ host: host, path: path });
      if (!out) return;
      out.innerHTML = testResultHTML(r);
    } catch (e) {
      if (out) out.innerHTML = '<span class="muted">测试失败：' + esc(e.message || '未知错误') + '</span>';
      if (e.status !== 0) toast('命中测试失败：' + (e.message || '未知错误') + '。请稍后重试', 'error');
    }
  }

  function testResultHTML(r) {
    if (!r) return '<span class="muted">无结果</span>';
    let html = '<div class="alert ' + (r.hit ? 'alert-info' : 'alert') + '">';
    if (!r.hit) {
      html += '<b>未命中</b>：' + esc(r.message || '走默认后端');
      return html + '</div>';
    }
    const rule = r.rule || {};
    html += '<b>命中规则 #' + esc(rule.id) + '</b>（序号 ' + esc(rule.match_order) + '）<br>' +
      '<span class="mono">[' + esc(rule.domain || '任意域名') + '] ' + esc(rule.path_value) + '</span> ' + esc(pathTypeName(rule.path_type)) + '<br>' +
      '→ 均衡器 <b>' + esc((r.upstream || {}).name || '—') + '</b><br>';
    if (r.position === 'node' && r.node) {
      html += '→ 选中节点 <span class="mono">' + esc(r.node.url) + '</span>' + (Number(r.node.priority) === 1 ? '（备份）' : '');
    } else {
      html += '→ <b>' + esc(r.message || r.position) + '</b>';
    }
    if (r.params && Object.keys(r.params).length) {
      html += '<br><span class="muted">路径参数：' + esc(Object.keys(r.params).map(function (k) {
        return k + '=' + r.params[k];
      }).join('，')) + '</span>';
    }
    return html + '</div><div class="form-hint">' + esc(r.message || '') + '</div>';
  }

  // ── 删除 / 恢复（三实体共用模式：确认弹层 + 文案三要素）────────────────

  function del(kind, id) {
    const titles = {
      rule: ['删除路由规则', '将软删除该规则（可恢复），立即从匹配快照移除（保存即热更）。'],
      up: ['删除负载均衡器', '将软删除该均衡器（可恢复）；若仍被路由规则引用，服务端将拒绝删除。'],
      node: ['删除上游节点', '将软删除该节点（可恢复）；若仍被均衡器节点关系引用，服务端将拒绝删除。'],
    };
    const urls = {
      rule: BASE + '/rules/delete',
      up: BASE + '/upstreams/delete',
      node: BASE + '/nodes/delete',
    };
    confirmDialog({
      title: titles[kind][0],
      message: titles[kind][1] + '确认？',
      confirmText: '删除',
      danger: true,
    }).then(function (ok) {
      if (!ok) return;
      api.post(urls[kind])({ id: Number(id) }).then(function () {
        toast('已删除（可从列表恢复）', 'success');
        afterWriteRefresh(kind);
      }).catch(function (e) {
        toast((e.message || '删除失败') + '。请按提示处理后重试', 'error');
      });
    });
  }

  function restore(kind, id) {
    const urls = {
      rule: BASE + '/rules/restore',
      up: BASE + '/upstreams/restore',
      node: BASE + '/nodes/restore',
    };
    api.post(urls[kind])({ id: Number(id) }).then(function () {
      toast('已恢复', 'success');
      afterWriteRefresh(kind);
    }).catch(function (e) {
      toast((e.message || '恢复失败') + '。请按提示处理（常见原因：同名/同地址活跃条目冲突，或规则引用的均衡器已删除）后重试', 'error');
    });
  }

  // 写成功后的刷新：列表静默重拉（失败不弹 toast，行内提示更新）+ 状态区计数与缓存同步
  async function afterWriteRefresh(kind) {
    if (kind === 'up' || !st.upstreams.length) await refreshCaches(true);
    await loadView(st.view, { silent: true });
    render();
  }

  // ── 输入校验小工具 ───────────────────────────────────────────────────

  function markInvalid(el) {
    if (!el) return;
    el.classList.add('is-invalid');
    el.addEventListener('input', function onInput() { el.classList.remove('is-invalid'); el.removeEventListener('input', onInput); });
  }

  // 域名校验：精确域名（无端口、无通配、无路径）；保存时归一转小写
  function validDomain(v) {
    return !v || (!/[:/\s*]/.test(v));
  }

  // ── 规则表单弹层 ─────────────────────────────────────────────────────

  // 标签多选（chip 编辑器）：下拉已有标签 + 回车/按钮新建；提交为标签名数组（整组保存）
  function tagChipsHTML(selected) {
    return (selected || []).map(function (t) {
      return '<span class="tag tag-blue" data-tag="' + esc(t) + '">' + esc(t) +
        ' <b data-tag-x="' + esc(t) + '" style="cursor:pointer" title="移除">✕</b></span>';
    }).join(' ');
  }

  function openRuleForm(row) {
    const m = metaOf();
    const isEdit = !!row;
    const tags = isEdit && Array.isArray(row.tags) ? row.tags.map(String) : [];
    const selTags = tags.slice();
    // 序号默认：当前最大序号 + 10（超 999 钳到 999 并提示）；域名兜底组合（留空域名 + 路径 /）默认 999
    let defOrder = 10;
    if (isEdit) {
      defOrder = Number(row.match_order) || 10;
    } else {
      // 默认 = 当前最大序号 + 10（计数为总数非最大序号，从已加载行推断；超 999 钳到 999）
      const rows = lists.rules.rows || [];
      const mx = rows.reduce(function (a, r) { return Math.max(a, Number(r.match_order) || 0); }, 0);
      defOrder = Math.min(999, mx + 10) || 10;
    }
    const pathTypeOptions = m.path_types.map(function (t) { return [String(t.value), t.name + '：' + t.desc]; });
    const upOptions = [['', '— 选择均衡器 —']].concat(st.upstreams.map(function (u) {
      const n = Array.isArray(u.nodes) ? u.nodes.length : 0;
      return [String(u.id), u.name + '（' + fmtInt(n) + ' 节点）'];
    }));
    const tagOptions = [['', '— 选择已有标签 —']].concat(st.tags.map(function (t) {
      return [String(t.name), t.name];
    }));
    const overlay = openModal({
      title: isEdit ? '编辑路由规则 #' + row.id : '新增路由规则',
      width: 560,
      body:
        '<div class="form-row"><label class="form-label">域名（可选维度）</label>' +
        Rock.comp.form.input({ id: 'dr-domain', width: 'full', value: isEdit ? (row.domain || '') : '', placeholder: '留空 = 任意域名' }) +
        '<div class="form-hint">精确匹配（自动转小写、剥端口）；不支持端口与通配符——泛域名请拆为多条精确域名规则。域名默认兜底规则 = 留空域名 + 路径 / + 序号 999。</div></div>' +
        '<div class="form-row"><label class="form-label">路径类型</label>' +
        Rock.comp.form.select({ id: 'dr-path-type', width: 'full', options: pathTypeOptions, selected: String(isEdit ? row.path_type : 1) }) +
        '<div class="form-hint" id="dr-path-type-desc"></div></div>' +
        '<div class="form-row"><label class="form-label">路径值</label>' +
        Rock.comp.form.input({ id: 'dr-path-value', mono: true, width: 'full', value: isEdit ? (row.path_value || '/') : '/' }) +
        '<div class="form-hint" id="dr-path-hint"></div></div>' +
        '<div class="form-row"><label class="form-label">匹配序号（1-999，升序命中即停）</label>' +
        Rock.comp.form.input({ id: 'dr-order', type: 'number', width: 'sm', value: String(defOrder), attrs: { min: 1, max: 999 } }) +
        '<div class="form-hint">' + esc(m.match_order.suggestion || '') + '；默认 = 当前最大序号 + 10。</div></div>' +
        '<div class="form-row"><label class="form-label">均衡器（命中后转发目标）</label>' +
        Rock.comp.form.select({ id: 'dr-upstream', width: 'full', options: upOptions, selected: isEdit ? String(row.upstream_id || '') : '' }) + '</div>' +
        '<div class="form-row"><label class="form-label">标签（可多选，可回车新建）</label>' +
        '<div id="dr-tags-box">' + tagChipsHTML(selTags) + '</div>' +
        '<div class="log-toolbar" style="margin-top:6px">' +
        Rock.comp.form.select({ id: 'dr-tag-select', sm: true, width: 'md', options: tagOptions }) +
        Rock.comp.form.input({ id: 'dr-tag-new', sm: true, width: 'sm', placeholder: '新标签名（回车添加）' }) +
        '<button class="btn btn-sm" data-act="dr-tag-add">添加</button>' +
        '</div></div>' +
        '<div class="form-row"><label class="form-label">标题</label>' +
        Rock.comp.form.input({ id: 'dr-title', width: 'full', value: isEdit ? (row.title || '') : '', placeholder: '规则标题（可空）' }) + '</div>' +
        '<div class="form-row"><label class="form-label">备注</label>' +
        Rock.comp.form.textarea({ id: 'dr-remark', rows: 2, value: isEdit ? (row.remark || '') : '' }) + '</div>' +
        (isEdit
          ? '<div class="form-row"><label class="form-label">启用</label>' +
            Rock.comp.form.switch({ id: 'dr-enabled', checked: !!row.enabled, title: '停用行不参与匹配，保留配置' }) +
            '<div class="form-hint">停用后该规则不参与匹配（保留配置资产）；立即热更生效。</div></div>'
          : ''),
      footer: '<button class="btn" data-modal-act="cancel">取消</button>' +
        '<button class="btn btn-primary" data-act="dr-save">保存</button>',
    });

    // 路径类型切换：占位与校验提示联动
    const pathHints = {
      1: '前缀：以 / 开头，段对齐匹配且命中自身（/api 命中 /api 与 /api/x，不匹配 /apix；/ 命中一切路径）',
      2: '精确：以 / 开头，路径全等匹配，不做尾斜杠归一（/api 只命中 /api）',
      3: '模式：以 / 开头，:name 捕获单段参数、* 通配其后剩余路径（如 /api/order/:id/*）',
    };
    function syncPathType() {
      const t = Number((overlay.querySelector('#dr-path-type') || {}).value || 1);
      const inputEl = overlay.querySelector('#dr-path-value');
      const hint = overlay.querySelector('#dr-path-hint');
      const desc = overlay.querySelector('#dr-path-type-desc');
      if (desc) desc.textContent = (m.path_types.find(function (x) { return x.value === t; }) || {}).desc || '';
      if (hint) hint.textContent = pathHints[t] || '';
      if (inputEl) inputEl.placeholder = { 1: '/api', 2: '/api/order/1', 3: '/api/order/:id/*' }[t] || '/';
    }
    syncPathType();
    overlay.querySelector('#dr-path-type').addEventListener('change', syncPathType);

    // 域名失焦转小写（归一预览；保存时同样归一）
    const domainEl = overlay.querySelector('#dr-domain');
    if (domainEl) domainEl.addEventListener('blur', function () { domainEl.value = domainEl.value.trim().toLowerCase(); });

    // 序号超界钳制（输入即钳并提示，非阻断）
    const orderEl = overlay.querySelector('#dr-order');
    if (orderEl) orderEl.addEventListener('change', function () {
      const v = Math.floor(Number(orderEl.value) || 0);
      if (v > 999) { orderEl.value = '999'; toast('序号已钳制到上界 999（匹配序号范围 1-999）', 'warning', 5000); }
      else if (v < 1 && orderEl.value !== '') { orderEl.value = '1'; }
    });

    // 标签 chip 编辑（页内局部操作，不触发保存）
    function addTag(name) {
      name = String(name || '').trim().toLowerCase();
      if (!name) return;
      if (selTags.indexOf(name) >= 0) { toast('标签「' + name + '」已在列表中', 'info', 2500); return; }
      selTags.push(name);
      overlay.querySelector('#dr-tags-box').innerHTML = tagChipsHTML(selTags);
    }
    overlay.addEventListener('click', function (e) {
      const x = e.target.closest('[data-tag-x]');
      if (x) {
        const name = x.getAttribute('data-tag-x');
        const i = selTags.indexOf(name);
        if (i >= 0) selTags.splice(i, 1);
        overlay.querySelector('#dr-tags-box').innerHTML = tagChipsHTML(selTags);
        return;
      }
      if (e.target.closest('[data-act="dr-tag-add"]')) {
        const sel = overlay.querySelector('#dr-tag-select');
        const newEl = overlay.querySelector('#dr-tag-new');
        if (newEl && newEl.value.trim()) { addTag(newEl.value); newEl.value = ''; return; }
        if (sel && sel.value) { addTag(sel.value); sel.value = ''; }
      }
    });
    const tagNewEl = overlay.querySelector('#dr-tag-new');
    if (tagNewEl) tagNewEl.addEventListener('keydown', function (e) {
      if (e.key === 'Enter') { e.preventDefault(); addTag(tagNewEl.value); tagNewEl.value = ''; }
    });

    overlay.addEventListener('click', async function (e) {
      if (!e.target.closest('[data-act="dr-save"]')) return;
      const body = {
        id: isEdit ? Number(row.id) : 0,
        match_order: Math.min(999, Math.max(1, Math.floor(Number((overlay.querySelector('#dr-order') || {}).value) || 0))) || 0,
        domain: ((overlay.querySelector('#dr-domain') || {}).value || '').trim().toLowerCase(),
        path_type: Number((overlay.querySelector('#dr-path-type') || {}).value || 1),
        path_value: ((overlay.querySelector('#dr-path-value') || {}).value || '').trim(),
        title: ((overlay.querySelector('#dr-title') || {}).value || '').trim(),
        upstream_id: Number((overlay.querySelector('#dr-upstream') || {}).value || 0),
        enabled: isEdit ? !!(overlay.querySelector('#dr-enabled') || {}).checked : true,
        remark: ((overlay.querySelector('#dr-remark') || {}).value || ''),
        tags: selTags.slice(),
      };
      // 前端先行校验（文案三要素；服务端仍全量校验兜底）
      if (!body.match_order || body.match_order < 1 || body.match_order > 999) {
        toast('保存失败：匹配序号应为 1-999 的整数（升序匹配、命中即停）。请修正序号后重试', 'error');
        markInvalid(overlay.querySelector('#dr-order')); return;
      }
      if (!validDomain(body.domain)) {
        toast('保存失败：域名仅支持精确域名（不带端口、不支持通配符）；端口维度无需填写（匹配时自动剥端口），泛域名请拆为多条精确域名规则。请修正后重试', 'error');
        markInvalid(overlay.querySelector('#dr-domain')); return;
      }
      if (!body.path_value || body.path_value.charAt(0) !== '/') {
        toast('保存失败：路径值必须以 / 开头（如 /api）。请修正路径值后重试', 'error');
        markInvalid(overlay.querySelector('#dr-path-value')); return;
      }
      if (!body.upstream_id) {
        toast('保存失败：请选择均衡器——命中规则后转发到该均衡器选出的节点。请从下拉选择后重试', 'error');
        markInvalid(overlay.querySelector('#dr-upstream')); return;
      }
      try {
        const r = isEdit
          ? await api.post(BASE + '/rules/update')(body)
          : await api.post(BASE + '/rules')(body);
        overlay.remove();
        toast(isEdit ? '规则已更新（快照已热更）' : '规则已新增（快照已热更）', 'success');
        if (r && Array.isArray(r.warnings) && r.warnings.length) {
          // 同序号非阻断提示：列出同序号的其他规则（先建者先匹配），防全局兜底静默吞掉域名专属规则
          openModal({
            title: '已保存，但有同序号提示',
            width: 520,
            body: '<div class="form-hint">以下提示不影响本次保存（非阻断）：</div><ul>' +
              r.warnings.map(function (w) { return '<li>' + esc(w) + '</li>'; }).join('') + '</ul>',
            footer: '<button class="btn btn-primary" data-modal-act="cancel">知道了</button>',
          });
        }
        await refreshCaches(true);
        await afterWriteRefresh('rule');
      } catch (err) {
        toast('规则保存失败：' + (err.message || '未知错误') + '。请按提示修正后重试；数据未保存', 'error');
      }
    });
  }

  // ── 均衡器表单弹层（含节点关系编辑器）──────────────────────────────────

  function relRowHTML(idx, rel, nodes) {
    rel = rel || {};
    const nodeOptions = [['', '— 选择节点 —']].concat(nodes.map(function (n) {
      const h = { ok: '●健康', bad: '●不健康', unknown: '○未探活' }[n.health] || '○未探活';
      return [String(n.id), (n.name || n.url) + ' ' + h];
    }));
    return '<div class="log-toolbar dr-rel-row" data-rel-idx="' + idx + '" style="margin-bottom:4px">' +
      Rock.comp.form.select({ field: 'node-id', sm: true, width: 'md', options: nodeOptions, selected: String(rel.node_id || '') }) +
      Rock.comp.form.input({ field: 'weight', sm: true, type: 'number', width: 'xs', value: String(rel.weight || 1), attrs: { min: 1, title: '权重（仅 round_robin 生效）', 'data-tip': '权重：平滑加权轮询用；仅 round_robin 策略生效，least_conn 忽略权重' } }) +
      Rock.comp.form.select({ field: 'priority', sm: true, width: 'sm', options: [['0', '高优'], ['1', '备份']], selected: String(rel.priority || 0) }) +
      '<button class="btn btn-sm btn-text" data-rel-del="' + idx + '" title="移除该行">✕</button>' +
      '</div>';
  }

  function openUpstreamForm(row) {
    const m = metaOf();
    const isEdit = !!row;
    const algoOptions = m.algos.map(function (a) { return [String(a.value), a.name + '：' + a.desc]; });
    const rels = isEdit && Array.isArray(row.nodes)
      ? row.nodes.map(function (n) { return { node_id: n.node_id, weight: n.weight, priority: n.priority }; })
      : [];
    const refs = isEdit ? Number(row.rule_refs) || 0 : 0;
    const overlay = openModal({
      title: isEdit ? '编辑负载均衡器 #' + row.id : '新增负载均衡器',
      width: 640,
      body:
        '<div class="form-row"><label class="form-label">名称（活跃行唯一）</label>' +
        Rock.comp.form.input({ id: 'du-name', width: 'full', value: isEdit ? (row.name || '') : '', placeholder: '如 订单服务-会话保持池' }) + '</div>' +
        '<div class="form-row"><label class="form-label">均衡策略</label>' +
        Rock.comp.form.select({ id: 'du-algo', width: 'full', options: algoOptions, selected: String(isEdit ? row.algo : 1) }) +
        '<div class="form-hint">least_conn 的代价与已知边界在选中时单独提示，可继续使用。</div></div>' +
        '<div class="form-row"><label class="form-label">会话保持（sticky cookie）</label>' +
        Rock.comp.form.switch({ id: 'du-sticky', checked: isEdit && Number(row.sticky_enabled) === 1, title: '网关自种 Cookie 粘性，与任意策略正交组合' }) +
        '<span id="du-cookie-wrap" style="display:none">' +
        Rock.comp.form.input({ id: 'du-cookie', sm: true, width: 'sm', value: isEdit ? (row.sticky_cookie || 'rocksys_node') : 'rocksys_node', placeholder: 'Cookie 名' }) +
        '</span>' +
        '<div class="form-hint">开启后网关在选点响应中种会话级 Cookie（默认名 rocksys_node），后续携带该 Cookie 的请求直路由原节点；节点失效自动回落选点并重种。</div></div>' +
        '<div class="form-row"><label class="form-label">节点关系（整组保存；高优全挂回落备份）</label>' +
        '<div id="du-rel-rows">' + rels.map(function (r, i) { return relRowHTML(i, r, st.nodes); }).join('') + '</div>' +
        '<div class="log-toolbar"><button class="btn btn-sm" data-act="du-rel-add">＋ 添加节点行</button>' +
        '<span class="muted">权重仅 round_robin 生效；节点健康点实时显示。无节点关系的均衡器无法通过保存校验。</span></div></div>' +
        '<div class="form-row"><label class="form-label">备注</label>' +
        Rock.comp.form.textarea({ id: 'du-remark', rows: 2, value: isEdit ? (row.remark || '') : '' }) + '</div>' +
        (isEdit
          ? '<div class="form-row"><label class="form-label">启用</label>' +
            Rock.comp.form.switch({ id: 'du-enabled', checked: !!row.enabled }) +
            '<div class="form-hint" id="du-enabled-hint">' +
            (refs > 0
              ? '当前被 ' + fmtInt(refs) + ' 条路由规则引用：停用后这些规则命中将返回 503（fail-closed，显式失败优于静默落到后续规则）。'
              : '当前无路由规则引用本均衡器。') +
            '</div></div>'
          : ''),
      footer: '<button class="btn" data-modal-act="cancel">取消</button>' +
        '<button class="btn btn-primary" data-act="du-save">保存</button>',
    });

    let relSeq = rels.length;
    overlay.addEventListener('click', function (e) {
      const del = e.target.closest('[data-rel-del]');
      if (del) {
        const rowEl = del.closest('.dr-rel-row');
        if (rowEl) rowEl.remove();
        return;
      }
      if (e.target.closest('[data-act="du-rel-add"]')) {
        overlay.querySelector('#du-rel-rows').insertAdjacentHTML('beforeend', relRowHTML(++relSeq, null, st.nodes));
      }
    });

    // sticky 开关：展开 Cookie 名输入 + WS 首连非阻断警告
    const stickyEl = overlay.querySelector('#du-sticky');
    const cookieWrap = overlay.querySelector('#du-cookie-wrap');
    function syncSticky() {
      if (cookieWrap) cookieWrap.style.display = stickyEl.checked ? '' : 'none';
      if (stickyEl.checked) {
        toast('会话保持对普通 HTTP 请求完全生效；浏览器 WebSocket 首次连接的握手响应拿不到粘性 Cookie（隧道直写、不经响应头），需先有过一次普通 HTTP 请求完成种值，否则 WS 连接不保证粘住原节点。', 'warning', 8000);
      }
    }
    stickyEl.addEventListener('change', syncSticky);
    if (stickyEl.checked && cookieWrap) cookieWrap.style.display = '';

    // 策略选中 least_conn：非阻断警告（缓冲路径代价 + 在途计数泄漏已知边界），可继续保存
    const algoEl = overlay.querySelector('#du-algo');
    algoEl.addEventListener('change', function () {
      if (Number(algoEl.value) === 2) {
        toast('least_conn 的在途递减依赖 Tail 收尾件：dispatch 启用期命中流量走缓冲路径（该代价与策略选择无关，round_robin 亦然）；已知边界——dispatch 之后的中间件中断链的请求不经过收尾回调，存在在途计数泄漏（低频统计偏差，不影响转发正确性）。可继续保存。', 'warning', 10000);
      }
    });

    overlay.addEventListener('click', async function (e) {
      if (!e.target.closest('[data-act="du-save"]')) return;
      const body = {
        id: isEdit ? Number(row.id) : 0,
        name: ((overlay.querySelector('#du-name') || {}).value || '').trim(),
        algo: Number(algoEl.value || 1),
        sticky_enabled: !!stickyEl.checked,
        sticky_cookie: ((overlay.querySelector('#du-cookie') || {}).value || '').trim() || 'rocksys_node',
        enabled: isEdit ? !!(overlay.querySelector('#du-enabled') || {}).checked : true,
        remark: ((overlay.querySelector('#du-remark') || {}).value || ''),
        nodes: [],
      };
      if (!body.name) {
        toast('保存失败：名称必填（活跃行唯一，如「订单服务-会话保持池」）。请填写名称后重试', 'error');
        markInvalid(overlay.querySelector('#du-name')); return;
      }
      // 关系组从 DOM 收集（整组保存语义）
      const relRows = Array.prototype.slice.call(overlay.querySelectorAll('.dr-rel-row'));
      for (let i = 0; i < relRows.length; i++) {
        const r = relRows[i];
        const nodeID = Number((r.querySelector('[data-field="node-id"]') || {}).value || 0);
        if (!nodeID) {
          toast('保存失败：第 ' + (i + 1) + ' 行节点关系未选择节点。请从下拉选择或移除该行后重试', 'error');
          return;
        }
        body.nodes.push({
          node_id: nodeID,
          weight: Math.max(1, Math.floor(Number((r.querySelector('[data-field="weight"]') || {}).value) || 1)),
          priority: Number((r.querySelector('[data-field="priority"]') || {}).value || 0),
        });
      }
      try {
        if (isEdit) await api.post(BASE + '/upstreams/update')(body);
        else await api.post(BASE + '/upstreams')(body);
        overlay.remove();
        toast(isEdit ? '均衡器已更新（快照已热更）' : '均衡器已新增（快照已热更）', 'success');
        await refreshCaches(true);
        await afterWriteRefresh('up');
      } catch (err) {
        toast('均衡器保存失败：' + (err.message || '未知错误') + '。请按提示修正后重试；数据未保存', 'error');
      }
    });
  }

  // ── 节点表单弹层（含探活折叠区）───────────────────────────────────────

  function openNodeForm(row) {
    const isEdit = !!row;
    const refs = isEdit ? Number(row.rel_refs) || 0 : 0;
    const overlay = openModal({
      title: isEdit ? '编辑上游节点 #' + row.id : '登记上游节点',
      width: 560,
      body:
        (isEdit
          ? '<div class="form-row"><label class="form-label">实时健康</label><span class="v">' + healthDot(row.health) +
            (row.inflight != null ? ' <span class="muted">在途 ' + fmtInt(row.inflight) + '</span>' : '') + '</span>' +
            '<div class="form-hint">健康状态由探活按周期巡检得出（内存单一事实源，不落库）；灰 = 未探活（未配置探活路径或尚未被启用均衡器引用）。</div></div>'
          : '') +
        '<div class="form-row"><label class="form-label">节点名称（可空）</label>' +
        Rock.comp.form.input({ id: 'dn-name', width: 'full', value: isEdit ? (row.name || '') : '', placeholder: '人类可读名称（可空）' }) + '</div>' +
        '<div class="form-row"><label class="form-label">节点地址（活跃行唯一）</label>' +
        Rock.comp.form.input({ id: 'dn-url', mono: true, width: 'full', value: isEdit ? (row.url || '') : '', placeholder: 'http://10.0.0.1:9001' }) +
        '<div class="form-hint">必须为合法的 http(s)://host[:port] 地址；同地址仅登记一次（探活全局去重的前提）。</div></div>' +
        '<details style="margin:8px 0"><summary style="cursor:pointer">探活配置（可选，默认 20s 周期 / 5s 超时）</summary>' +
        '<div class="form-row" style="margin-top:8px"><label class="form-label">探活周期（毫秒）</label>' +
        Rock.comp.form.input({ id: 'dn-hc-interval', type: 'number', width: 'sm', value: String(isEdit ? (row.hc_interval_ms || 20000) : 20000), attrs: { min: 1000 } }) +
        '<div class="form-hint">节点故障到被自动摘除，最长存在约一个探活周期的窗口期，期间该节点的新请求仍会被转发并失败。</div></div>' +
        '<div class="form-row"><label class="form-label">探活超时（毫秒）</label>' +
        Rock.comp.form.input({ id: 'dn-hc-timeout', type: 'number', width: 'sm', value: String(isEdit ? (row.hc_timeout_ms || 5000) : 5000), attrs: { min: 100 } }) +
        '<div class="form-hint">应小于探活周期，否则探活请求会互相堆积。</div></div>' +
        '<div class="form-row"><label class="form-label">探活路径</label>' +
        Rock.comp.form.input({ id: 'dn-hc-path', mono: true, width: 'md', value: isEdit ? (row.hc_path || '') : '', placeholder: '如 /healthz（以 / 开头）' }) +
        '<div class="form-hint">留空 = 不做探活，该节点将<b>始终被视为健康</b>——节点宕机后流量不会被自动摘除，将持续转发失败，建议生产环境配置探活路径（2xx/3xx 判健康）。</div></div>' +
        '</details>' +
        '<div class="form-row"><label class="form-label">备注</label>' +
        Rock.comp.form.textarea({ id: 'dn-remark', rows: 2, value: isEdit ? (row.remark || '') : '' }) + '</div>' +
        (isEdit
          ? '<div class="form-row"><label class="form-label">启用</label>' +
            Rock.comp.form.switch({ id: 'dn-enabled', checked: !!row.enabled }) +
            '<div class="form-hint">停用 = 不参与任何均衡器构建与探活。</div></div>' +
            (refs > 0 ? '<div class="form-hint">当前被 ' + fmtInt(refs) + ' 个均衡器的节点关系引用；删除需先到「负载均衡器」移除对应关系行。</div>' : '')
          : ''),
      footer: '<button class="btn" data-modal-act="cancel">取消</button>' +
        '<button class="btn btn-primary" data-act="dn-save">保存</button>',
    });

    overlay.addEventListener('click', async function (e) {
      if (!e.target.closest('[data-act="dn-save"]')) return;
      const body = {
        id: isEdit ? Number(row.id) : 0,
        name: ((overlay.querySelector('#dn-name') || {}).value || '').trim(),
        url: ((overlay.querySelector('#dn-url') || {}).value || '').trim(),
        hc_interval_ms: Math.floor(Number((overlay.querySelector('#dn-hc-interval') || {}).value) || 20000),
        hc_timeout_ms: Math.floor(Number((overlay.querySelector('#dn-hc-timeout') || {}).value) || 5000),
        hc_path: ((overlay.querySelector('#dn-hc-path') || {}).value || '').trim(),
        enabled: isEdit ? !!(overlay.querySelector('#dn-enabled') || {}).checked : true,
        remark: ((overlay.querySelector('#dn-remark') || {}).value || ''),
      };
      if (!/^https?:\/\//.test(body.url) || /\s/.test(body.url)) {
        toast('保存失败：地址必须为合法的 http(s)://host[:port] 地址（如 http://10.0.0.1:9001）。请修正后重试', 'error');
        markInvalid(overlay.querySelector('#dn-url')); return;
      }
      if (body.hc_path && body.hc_path.charAt(0) !== '/') {
        toast('保存失败：探活路径必须以 / 开头（如 /healthz）；留空 = 不探活、始终视为健康。请修正后重试', 'error');
        markInvalid(overlay.querySelector('#dn-hc-path')); return;
      }
      try {
        if (isEdit) await api.post(BASE + '/nodes/update')(body);
        else await api.post(BASE + '/nodes')(body);
        overlay.remove();
        toast(isEdit ? '节点已更新（快照已热更）' : '节点已登记（快照已热更）', 'success');
        await refreshCaches(true);
        await afterWriteRefresh('node');
      } catch (err) {
        toast('节点保存失败：' + (err.message || '未知错误') + '。请按提示修正后重试；数据未保存', 'error');
      }
    });
  }

  // ── actions 注册（main.js 统一委托）──────────────────────────────────

  window.Rock.views.dispatch = {
    load: load,
    render: render,
    actions: {
      'dispatch-view': function (el) { switchView(el.getAttribute('data-view') || 'rules'); },
      'dispatch-reload-all': function () { reloadAll(); },
      'dispatch-test-run': function () { runTest(); },
      'dispatch-rule-new': function () { openRuleForm(null); },
      'dispatch-rule-edit': function (el) {
        const row = lists.rules.rows.find(function (r) { return String(r.id) === el.getAttribute('data-id'); });
        if (row) openRuleForm(row);
      },
      'dispatch-rule-del': function (el) { del('rule', el.getAttribute('data-id')); },
      'dispatch-rule-restore': function (el) { restore('rule', el.getAttribute('data-id')); },
      'dispatch-up-new': function () { ensureCaches(true).then(function () { openUpstreamForm(null); }); },
      'dispatch-up-edit': function (el) {
        const row = lists.upstreams.rows.find(function (r) { return String(r.id) === el.getAttribute('data-id'); });
        if (row) ensureCaches(true).then(function () { openUpstreamForm(row); });
      },
      'dispatch-up-del': function (el) { del('up', el.getAttribute('data-id')); },
      'dispatch-up-restore': function (el) { restore('up', el.getAttribute('data-id')); },
      'dispatch-node-new': function () { openNodeForm(null); },
      'dispatch-node-edit': function (el) {
        const row = lists.nodes.rows.find(function (r) { return String(r.id) === el.getAttribute('data-id'); });
        if (row) openNodeForm(row);
      },
      'dispatch-node-del': function (el) { del('node', el.getAttribute('data-id')); },
      'dispatch-node-restore': function (el) { restore('node', el.getAttribute('data-id')); },
    },
  };
})();
