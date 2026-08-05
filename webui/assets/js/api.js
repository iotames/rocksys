/* ==========================================================================
 * RockSys 管理控制台 - api.js API 客户端
 * Token 存取、fetch 封装（get/post/put/text）、约 5 秒超时、401 处理、
 * 503（观测未开启）识别、错误消息提取、网络不可达标记。
 * 依赖 Rock.ui（运行时访问 markUnreachable / onUnauthorized）。
 * 挂载到全局命名空间 window.Rock.api。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};

  const TOKEN_KEY = 'rocksys.admin_token';

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

  // 请求核心：统一携带 Token、超时（约 5 秒）、401 / 503 / 网络错误处理
  function request(method, url, body) {
    const headers = {};
    const token = getToken();
    if (token) headers['Authorization'] = 'Bearer ' + token;
    let payload;
    if (body !== undefined && body !== null) {
      headers['Content-Type'] = 'application/json';
      payload = JSON.stringify(body);
    }
    let res;
    try {
      res = fetch(url, { method, headers, body: payload, cache: 'no-store', signal: AbortSignal.timeout(5000) });
    } catch (e) {
      // 同步抛错（极少数情况）
      Rock.ui.markUnreachable(true);
      return Promise.reject(new ApiError('请求发起失败', 0));
    }
    return res.then(async r => {
      if (r.status === 401) {
        // 凭证失效：优先取后端错误信息（如登录密码错误），否则通用提示
        let msg = '未授权：访问凭证无效或已过期，请重新输入';
        try { const j = await r.json(); if (j && j.error) msg = j.error; } catch (e) { /* 非 JSON 响应 */ }
        Rock.ui.onUnauthorized();
        throw new ApiError(msg, 401);
      }
      if (r.status === 503 && (url.indexOf('/admin/metrics') === 0 || url.indexOf('/admin/logs') === 0)) {
        // 观测未注册：交给页面展示引导
        Rock.ui.markUnreachable(false);
        throw new ApiError('观测未开启', 503, { obsDisabled: true });
      }
      if (!r.ok) {
        if (r.status >= 500) Rock.ui.markUnreachable(true);
        let msg = '';
        try {
          const j = await r.json();
          msg = j.error || j.message || '';
        } catch (e) { /* 非 JSON 响应 */ }
        if (!msg) { try { msg = (await r.text()).slice(0, 200); } catch (e) { /* ignore */ } }
        throw new ApiError(msg || ('HTTP ' + r.status), r.status);
      }
      Rock.ui.markUnreachable(false);
      return r;
    }).catch(err => {
      if (err && err.name === 'TimeoutError') {
        Rock.ui.markUnreachable(true);
        throw new ApiError('请求超时（5 秒）', 0);
      }
      if (err instanceof TypeError) {
        // fetch 网络层失败（连接拒绝 / 无法解析等）
        Rock.ui.markUnreachable(true);
        throw new ApiError('管理接口不可达', 0);
      }
      throw err;
    });
  }

  const api = {
    get: url => request('GET', url).then(r => r.json().catch(() => null)),
    put: url => body => request('PUT', url, body).then(r => r.json().catch(() => ({}))),
    post: url => body => request('POST', url, body).then(r => r.json().catch(() => ({}))),
    text: url => request('GET', url).then(r => r.text()),
  };

  window.Rock.api = {
    TOKEN_KEY,
    getToken,
    setToken,
    ApiError,
    request,
    get: api.get,
    put: api.put,
    post: api.post,
    text: api.text,
  };
})();
