/* ==========================================================================
 * RockSys 管理控制台 - components/form.js 统一表单控件组件
 *
 * 统一全站 input / select / checkbox 的标记与类名（复用 style.css 既有
 * .input / .input-sm / .select / .select-sm / .form-row / .form-label / .form-hint），
 * 禁止各页面手写裸控件或内联样式——否则页面之间长相不一，改样式要逐个文件找。
 * 宽度用 .form-w-sm/md/lg 语义类，不写内联 width。
 *
 * 接口（全部返回 HTML 字符串，由调用方拼进视图）：
 *   Rock.comp.form.input({ id?, scope?, field?, type?, placeholder?, value?, cls?, mono?, attrs? })
 *   Rock.comp.form.select({ id?, scope?, field?, options, selected?, disabled?, sm?, attrs? })
 *   Rock.comp.form.checkbox({ id?, scope?, field?, label, checked?, attrs? })
 *   Rock.comp.form.search({ id?, placeholder, scope?, field?, value? })   —— 搜索框（🔍 前缀 + 统一占位样式）
 *   Rock.comp.form.inline(controlsHTML)                                  —— 行内控件组容器（自动换行、统一间距）
 *   Rock.comp.form.checkList(itemsHTML)                                  —— 复选清单容器（表多选等）
 *   Rock.comp.form.label(text, controlHTML)                              —— 上标签下控件（竖排表单行）
 *   Rock.comp.form.hint(text)                                            —— 说明文案（.form-hint）
 *
 * scope/field 为可选的数据绑定标记（data-scope / data-field）：页面用页内委托把输入值
 * 同步进视图状态，重渲染时按状态回显，避免用户已输入内容被重绘清空。
 * 挂载到全局命名空间 window.Rock.comp.form。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const esc = Rock.util.esc;

  // attrs 组装：undefined/null 跳过；布尔 true 输出裸属性名。
  function attrsHTML(attrs) {
    if (!attrs) return '';
    return Object.keys(attrs).map(function (k) {
      const v = attrs[k];
      if (v === undefined || v === null || v === false) return '';
      if (v === true) return ' ' + k;
      return ' ' + k + '="' + esc(String(v)) + '"';
    }).join('');
  }

  // 通用绑定与标识属性（数据同步 + id/name + 附加 attrs）。
  function commonAttrs(o) {
    let s = '';
    if (o.id) s += ' id="' + esc(o.id) + '"';
    if (o.scope) s += ' data-scope="' + esc(o.scope) + '"';
    if (o.field) s += ' data-field="' + esc(o.field) + '"';
    s += attrsHTML(o.attrs);
    return s;
  }

  // input 文本/数值输入。
  function input(o) {
    o = o || {};
    const cls = ['input'];
    if (o.sm) cls.push('input-sm');
    if (o.width) cls.push('form-w-' + o.width);
    if (o.mono) cls.push('mono');
    if (o.cls) cls.push(o.cls);
    return '<input type="' + esc(o.type || 'text') + '" class="' + cls.join(' ') + '"' +
      commonAttrs(o) +
      (o.placeholder ? ' placeholder="' + esc(o.placeholder) + '"' : '') +
      ' value="' + esc(o.value === undefined || o.value === null ? '' : String(o.value)) + '"' +
      ' autocomplete="off" spellcheck="false">';
  }

  // select 下拉：options 为 [[value, label], …]（复用 Rock.comp.select.options 保持选中语义一致）。
  function select(o) {
    o = o || {};
    const cls = ['select'];
    if (o.sm) cls.push('select-sm');
    if (o.width) cls.push('form-w-' + o.width);
    if (o.cls) cls.push(o.cls);
    return '<select class="' + cls.join(' ') + '"' + commonAttrs(o) +
      (o.disabled ? ' disabled' : '') + '>' +
      Rock.comp.select.options(o.options || [], o.selected) + '</select>';
  }

  // checkbox 带文字标签（标签紧贴控件，点击文字可切换）。
  function checkbox(o) {
    o = o || {};
    return '<label class="form-check"><input type="checkbox"' + commonAttrs(o) +
      (o.checked ? ' checked' : '') + '> ' + esc(o.label || '') + '</label>';
  }

  // search 统一搜索框：放大镜前缀由 CSS 类提供（各页只需给占位文案），
  // 供全局配置页等搜索入口复用，避免各页自造搜索样式。
  function search(o) {
    o = o || {};
    return input({
      id: o.id, scope: o.scope, field: o.field, value: o.value,
      placeholder: o.placeholder, cls: 'input-search', width: o.width || 'full',
      attrs: { spellcheck: 'false' },
    });
  }

  // inline 行内控件组：自动换行 + 统一间距（一个卡片内的一组筛选/表单控件）。
  function inline(controlsHTML) {
    return '<div class="form-inline">' + (controlsHTML || '') + '</div>';
  }

  // checkList 复选清单容器（表多选、批量操作等密集勾选场景）。
  function checkList(itemsHTML) {
    return '<div class="check-list">' + (itemsHTML || '') + '</div>';
  }

  // label 竖排表单行：上方字段名、下方控件（沿用 .form-row/.form-label）。
  function label(text, controlHTML) {
    return '<div class="form-row"><label class="form-label">' + esc(text) + '</label>' +
      (controlHTML || '') + '</div>';
  }

  // hint 说明文案。
  function hint(text) {
    return '<div class="form-hint">' + esc(text) + '</div>';
  }

  window.Rock.comp.form = { input, select, checkbox, search, inline, checkList, label, hint };
})();
