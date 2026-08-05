/* ==========================================================================
 * RockSys 管理控制台 - ui.js UI 基础设施
 * Toast、二次确认弹窗、通用模态、骨架屏、状态色点、网关可达性横幅、
 * 访问凭证设置弹窗。依赖 Rock.util / Rock.state（运行时访问）。
 * 挂载到全局命名空间 window.Rock.ui。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const fmtTime = Rock.util.fmtTime;
  const fmtDateTime = Rock.util.fmtDateTime;

  // 401 弹窗防重入标志
  let tokenDialogOpen = false;
  // 凭证保存/清除后的回调（由 main.js 注入：刷新当前页）
  let tokenSavedHandler = function () {};

  function setTokenSavedHandler(fn) {
    tokenSavedHandler = fn || function () {};
  }

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
    } else if (!tokenDialogOpen) {
      openTokenDialog('访问凭证无效或已过期，请重新输入。');
    }
  }

  // 访问凭证设置弹窗（保存/清除后调用 tokenSavedHandler 刷新当前页）
  function openTokenDialog(hint) {
    if (tokenDialogOpen) return;
    tokenDialogOpen = true;
    const current = Rock.api.getToken();
    const overlay = openModal({
      title: '访问凭证设置',
      width: 460,
      body:
        (hint ? '<div class="alert alert-warning">' + esc(hint) + '</div>' :
          '<div class="form-hint">若网关设置了访问令牌，在此输入后保存，之后所有操作自动携带，无需重复填写。凭证仅保存在本机浏览器。</div>') +
        '<div class="form-row" style="margin-top:12px">' +
        '<label class="form-label">访问令牌（Token）</label>' +
        '<div class="input-affix">' +
        '<input type="password" id="token-input" class="input" placeholder="请输入访问令牌" value="' + esc(current) + '" autocomplete="off">' +
        '<button class="btn btn-sm" id="token-eye">显示</button>' +
        '</div></div>',
      footer:
        (current ? '<button class="btn btn-danger" id="token-clear">清除凭证</button>' : '') +
        '<button class="btn" data-modal-act="cancel">取消</button>' +
        '<button class="btn btn-primary" id="token-save">保存</button>',
    });
    const input = $('#token-input');
    const eye = $('#token-eye');
    eye.addEventListener('click', () => {
      const show = input.type === 'password';
      input.type = show ? 'text' : 'password';
      eye.textContent = show ? '隐藏' : '显示';
    });
    const saveBtn = $('#token-save');
    saveBtn.addEventListener('click', () => {
      const v = input.value.trim();
      if (!v) { toast('请输入访问令牌', 'warning'); return; }
      Rock.api.setToken(v);
      overlay.remove();
      tokenDialogOpen = false;
      toast('访问凭证已保存，后续请求自动携带', 'success');
      tokenSavedHandler();
    });
    const clearBtn = $('#token-clear');
    if (clearBtn) {
      clearBtn.addEventListener('click', () => {
        Rock.api.setToken('');
        overlay.remove();
        tokenDialogOpen = false;
        toast('访问凭证已清除', 'info');
        tokenSavedHandler();
      });
    }
    // 弹窗关闭时复位标志
    const orig = overlay.remove.bind(overlay);
    overlay.remove = function () { tokenDialogOpen = false; orig(); };
  }

  window.Rock.ui = {
    toast,
    confirmDialog,
    openModal,
    skeletonHTML,
    markUnreachable,
    noteUpdated,
    onUnauthorized,
    openTokenDialog,
    setTokenSavedHandler,
  };
})();
