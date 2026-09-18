/* ==========================================================================
 * RockSys 管理控制台 - components/form.js 统一表单控件组件
 *
 * 统一全站 input / select / checkbox 的标记与类名（复用 style.css 既有
 * .input / .input-sm / .select / .select-sm / .form-row / .form-label / .form-hint），
 * 禁止各页面手写裸控件或内联样式——否则页面之间长相不一，改样式要逐个文件找。
 * 宽度用 .form-w-sm/md/lg 语义类，不写内联 width。
 *
 * 接口（全部返回 HTML 字符串，由调用方拼进视图）：
 *   四个输入控件（input/select/textarea/switch/checkbox）统一支持 disabled?（置灰不可编辑）；
 *   cls? 均为追加类名钩子。控件形态只表达"值的类型"，可编辑性一律用 disabled 表达，两者正交。
 *   Rock.comp.form.input({ id?, scope?, field?, type?, placeholder?, value?, cls?, mono?, disabled?, attrs? })
 *     —— type 可传 date/time/datetime-local 等，供日期时间控件复用（与 filterBar 同款式样）
 *   Rock.comp.form.select({ id?, scope?, field?, options, selected?, disabled?, sm?, attrs? })
 *   Rock.comp.form.textarea({ id?, scope?, field?, placeholder?, value?, rows?, mono?, cls?, disabled?, attrs? })
 *   Rock.comp.form.switch({ id?, scope?, field?, title?, checked?, disabled?, cls?, attrs? })
 *     —— 统一开关（.el-switch 滑块结构），替代各页手写裸 checkbox 开关
 *   Rock.comp.form.checkbox({ id?, scope?, field?, label?, checked?, disabled?, cls?, attrs? })
 *     —— 传 label 为带文字勾选（label.form-check 包裹）；不传 label 为裸勾选框（可配 cls）
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

  // input 文本/数值输入。disabled 与 select/textarea/switch 一致，可传顶层选项或 attrs。
  function input(o) {
    o = o || {};
    const cls = ['input'];
    if (o.sm) cls.push('input-sm');
    if (o.width) cls.push('form-w-' + o.width);
    if (o.mono) cls.push('mono');
    if (o.cls) cls.push(o.cls);
    return '<input type="' + esc(o.type || 'text') + '" class="' + cls.join(' ') + '"' +
      commonAttrs(o) +
      (o.disabled ? ' disabled' : '') +
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

  // checkbox 带文字标签（标签紧贴控件，点击文字可切换）；不传 label 时输出裸勾选框
  //（表格行多选等密集场景，可配 cls 挂页内样式钩子）。
  function checkbox(o) {
    o = o || {};
    const cls = o.cls ? ' class="' + esc(o.cls) + '"' : '';
    const box = '<input type="checkbox"' + cls + commonAttrs(o) + (o.checked ? ' checked' : '') +
      (o.disabled ? ' disabled' : '') + '>';
    if (!o.label) return box;
    return '<label class="form-check">' + box + ' ' + esc(o.label) + '</label>';
  }

  // textarea 多行文本（复用 .input 款式；mono 等宽字体，rows 默认 4）。
  function textarea(o) {
    o = o || {};
    const cls = ['input'];
    if (o.mono) cls.push('mono');
    if (o.cls) cls.push(o.cls);
    return '<textarea class="' + cls.join(' ') + '"' + commonAttrs(o) +
      ' rows="' + (o.rows === undefined || o.rows === null ? 4 : o.rows) + '"' +
      (o.placeholder ? ' placeholder="' + esc(o.placeholder) + '"' : '') +
      (o.disabled ? ' disabled' : '') + '>' +
      esc(o.value === undefined || o.value === null ? '' : String(o.value)) + '</textarea>';
  }

  // switch 开关：统一 .el-switch 滑块结构（label 包裹 + core 滑块），title 作悬浮说明。
  // cls 挂到内部 checkbox 上（与 input/select/textarea 一致），便于调用方加取值/锚点钩子。
  function switch_(o) {
    o = o || {};
    const cls = o.cls ? ' class="' + esc(o.cls) + '"' : '';
    return '<label class="el-switch"' + (o.title ? ' title="' + esc(o.title) + '"' : '') + '>' +
      '<input type="checkbox"' + cls + commonAttrs(o) + (o.checked ? ' checked' : '') +
      (o.disabled ? ' disabled' : '') + '>' +
      '<span class="el-switch-core"></span></label>';
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

  window.Rock.comp.form = { input, select, checkbox, textarea, switch: switch_, search, inline, checkList, label, hint };
})();
