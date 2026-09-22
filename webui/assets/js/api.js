/* ==========================================================================
 * RockSys 管理控制台 - api.js API 客户端
 * Token 存取、fetch 封装（get/post/put/text）、默认 15 秒超时（可按请求放宽）、401 处理、
 * 503（观测未开启）识别、错误消息提取、超时（408）与网络不可达（0）区分标记。
 * 统一加载指示：请求期间顶部显示轻量指示条（默认文案"数据请求中…"，调用方可按请求覆写）。
 * UI 状态反馈经 setUiBridge 由入口注入，API 客户端本身不依赖任何视图。
 * 挂载到全局命名空间 window.Rock.api。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};

  const TOKEN_KEY = 'rocksys.admin_token';
  const DEFAULT_TIMEOUT_MS = 15000;
  const DEFAULT_LOADING_TEXT = '数据请求中…';

  // UI 桥接（入口注入）：markUnreachable(v) / onUnauthorized()，可空
  let uiBridge = null;
  function setUiBridge(b) { uiBridge = b; }
  function bridgeUnreachable(v) {
    if (uiBridge && uiBridge.markUnreachable) uiBridge.markUnreachable(v);
  }
  function bridgeUnauthorized() {
    if (uiBridge && uiBridge.onUnauthorized) uiBridge.onUnauthorized();
  }

  // ---- 统一加载指示条（中性态，非 toast：成功/失败提示仍由调用方经 Rock.ui.notify 触发）----
  // 顶部固定细进度条 + 文案。并行请求计数：全部结束才隐藏；文案显示最近一次请求的覆写值。
  let loadingCount = 0;
  let loadingHost = null;
  let loadingBar = null;
  let loadingLabel = null;

  function ensureLoadingHost() {
    if (loadingHost) return loadingHost;
    const style = document.createElement('style');
    style.textContent = [
      '#rock-api-loading{position:fixed;top:0;left:0;right:0;z-index:9999;pointer-events:none;',
      '  opacity:0;transition:opacity .15s;}',
      '#rock-api-loading.active{opacity:1;}',
      '#rock-api-loading .rock-loading-bar{height:2px;background:#4a7dff;',
      '  animation:rock-loading-slide 1.1s ease-in-out infinite;}',
      '@keyframes rock-loading-slide{0%{transform:translateX(-100%);}100%{transform:translateX(100%);}}',
      '#rock-api-loading .rock-loading-text{margin-top:4px;text-align:center;font-size:12px;',
      '  color:#888;user-select:none;}',
    ].join('');
    document.head.appendChild(style);
    loadingHost = document.createElement('div');
    loadingHost.id = 'rock-api-loading';
    loadingBar = document.createElement('div');
    loadingBar.className = 'rock-loading-bar';
    loadingLabel = document.createElement('div');
    loadingLabel.className = 'rock-loading-text';
    loadingHost.appendChild(loadingBar);
    loadingHost.appendChild(loadingLabel);
    document.body.appendChild(loadingHost);
    return loadingHost;
  }

  function loadingStart(text) {
    const host = ensureLoadingHost();
    loadingCount++;
    loadingLabel.textContent = text || DEFAULT_LOADING_TEXT;
    host.classList.add('active');
  }

  function loadingEnd() {
    if (!loadingHost) return;
    loadingCount = Math.max(0, loadingCount - 1);
    if (loadingCount === 0) loadingHost.classList.remove('active');
  }

  function getToken() {
    try { return localStorage.getItem(TOKEN_KEY) || ''; } catch (e) { return ''; }
  }

  function setToken(t) {
    try {
      if (t) localStorage.setItem(TOKEN_KEY, t);
      else localStorage.removeItem(TOKEN_KEY);
    } catch (e) { /* 忽略存储异常 */ }
  }

  class ApiError extends Error {
    constructor(message, status, opts) {
      super(message);
      this.name = 'ApiError';
      this.status = status || 0;
      this.obsDisabled = !!(opts && opts.obsDisabled);
    }
  }

  // 请求核心：统一携带 Token、超时（默认 15 秒，慢接口可经 timeoutMs 放宽）、
  // 401 / 503 / 网络错误处理、加载指示（loadingText 可覆写默认文案）。
  function request(method, url, body, timeoutMs, loadingText) {
    const headers = {};
    const token = getToken();
    if (token) headers['Authorization'] = 'Bearer ' + token;
    let payload;
    if (body !== undefined && body !== null) {
      headers['Content-Type'] = 'application/json';
      payload = JSON.stringify(body);
    }
    const effectiveTimeout = timeoutMs || DEFAULT_TIMEOUT_MS;
    loadingStart(loadingText);
    let res;
    try {
      res = fetch(url, { method, headers, body: payload, cache: 'no-store', signal: AbortSignal.timeout(effectiveTimeout) });
    } catch (e) {
      loadingEnd();
      // 同步抛错（极少数情况）
      bridgeUnreachable(true);
      return Promise.reject(new ApiError('请求发起失败', 0));
    }
    return res.then(async r => {
      if (r.status === 401) {
        // 凭证失效：优先取后端错误信息（如登录密码错误），否则通用提示
        let msg = '未授权：登录已失效，请重新登录';
        try { const j = await r.json(); if (j && j.error) msg = j.error; } catch (e) { /* 非 JSON 响应 */ }
        bridgeUnauthorized();
        throw new ApiError(msg, 401);
      }
      if (r.status === 503 && (url.indexOf('/admin/metrics') === 0 || url.indexOf('/admin/logs') === 0)) {
        // 观测未注册：交给页面展示引导
        bridgeUnreachable(false);
        throw new ApiError('观测未开启', 503, { obsDisabled: true });
      }
      if (!r.ok) {
        // 服务端有响应即视为可达（含 5xx），不触发"接口不可达"横幅；错误文案交给 toast 展示
        // body 只读一次，文案决策链：JSON 的 error/message → 短纯文本原样 → 固定兜底提示。
        // HTML 错误页/超长文本不灌进提示条（无可读性），统一指向服务端日志。
        const MAX_RAW_LEN = 200;
        let msg = '';
        try {
          const t = await r.text();
          try {
            const j = JSON.parse(t);
            msg = j.error || j.message || '';
            if (!msg) msg = '服务端响应格式异常（HTTP ' + r.status + '）';
          } catch (e) {
            if (t.length <= MAX_RAW_LEN && !t.includes('<')) msg = t;
          }
        } catch (e) { /* body 不可读，走兜底 */ }
        if (!msg) msg = '服务端响应异常（HTTP ' + r.status + '），请查看服务端日志';
        throw new ApiError(msg, r.status);
      }
      bridgeUnreachable(false);
      return r;
    }).catch(err => {
      if (err && err.name === 'TimeoutError') {
        // 超时 ≠ 不可达：服务端已响应、只是在等慢查询（如大库精确统计），置不可达会误报为服务故障。
        // 用独立状态 408（而非 0）承载超时：调用方普遍以 `status !== 0` 判定「服务端有响应、
        // 失败应弹统一 error toast」，401/503 之外的非 2xx 同样走该分支——超时因此能正常提示，
        // 且不调用 bridgeUnreachable，顶栏可达性状态不受影响。
        throw new ApiError('请求超时（' + Math.round(effectiveTimeout / 1000) + ' 秒），可缩小范围或稍后重试', 408);
      }
      if (err instanceof TypeError) {
        // fetch 网络层失败（连接拒绝 / 无法解析等）
        bridgeUnreachable(true);
        throw new ApiError('管理接口不可达', 0);
      }
      throw err;
    }).finally(loadingEnd);
  }

  const api = {
    get: (url, timeoutMs, loadingText) => request('GET', url, undefined, timeoutMs, loadingText).then(r => r.json().catch(() => null)),
    // textMeta：NDJSON 文本 + X-Total-Count 响应头总数（服务端分页端点用）
    textMeta: (url, timeoutMs, loadingText) => request('GET', url, undefined, timeoutMs, loadingText).then(async r => ({
      text: await r.text(),
      total: Number(r.headers.get('X-Total-Count')) || 0,
    })),
    put: url => body => request('PUT', url, body).then(r => r.json().catch(() => ({}))),
    post: (url, timeoutMs, loadingText) => body => request('POST', url, body, timeoutMs, loadingText).then(r => r.json().catch(() => ({}))),
    text: (url, timeoutMs, loadingText) => request('GET', url, undefined, timeoutMs, loadingText).then(r => r.text()),
  };

  window.Rock.api = {
    TOKEN_KEY,
    getToken,
    setToken,
    ApiError,
    request,
    setUiBridge,
    get: api.get,
    put: api.put,
    post: api.post,
    text: api.text,
    textMeta: api.textMeta,
  };
})();
