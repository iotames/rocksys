/* ==========================================================================
 * RockSys 管理控制台 - views/auth.js 认证视图
 * 登录 / 注册（初始化）/ 重置（忘记密码）三个全屏面板。
 * 启动时由 main.js 调用 Rock.auth.init() 检测管理接口认证状态并决定显示哪个面板。
 * 依赖 Rock.util / Rock.api / Rock.ui / Rock.main。
 * 挂载到全局命名空间 window.Rock.auth。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};

  const $ = Rock.util.$;
  const api = Rock.api;
  const ui = Rock.ui;
  const notify = Rock.ui.notify;

  // 切换显示指定认证面板
  function showPanel(name) {
    ['login', 'register', 'reset', 'degraded'].forEach(function (p) {
      const el = $('#auth-panel-' + p);
      if (el) el.classList.toggle('hidden', p !== name);
    });
    setError('');
  }

  // 显示/清除认证错误提示
  function setError(msg) {
    const el = $('#auth-error');
    if (!el) return;
    if (msg) { el.textContent = msg; el.classList.remove('hidden'); }
    else el.classList.add('hidden');
  }

  // 显示认证视图（全屏覆盖控制台）
  function showAuth() {
    $('#auth-view').classList.remove('hidden');
    document.body.classList.add('auth-mode');
  }

  // 进入控制台（隐藏认证视图）
  // 数据加载经 Rock.main.bootConsole 门控触发：首启时统一拉取全局基础数据并渲染当前页，
  // 保证登录前零业务请求（未登录/未初始化/数据层降级时不发任何 API）。
  function enterConsole() {
    $('#auth-view').classList.add('hidden');
    document.body.classList.remove('auth-mode');
    if (Rock.main && Rock.main.bootConsole) Rock.main.bootConsole();
  }

  // 启动引导：检测认证状态，决定显示注册/重置/登录面板或直接进入控制台
  function init() {
    let st = null;
    try {
      st = api.get('/admin/auth/status');
    } catch (e) { /* 同步异常忽略 */ }
    if (!st || typeof st.then !== 'function') {
      // 状态接口不可达：显示登录页（后续请求会提示错误）
      showAuth();
      showPanel('login');
      return;
    }
    st.then(function (s) {
      if (!s) { showAuth(); showPanel('login'); return; }
      // 回环免登录（127.0.0.1 且无静态 token）→ 直接进入控制台
      if (!s.auth_required) { enterConsole(); return; }
      // 数据层降级（数据库不可用）→ 降级引导页，杜绝误导性注册面板（账号操作后端也已 503 拦截）
      if (!s.has_user && s.store_ready === false) {
        showAuth();
        showPanel('degraded');
        const reason = $('#auth-degraded-reason');
        if (reason) reason.textContent = s.store_error || '数据库未配置或初始化失败';
        return;
      }
      // 全新系统 → 注册引导页
      if (!s.has_user) { showAuth(); showPanel('register'); return; }
      // 重置模式（运维已改 ADMIN_INITIALIZED=false）→ 重置页
      if (s.setup_mode) {
        showAuth();
        showPanel('reset');
        const hint = $('#auth-reset-user');
        if (hint) hint.textContent = s.username || '管理员';
        const input = $('#auth-reset-user-input');
        if (input && s.username) input.value = s.username;
        return;
      }
      // 已初始化：有 token 进控制台，否则登录
      if (api.getToken()) { enterConsole(); }
      else { showAuth(); showPanel('login'); }
    }).catch(function () {
      showAuth();
      showPanel('login');
    });
  }

  // 登录
  function login() {
    const user = $('#auth-login-user').value.trim();
    const pass = $('#auth-login-pass').value;
    if (!user || !pass) { setError('请输入用户名与密码'); return; }
    setError('');
    api.post('/admin/auth/login')({ username: user, password: pass })
      .then(function (r) {
        if (r && r.token) {
          api.setToken(r.token);
          // 后端 pruneWarnings：清理机制未开启的持久化膨胀提醒（全局常驻置顶横幅 + 登录即时 toast）
          const ws = (r && Array.isArray(r.warnings)) ? r.warnings.filter(function (w) { return !!w; }) : [];
          Rock.state.store.loginWarnings = ws.length ? ws : null;
          ws.forEach(function (w) { notify.warn(w); });
          if (Rock.main && Rock.main.renderPruneBanner) Rock.main.renderPruneBanner();
          notify.success('登录成功');
          enterConsole();
        } else {
          setError((r && r.error) || '登录失败');
        }
      })
      .catch(function (e) { const m = e.message || '登录失败'; setError(m); notify.error(m); });
  }

  // 首次注册（初始化管理员）
  function register() {
    const user = $('#auth-reg-user').value.trim();
    const pass = $('#auth-reg-pass').value;
    const pass2 = $('#auth-reg-pass2').value;
    if (!user || pass.length < 8) { setError('用户名不能为空，密码至少 8 位'); return; }
    if (pass !== pass2) { setError('两次输入的密码不一致'); return; }
    setError('');
    api.post('/admin/auth/register')({ username: user, password: pass })
      .then(function (r) {
        if (r && r.ok) {
          notify.success('初始化成功，请登录');
          showPanel('login');
        } else {
          setError((r && r.error) || '注册失败');
        }
      })
      .catch(function (e) { const m = e.message || '注册失败'; setError(m); notify.error(m); });
  }

  // 重置凭证（忘记密码）
  function reset() {
    const user = $('#auth-reset-user-input').value.trim();
    const pass = $('#auth-reset-pass').value;
    const pass2 = $('#auth-reset-pass2').value;
    if (!user || pass.length < 8) { setError('用户名不能为空，密码至少 8 位'); return; }
    if (pass !== pass2) { setError('两次输入的密码不一致'); return; }
    setError('');
    api.post('/admin/auth/reset')({ username: user, password: pass })
      .then(function (r) {
        if (r && r.ok) {
          notify.success('重置成功，请登录');
          showPanel('login');
        } else {
          setError((r && r.error) || '重置失败');
        }
      })
      .catch(function (e) { const m = e.message || '重置失败'; setError(m); notify.error(m); });
  }

  // 绑定事件
  function bind() {
    $('#auth-login-btn').addEventListener('click', login);
    $('#auth-reg-btn').addEventListener('click', register);
    $('#auth-reset-btn').addEventListener('click', reset);
    // Enter 键提交（用户名 / 确认密码框均可直接回车）
    ['auth-login-user', 'auth-login-pass'].forEach(function (id) {
      const el = $('#' + id);
      if (el) el.addEventListener('keydown', function (e) { if (e.key === 'Enter') login(); });
    });
    ['auth-reg-user', 'auth-reg-pass2'].forEach(function (id) {
      const el = $('#' + id);
      if (el) el.addEventListener('keydown', function (e) { if (e.key === 'Enter') register(); });
    });
    ['auth-reset-user-input', 'auth-reset-pass2'].forEach(function (id) {
      const el = $('#' + id);
      if (el) el.addEventListener('keydown', function (e) { if (e.key === 'Enter') reset(); });
    });
  }

  window.Rock.auth = {
    init,
    showAuth,
    enterConsole,
    showPanel,
    setError,
    login,
    register,
    reset,
    bind,
  };
})();