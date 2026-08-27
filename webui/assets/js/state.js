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

  // 组件元数据（名称 → 中文名 / 说明 / 环节）
  const COMPONENT_META = {
    shield:   { title: '防护',     desc: '入口安全防护：按 IP 黑白名单、WAF 规则与限流拦截请求；命中返回 403/429 并中断链路，未命中放行。', slot: 'Head', slotLabel: '入口环节' },
    trace:    { title: '透传',     desc: '链路追踪：将请求的 trace_id 写入响应头 X-Trace-Id，便于串联全链路日志（trace_id 由入口自动生成）。', slot: 'Head', slotLabel: '入口环节' },
    auth:     { title: '认证',     desc: 'JWT 鉴权：校验 Authorization 中的令牌（签名与有效期），合法放行并识别租户，非法返回 401。', slot: 'Head', slotLabel: '入口环节' },
    dispatch: { title: '分发',     desc: '路由决策：按 URL 规则选出目标后端并写入转发信息，实际转发由转发引擎执行；未命中路由规则则进入 ROCKSYS_UPSTREAM 默认后端，命中但节点不可用返回 503。', slot: 'Middle', slotLabel: '分发环节' },
    rewrite:  { title: '改写',     desc: '转发前改写：按规则调整请求的 URI 前缀或注入请求头，随后由转发引擎转发。', slot: 'Middle', slotLabel: '分发环节' },
    script:   { title: '脚本',     desc: 'Lua 策略引擎：执行自定义脚本（单脚本限时 100ms），可改写目标/请求/响应，也可直接返回响应终止转发。', slot: 'Middle', slotLabel: '分发环节' },
    obs:      { title: '观测',     desc: '请求观测：记录访问日志（含分环节耗时）并聚合 QPS/延迟/错误率等指标，供概览与日志页查看。', slot: 'Tail', slotLabel: '响应环节' },
    copy:     { title: '抄送',     desc: '流量影子：转发完成后异步复制请求（不含请求体）到影子后端，不改写响应、不阻塞主链，失败仅告警。', slot: 'Tail', slotLabel: '响应环节' },
    result:   { title: '结果',     desc: '出口加工：按规则对 JSON 响应脱敏或封装为统一格式（Envelope）；非 JSON 响应原样透传。', slot: 'Tail', slotLabel: '响应环节' },
    config:   { title: '配置服务', desc: 'KV 配置服务：集中读写配置（默认本地文件），变更支持订阅广播，供各组件与服务使用。', kind: 'component' },
    registry: { title: '注册',     desc: '服务注册与发现：实例注册、心跳续约、超时自动摘除，实例变更自动同步到分发（dispatch）路由。', kind: 'component' },
    object:   { title: '存储',     desc: '本地对象存储：对象读写存储于本地磁盘（含路径穿越防护）。', kind: 'component' },
    mq:       { title: '消息',     desc: '异步消息可靠投递：Outbox 模式（业务事务与消息同写）＋轮询投递，失败自动重试、超限转死信，不依赖独立 MQ。', kind: 'component' },
  };

  // 数据流组件（链中间件）展示顺序：与 HTTP_DATAFLOW.md 链路顺序一致
  const COMPONENT_ORDER = ['shield', 'trace', 'auth', 'dispatch', 'rewrite', 'script', 'obs', 'copy', 'result'];

  // 独立服务（数据流无关，服务菜单）展示顺序
  const SERVICE_ORDER = ['config', 'registry', 'object', 'mq'];

  // 配置分组映射（key 前缀 → 分组）：全局配置页仅保留基础设施分组（网关 / 数据访问），
  // 组件与服务的独有配置项已迁至各自页面（配置页签），经 COMPONENT_PREFIX 过滤取用
  const PREFIX_GROUPS = [
    { prefix: 'ROCKSYS_', name: 'gateway', label: '网关' },
    { prefix: 'DB_',      name: 'db',      label: '数据访问' },
  ];

  // 枚举值配置项（编辑态渲染下拉而非手填）：key → 可选值数组（首个为默认/推荐）
  const ENUM_KEYS = {
    OBS_STORE: ['db', 'file'], // db 默认；file 已弃用，将不再被支持
  };

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
    COMPONENT_META,
    COMPONENT_ORDER,
    SERVICE_ORDER,
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
