/* ==========================================================================
 * RockSys 管理控制台 - ui.js UI 基础设施
 * Toast、二次确认弹窗、通用模态、骨架屏、状态色点、网关可达性横幅。
 * 依赖 Rock.util / Rock.state（运行时访问）。
 * 挂载到全局命名空间 window.Rock.ui。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const fmtTime = Rock.util.fmtTime;
  const fmtDateTime = Rock.util.fmtDateTime;

  // 右上角消息提示（唯一提示组件，全站统一走这里，禁止再造轮子）：
  // - success / info：操作正常反馈，显示完自动消失（默认 3.2s，点击也可关闭）；
  // - error / warning：异常信息，不自动消失——需点右上角 ✕ 或「知道了」按钮关闭；
  //   传显式 duration 时仍自动消失（如登录页警告 6s）。
  // 切换页面经 clearToasts() 清空（刷新页面天然清空），不让过期提示跨页残留。
  function toast(message, type, duration) {
    type = type || 'success';
    const sticky = (type === 'error' || type === 'warning') && duration == null;
    const root = $('#toast-root');
    if (!root) return;
    const el = document.createElement('div');
    el.className = 'toast toast-' + type + (sticky ? ' toast-sticky' : '');
    const icons = { success: '✓', error: '✕', warning: '⚠', info: 'ℹ' };
    el.innerHTML =
      '<div class="toast-head"><span class="toast-icon">' + (icons[type] || 'ℹ') + '</span>' +
      '<span class="toast-msg"></span>' +
      (sticky ? '<button class="toast-x" data-toast-act="close" title="关闭">✕</button>' : '') +
      '</div>' +
      (sticky ? '<div class="toast-foot"><button class="btn btn-sm" data-toast-act="close">知道了</button></div>' : '');
    el.querySelector('.toast-msg').textContent = message;
    root.appendChild(el);
    requestAnimationFrame(() => el.classList.add('show'));
    const close = () => {
      if (!el.isConnected) return;
      el.classList.remove('show');
      setTimeout(() => el.remove(), 260);
    };
    el.addEventListener('click', e => {
      // 常驻提示仅 ✕ /「知道了」可关闭，避免误点正文丢失信息
      if (!sticky || e.target.closest('[data-toast-act="close"]')) close();
    });
    if (!sticky) setTimeout(close, duration == null ? 3200 : duration);
  }

  // 清空全部提示（路由切换时调用；刷新页面天然清空）
  function clearToasts() {
    const root = $('#toast-root');
    if (root) root.innerHTML = '';
  }

  // 二次确认弹窗（危险操作为红色按钮）
  function confirmDialog(opts) {
    return new Promise(resolve => {
      const title = opts.title || '操作确认';
      const confirmText = opts.confirmText || '确认';
      const cancelText = opts.cancelText || '取消';
      const danger = !!opts.danger;
      const width = opts.width || 440;
      const root = $('#modal-root');
      const overlay = document.createElement('div');
      overlay.className = 'modal-overlay';
      overlay.innerHTML =
        '<div class="modal" style="width:' + width + 'px">' +
        '<div class="modal-header"><span class="modal-title">' + esc(title) + '</span>' +
        '<button class="modal-x" data-modal-act="cancel">✕</button></div>' +
        '<div class="modal-body">' + (opts.message || '') + '</div>' +
        '<div class="modal-footer">' +
        '<button class="btn" data-modal-act="cancel">' + esc(cancelText) + '</button>' +
        '<button class="btn ' + (danger ? 'btn-danger' : 'btn-primary') + '" data-modal-act="ok">' + esc(confirmText) + '</button>' +
        '</div></div>';
      root.appendChild(overlay);
      let done = false;
      const close = val => {
        if (done) return;
        done = true;
        overlay.remove();
        resolve(val);
      };
      overlay.addEventListener('click', e => {
        if (e.target === overlay) return close(false);
        const act = e.target.closest('[data-modal-act]');
        if (!act) return;
        e.stopPropagation();
        const a = act.getAttribute('data-modal-act');
        if (a === 'ok') close(true);
        else if (a === 'cancel') close(false);
      });
      overlay._rockClose = () => close(false);
    });
  }

  // 通用模态（返回 overlay 供调用方绑定事件）
  function openModal(opts) {
    const root = $('#modal-root');
    const overlay = document.createElement('div');
    overlay.className = 'modal-overlay';
    overlay.innerHTML =
      '<div class="modal" style="width:' + (opts.width || 480) + 'px">' +
      '<div class="modal-header"><span class="modal-title">' + esc(opts.title || '') + '</span>' +
      '<button class="modal-x" data-modal-act="cancel">✕</button></div>' +
      '<div class="modal-body">' + (opts.body || '') + '</div>' +
      (opts.footer ? '<div class="modal-footer">' + opts.footer + '</div>' : '') +
      '</div>';
    root.appendChild(overlay);
    overlay.addEventListener('click', e => {
      const act = e.target.closest('[data-modal-act]');
      if (act) {
        e.stopPropagation();
        if (act.getAttribute('data-modal-act') === 'cancel') overlay.remove();
      } else if (e.target === overlay) {
        overlay.remove();
      }
    });
    return overlay;
  }

  // ESC 关闭最上层弹层：confirmDialog 优先走 _rockClose（等价取消，resolve(false)），
  // 其余（openModal / detailModal 等）直接移除 overlay。仅在有弹层时响应。
  document.addEventListener('keydown', e => {
    if (e.key !== 'Escape') return;
    const root = document.getElementById('modal-root');
    if (!root || !root.lastElementChild) return;
    const overlay = root.lastElementChild;
    e.preventDefault();
    if (typeof overlay._rockClose === 'function') overlay._rockClose();
    else overlay.remove();
  });

  // 骨架屏
  function skeletonHTML(rows) {
    rows = rows || 4;
    let html = '';
    for (let i = 0; i < rows; i++) html += '<div class="sk-card sk-line"></div>';
    return '<div class="skeleton">' + html + '</div>';
  }

  // 网关可达性横幅 + 顶部状态点（运行时读取 Rock.state.store）
  function markUnreachable(v) {
    const store = Rock.state.store;
    if (store.unreachable === v) return;
    store.unreachable = v;
    const banner = $('#unreachable-banner');
    if (banner) banner.classList.toggle('hidden', !v);
    const dot = $('#gw-status-dot');
    if (dot) {
      dot.classList.remove('dot-ok', 'dot-bad');
      dot.classList.add(v ? 'dot-bad' : 'dot-ok');
    }
    const label = $('#gw-status-text');
    if (label) label.textContent = v ? '网关不可达' : '网关在线';
  }

  // 更新"最近更新"时间
  function noteUpdated() {
    Rock.state.store.lastUpdated = Date.now();
    const el = $('#last-updated');
    if (el) el.textContent = '最近更新 ' + fmtTime(new Date());
    const b = $('#banner-last-updated');
    if (b) b.textContent = fmtDateTime(new Date());
  }

  // 401：凭证失效 → 跳转登录视图（已在认证页则不重复弹）。
  // 处理逻辑经 setUnauthorizedHandler 由入口注入，避免基础设施反向依赖登录视图。
  let unauthorizedHandler = null;

  function setUnauthorizedHandler(fn) {
    unauthorizedHandler = fn;
  }

  function onUnauthorized() {
    if (document.body.classList.contains('auth-mode')) return;
    if (unauthorizedHandler) unauthorizedHandler();
  }

  window.Rock.ui = {
    toast,
    clearToasts,
    confirmDialog,
    openModal,
    skeletonHTML,
    markUnreachable,
    noteUpdated,
    setUnauthorizedHandler,
    onUnauthorized,
  };
})();
