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

  // 右上角消息提示（成功 / 失败 / 警告 / 信息）
  function toast(message, type, duration) {
    type = type || 'success';
    duration = duration == null ? 3200 : duration;
    const root = $('#toast-root');
    if (!root) return;
    const el = document.createElement('div');
    el.className = 'toast toast-' + type;
    const icons = { success: '✓', error: '✕', warning: '⚠', info: 'ℹ' };
    el.innerHTML = '<span class="toast-icon">' + (icons[type] || 'ℹ') + '</span><span class="toast-msg"></span>';
    el.querySelector('.toast-msg').textContent = message;
    root.appendChild(el);
    requestAnimationFrame(() => el.classList.add('show'));
    const close = () => {
      el.classList.remove('show');
      setTimeout(() => el.remove(), 260);
    };
    el.addEventListener('click', close);
    setTimeout(close, duration);
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

  // 401：凭证失效 → 跳转登录视图（已在认证页则不重复弹）
  function onUnauthorized() {
    if (document.body.classList.contains('auth-mode')) return;
    if (Rock.auth) {
      Rock.auth.showAuth();
      Rock.auth.showPanel('login');
      Rock.auth.setError('访问凭证无效或已过期，请重新登录');
    }
  }

  window.Rock.ui = {
    toast,
    confirmDialog,
    openModal,
    skeletonHTML,
    markUnreachable,
    noteUpdated,
    onUnauthorized,
  };
})();
