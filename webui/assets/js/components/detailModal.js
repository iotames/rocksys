/* ==========================================================================
 * RockSys 管理控制台 - components/detailModal.js 行详情弹层组件
 * 数据列表行的详情展示：字段清单（必填）→ 键值网格弹层。
 * 业务无关：不感知任何 API 与字段语义，字段清单由视图提供：
 *   - { key, label, render?, pre?, copy? }，render 返回 HTML 属视图显式信任边界
 *     （不走内部 esc），其余取值默认经 Rock.util.esc；
 *   - pre 为等宽块（长文本如 payload/UA），copy 为"一键复制"（clipboard + toast）。
 * 可选 actions 配置：[{ label, className?, onClick(values) }] 渲染为 footer 按钮
 * （业务无关插槽：回调收当前行数据 values，组件不感知字段语义；缺省不传行为不变）。
 * 基于 Rock.ui.openModal（modal 层已含 ESC 关闭）。挂载 window.Rock.comp.detailModal。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const esc = Rock.util.esc;

  // 复制按钮点击（弹层内事件委托，data-copy-value 存原文）
  function copyText(text) {
    const done = () => Rock.ui.toast('已复制', 'success', 1600);
    const fail = () => Rock.ui.toast('复制失败', 'error', 1600);
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, fail);
    } else {
      fail();
    }
  }

  // 单字段渲染：render 显式 HTML → 原样；pre → 等宽块；否则 esc 后的键值网格项
  function fieldHTML(f, row) {
    const raw = row[f.key];
    if (typeof f.render === 'function') {
      return '<div class="detail-item detail-item-block"><span class="k">' + esc(f.label) + '：</span>' +
        '<span class="v">' + f.render(row) + '</span>' + copyBtnHTML(f, raw) + '</div>';
    }
    const text = raw == null || raw === '' ? '—' : String(raw);
    if (f.pre) {
      return '<div class="detail-item detail-item-block"><span class="k">' + esc(f.label) + '：</span>' +
        copyBtnHTML(f, text) +
        '<span class="detail-pre">' + esc(text) + '</span></div>';
    }
    return '<div class="detail-item"><span class="k">' + esc(f.label) + '：</span><span class="v">' + esc(text) + '</span>' +
      copyBtnHTML(f, text) + '</div>';
  }

  // copy 按钮原文兜底：pre 时取展示文本，render 时取 row 原始值；"复制"动作仅按字段声明启用
  function copyBtnHTML(f, raw) {
    if (!f.copy || raw == null || raw === '') return '';
    return '<button class="btn btn-sm btn-text detail-copy" data-copy-value="' + esc(String(raw)) + '" title="复制">copy</button>';
  }

  // 打开行详情弹层：show({ title, fields, row, width?, actions? })
  function show(opts) {
    if (!opts || !opts.fields || !opts.fields.length) return null;
    const row = opts.row || {};
    const body =
      '<div class="detail-grid detail-grid-modal">' +
      opts.fields.map(function (f) { return fieldHTML(f, row); }).join('') +
      '</div>';
    // 可选 footer 按钮（actions 插槽）：onClick 收当前行数据，回调内自行决定是否关弹层
    let footer = '';
    if (opts.actions && opts.actions.length) {
      footer = opts.actions.map(function (a, i) {
        return '<button class="btn ' + (a.className || 'btn-primary') + '" data-detail-act="' + i + '">' +
          esc(a.label || '') + '</button>';
      }).join('');
    }
    const overlay = Rock.ui.openModal({
      title: (typeof opts.title === 'function' ? opts.title(row) : opts.title) || '详情',
      body: body,
      footer: footer,
      width: opts.width || 640,
    });
    overlay.addEventListener('click', function (e) {
      const btn = e.target.closest('[data-copy-value]');
      if (btn) { copyText(btn.getAttribute('data-copy-value')); return; }
      const actBtn = e.target.closest('[data-detail-act]');
      if (actBtn) {
        const a = opts.actions[Number(actBtn.getAttribute('data-detail-act'))];
        if (a && typeof a.onClick === 'function') a.onClick(row);
      }
    });
    return overlay;
  }

  window.Rock.comp.detailModal = { show };
})();
