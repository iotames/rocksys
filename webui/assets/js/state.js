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
    logs: [],                      // 日志行
    logsLoaded: false,
    logsError: null,
    unreachable: false,            // 网关是否不可达
    lastUpdated: null,             // 最近一次成功更新时间戳
    // 各页"首次加载失败"标志（无缓存数据时展示错误态，避免永久骨架屏）
    overviewFailed: false,
    componentsFailed: false,
    configFailed: false,
    metricsFailed: false,
  };

  // 组件元数据（名称 → 中文名 / 说明 / 环节）
  const COMPONENT_META = {
    shield:   { title: '防护',     desc: '黑白名单 / 限流 / WAF 检测',                slot: 'Head',   slotLabel: '入口环节' },
    trace:    { title: '透传',     desc: '链路标识透传：trace_id 注入请求与响应头',    slot: 'Head',   slotLabel: '入口环节' },
    auth:     { title: '认证',     desc: 'JWT 身份认证',                              slot: 'Head',   slotLabel: '入口环节' },
    dispatch: { title: '分发',     desc: '按 URI 路由分发到不同后端',                 slot: 'Middle', slotLabel: '分发环节' },
    rewrite:  { title: '改写',     desc: '转发前改写 URI 前缀与请求头',               slot: 'Middle', slotLabel: '分发环节' },
    script:   { title: '脚本',     desc: 'Lua 策略引擎：安全规则 / 路由改写 / 分流',   slot: 'Middle', slotLabel: '分发环节' },
    obs:      { title: '观测',     desc: '访问日志 + 指标聚合',                       slot: 'Tail',   slotLabel: '响应环节' },
    copy:     { title: '抄送',     desc: '请求影子抄送（流量审计）',                  slot: 'Tail',   slotLabel: '响应环节' },
    result:   { title: '结果',     desc: '统一出口格式 / 字段脱敏',                   slot: 'Tail',   slotLabel: '响应环节' },
    config:   { title: '配置服务', desc: 'KV 配置服务，集中下发与热更新广播',          kind: 'component' },
    registry: { title: '注册',     desc: '服务注册与发现',                            kind: 'component' },
    object:   { title: '存储',     desc: '对象存储（本地磁盘 / S3 兼容）',            kind: 'component' },
    mq:       { title: '消息',     desc: '异步消息解耦（Outbox 模式）',               kind: 'component' },
  };

  // 组件展示顺序（mq 按配置装配，可能缺席）
  const COMPONENT_ORDER = ['shield', 'trace', 'auth', 'dispatch', 'rewrite', 'script', 'obs', 'copy', 'result', 'config', 'registry', 'object', 'mq'];

  // 配置分组映射（key 前缀 → 分组）
  const PREFIX_GROUPS = [
    { prefix: 'ROCKSYS_', name: 'gateway', label: '网关' },
    { prefix: 'SHIELD_',  name: 'shield',  label: '防护' },
    { prefix: 'DISPATCH_', name: 'dispatch', label: '分发' },
    { prefix: 'REWRITE_', name: 'rewrite', label: '改写' },
    { prefix: 'OBS_',     name: 'obs',     label: '观测' },
    { prefix: 'COPY_',    name: 'copy',    label: '抄送' },
    { prefix: 'RESULT_',  name: 'result',  label: '结果' },
    { prefix: 'AUTH_',    name: 'auth',    label: '认证' },
    { prefix: 'MQ_',      name: 'mq',      label: '消息' },
    { prefix: 'DB_',      name: 'db',      label: '数据访问' },
    // SQL_DIR 特判归入 db 组（见 groupOf），避免同组重复注册产生重复页签
  ];

  // 枚举值配置项（编辑态渲染下拉而非手填）：key → 可选值数组（首个为默认/推荐）
  const ENUM_KEYS = {
    OBS_STORE: ['db', 'file'], // db 默认；file 已弃用，将不再被支持
  };

  // 需重启才生效的配置项
  const RESTART_KEYS = ['ROCKSYS_LISTEN', 'ROCKSYS_ADMIN', 'ROCKSYS_CONFIG'];

  // 敏感配置（默认掩码）
  function isSensitiveKey(k) { return /SECRET|TOKEN|PASSWORD/i.test(k); }

  // 组件名 → 配置前缀（用于组件卡片展开配置区）
  const COMPONENT_PREFIX = {
    shield: 'SHIELD_', dispatch: 'DISPATCH_', rewrite: 'REWRITE_',
    obs: 'OBS_', copy: 'COPY_', result: 'RESULT_', auth: 'AUTH_', mq: 'MQ_',
  };

  // 配置分组归属（SQL_DIR 特判归入数据访问组）
  function groupOf(key) {
    if (key === 'SQL_DIR') return { prefix: 'DB_', name: 'db', label: '数据访问' };
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
    COMPONENT_META,
    COMPONENT_ORDER,
    PREFIX_GROUPS,
    ENUM_KEYS,
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
