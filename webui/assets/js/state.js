/* ==========================================================================
 * RockSys 管理控制台 - state.js 全局状态与常量
 * 纯数据模块：store、组件中文名映射、环节标签、配置 key 分组规则、
 * 敏感/需重启规则、数据标准化函数。不依赖其他模块。
 * 挂载到全局命名空间 window.Rock.state。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};

  // 全局状态
  const store = {
    base: null,                    // GET /admin/config 底座信息
    baseLoaded: false,
    switches: [],                  // GET /admin/switch/list 组件状态
    switchesLoaded: false,
    configList: [],                // GET /admin/config/list 全量配置项
    configListLoaded: false,
    configUnavailable: false,      // 配置接口不可用时置 true（容错）
    metrics: null,                 // GET /admin/metrics 最新指标
    metricsError: null,            // 'obs' = 观测未开启
    metricsHistory: [],            // 趋势采样历史 [{t, qps, p50, p95, p99, err}]
    wafMetrics: null,              // GET /admin/shield/metrics 近 1 分钟拦截计数（内存窗口）
    wafMetricsError: null,
    wafStats: null,                // GET /admin/shield/stats 按日聚合 + Top IP
    wafStatsError: null,
    wafEvents: [],                 // 拦截明细行（/admin/shield/events JSONL）
    wafEventsError: null,
    wafLoaded: false,              // WAF安全防护页是否已加载（懒加载缓存判定）
    meta: { components: [], services: [] }, // GET /admin/meta 组件/服务元数据（全局获取，不做持久化缓存）
    metaLoaded: false,
    loginWarnings: null,           // 登录响应 warnings（prune 未开启等持久化膨胀提醒）
    logs: [],                      // 入网数据日志行（obs /admin/logs）
    logsLoaded: false,
    logsError: null,
    syslogInfo: null,              // GET /admin/log/info 系统日志状态
    syslogInfoError: null,         // 'obs' = 观测未开启；其余为错误消息
    syslogPageVisible: false,      // 系统日志页是否当前可见（控制 SSE 生命周期）
    unreachable: false,            // 网关是否不可达
    lastUpdated: null,             // 最近一次成功更新时间戳
    // 各页"首次加载失败"标志（无缓存数据时展示错误态，避免永久骨架屏）
    overviewFailed: false,
    componentsFailed: false,
    configFailed: false,
    metricsFailed: false,
  };

  // 组件/服务元数据来自后端 GET /admin/meta（权威源 internal/catalog），前端不再硬编码。
  // ensureMeta 全局获取一次（页面会话内持有，不做持久化缓存）；componentMeta 按名取用。
  async function ensureMeta() {
    if (store.metaLoaded) return;
    try {
      const r = await Rock.api.get('/admin/meta');
      store.meta.components = (r && r.components) || [];
      store.meta.services = (r && r.services) || [];
      store.metaLoaded = true;
    } catch (e) { /* 接口不可达：保持空元数据，页面回退英文名 */ }
  }

  // 按名称取组件/服务元数据；kind：'service'=独立服务，其余（'middleware' 等）=链中间件。
  // 未命中时回退：title=英文名、desc 空、slotLabel 按类型给默认环节。
  function componentMeta(name, kind) {
    const isService = kind === 'service';
    const list = isService ? store.meta.services : store.meta.components;
    const found = (list || []).find(function (x) { return x.name === name; });
    if (found) return found;
    return {
      title: name,
      desc: '',
      slot: '',
      slotLabel: isService ? '独立服务' : '链中间件',
    };
  }

  // 数据流组件（链中间件）展示顺序：与 HTTP_DATAFLOW.md 链路顺序一致
  const COMPONENT_ORDER = ['shield', 'trace', 'auth', 'dispatch', 'rewrite', 'script', 'obs', 'copy', 'result'];

  // 独立服务（数据流无关，服务菜单）展示顺序
  const SERVICE_ORDER = ['config', 'registry', 'object', 'mq'];

  // WAF 拦截类别（与 plugins/shield/block_type.go 数值稳定约定一致）
  const BLOCK_TYPES = [
    [1, 'IP黑名单'], [2, '限流'], [3, '方法不允许'], [4, '请求体超限'], [5, '风险路径'],
    [6, '路径遍历'], [7, 'SQL注入'], [8, 'XSS'], [9, '爬虫UA'], [10, '路径规则'],
  ];

  // 类别编号 → 中文名（未命中回退原值）
  function blockTypeName(bt) {
    bt = Number(bt) || 0;
    for (let i = 0; i < BLOCK_TYPES.length; i++) {
      if (BLOCK_TYPES[i][0] === bt) return BLOCK_TYPES[i][1];
    }
    return '未知';
  }

  // 配置分组映射（key 前缀 → 分组）：全局配置页仅保留基础设施分组（网关 / 数据访问），
  // 组件与服务的独有配置项已迁至各自页面（配置页签），经 COMPONENT_PREFIX 过滤取用
  const PREFIX_GROUPS = [
    { prefix: 'ROCKSYS_', name: 'gateway', label: '网关' },
    { prefix: 'DB_',      name: 'db',      label: '数据访问' },
  ];

  // 枚举值配置项（编辑态渲染下拉而非手填）：key → 可选值数组（首个为默认/推荐）
  const ENUM_KEYS = {
    DB_DRIVER: ['sqlite', 'mysql', 'postgres'],
    OBS_STORE: ['db', 'file'], // db 默认；file 已弃用，将不再被支持
    ROCKSYS_LOG_LEVEL: ['debug', 'info', 'warn', 'error'],
    SHIELD_RATE_LIMIT_BY: ['ip'], // 当前仅支持 ip
  };

  // 布尔配置项（编辑态渲染 switch 开关）：*_ENABLED 挂载开关按命名约定识别，
  // 其余显式列于 BOOL_KEY_EXTRA（与后端 Register 的 bool 默认值一一对应）
  const BOOL_KEY_EXTRA = [
    'RESULT_WRAP', 'ROCKSYS_LOG_TO_FILE', 'DB_ENABLE', 'OBS_LOG_PRUNE_ENABLED',
    'SHIELD_EVENT_LOG_ENABLED', 'SHIELD_EVENT_PRUNE_ENABLED',
  ];
  function isBoolKey(k) { return /_ENABLED$/.test(k) || BOOL_KEY_EXTRA.indexOf(k) >= 0; }

  // 整数配置项（编辑态渲染 number 输入 + 非负整数前端校验）
  const INT_KEYS = [
    'ROCKSYS_TIMEOUT', 'ROCKSYS_LOG_MAX_SIZE', 'DB_PORT', 'AUTH_JWT_TTL',
    'SHIELD_RATE_LIMIT_RPS', 'SHIELD_RATE_LIMIT_BURST', 'SHIELD_MAX_BODY_SIZE',
    'OBS_RETENTION_DAYS', 'OBS_LOG_RETENTION_DAYS',
    'SHIELD_EVENT_BUFFER', 'SHIELD_EVENT_FLUSH_ROWS', 'SHIELD_EVENT_FLUSH_INTERVAL',
    'SHIELD_EVENT_RETENTION_DAYS', 'REGISTRY_TTL',
    'MQ_MAX_RETRIES', 'MQ_BASE_BACKOFF', 'MQ_POLL_INTERVAL',
  ];
  function isIntKey(k) { return INT_KEYS.indexOf(k) >= 0; }

  // 长文本配置项（编辑态渲染多行 textarea）：规则 / 目标列表 / DSN / 逗号分隔清单
  const TEXTAREA_KEYS = [
    'DISPATCH_RULES', 'REWRITE_RULES', 'COPY_TARGETS', 'DB_DSN',
    'RESULT_MASK_FIELDS', 'SHIELD_IP_WHITELIST', 'SHIELD_WAF_RISK_PATHS', 'SHIELD_ALLOW_METHODS',
  ];
  function isTextareaKey(k) { return TEXTAREA_KEYS.indexOf(k) >= 0; }

  // 需重启才生效的配置项
  const RESTART_KEYS = ['ROCKSYS_LISTEN', 'ROCKSYS_ADMIN', 'ROCKSYS_CONFIG'];

  // 敏感配置（默认掩码）
  function isSensitiveKey(k) { return /SECRET|TOKEN|PASSWORD/i.test(k); }

  // 组件/服务名 → 配置前缀（用于组件页配置页签过滤；无前缀者无独立配置项，如 trace/config）
  const COMPONENT_PREFIX = {
    shield: 'SHIELD_', trace: 'TRACE_', dispatch: 'DISPATCH_', rewrite: 'REWRITE_',
    obs: 'OBS_', copy: 'COPY_', result: 'RESULT_', auth: 'AUTH_', script: 'SCRIPT_',
    registry: 'REGISTRY_', object: 'OBJECT_', mq: 'MQ_',
  };

  // 配置分组归属（按前缀匹配；未匹配落"其他"组）
  function groupOf(key) {
    for (let i = 0; i < PREFIX_GROUPS.length; i++) {
      if (key.indexOf(PREFIX_GROUPS[i].prefix) === 0) return PREFIX_GROUPS[i];
    }
    return { prefix: '', name: 'other', label: '其他' };
  }

  // 数据标准化（容错：字段缺失时给出默认值）
  function normalizeSwitches(arr) {
    if (!Array.isArray(arr)) return [];
    return arr.map(s => ({
      name: String(s.name || ''),
      kind: s.kind === 'component' ? 'component' : 'middleware',
      state: ['enabled', 'disabled', 'draining'].indexOf(s.state) >= 0 ? s.state : 'disabled',
      started_at: s.started_at || s.startedAt || '',
      last_switch_at: s.last_switch_at || s.lastSwitchAt || '',
      message: s.message || '',
    })).filter(s => s.name);
  }

  function normalizeConfigList(arr) {
    if (!Array.isArray(arr)) return [];
    return arr.map(c => ({
      key: String(c.key || ''),
      title: String(c.title || ''),
      defval: c.defval == null ? '' : String(c.defval),
      current: c.current == null ? '' : String(c.current),
      example: c.example == null ? '' : String(c.example),
    })).filter(c => c.key);
  }

  function normalizeMetrics(m) {
    if (!m) return null;
    return {
      qps: Number(m.qps) || 0,
      p50_ms: Number(m.p50_ms) || 0,
      p95_ms: Number(m.p95_ms) || 0,
      p99_ms: Number(m.p99_ms) || 0,
      error_rate: Number(m.error_rate) || 0,
    };
  }

  // 错误率（后端为比例，如 0.1 = 10%）
  function fmtRate(v) {
    v = Number(v) || 0;
    return (v * 100).toFixed(2) + '%';
  }

  window.Rock.state = {
    store,
    ensureMeta,
    componentMeta,
    COMPONENT_ORDER,
    SERVICE_ORDER,
    BLOCK_TYPES,
    blockTypeName,
    PREFIX_GROUPS,
    ENUM_KEYS,
    BOOL_KEY_EXTRA,
    isBoolKey,
    INT_KEYS,
    isIntKey,
    TEXTAREA_KEYS,
    isTextareaKey,
    RESTART_KEYS,
    isSensitiveKey,
    COMPONENT_PREFIX,
    groupOf,
    normalizeSwitches,
    normalizeConfigList,
    normalizeMetrics,
    fmtRate,
  };
})();
