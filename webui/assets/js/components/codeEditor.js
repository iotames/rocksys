/* ==========================================================================
 * RockSys 管理控制台 - components/codeEditor.js 通用代码编辑器组件（业务无关）
 * textarea 透明文字 + 底层语法高亮层：输入/滚动联动、Tab 缩进、Ctrl/Cmd+S。
 * 在 luaEditor 基础上泛化：语法高亮器可插拔（lang 可选 'lua' | 'lines' | 'sql' | null）。
 *   - lua：复用 Rock.comp.luaEditor 的 Lua 着色与近似校验
 *   - lines：规则清单（# 注释行灰显，其余原色，适合 .txt 特征文件）
 *   - sql：SQL 轻量着色（关键字/注释/字符串三类，适合 DDL 预览与编辑）
 *   - null/缺省：不着色（纯文本）
 * API：
 *   html(id, opts)   生成编辑器 HTML（.editor-wrap + 高亮层 + textarea）
 *   wire(id, opts)   绑定联动（opts.value/onChange/onSave/lang）
 *   value(id)        取当前内容；setValue(id, v) 重置内容
 *   dirty            内容是否相对初始值已修改（按钮置灰/离开提醒用）
 * 依赖 Rock.util.esc / Rock.util.insertAtCursor。
 * 挂载到全局命名空间 window.Rock.comp.codeEditor。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = Rock.comp || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const insertAtCursor = Rock.util.insertAtCursor;

  // 各实例状态：id → { value, initial, lang }
  const instances = {};

  // sql 高亮：-- 注释 / 单双引号字符串 / 关键字 三类着色（大小写不敏感，纯前端轻量实现）
  const SQL_TOKEN_RE = /(--[^\n]*)|('(?:''|[^'\n])*')|("(?:""|[^"\n])*")|(\b(?:select|from|where|insert|into|values|update|set|delete|create|table|index|unique|if|not|exists|drop|alter|add|column|primary|key|foreign|references|constraint|default|null|and|or|on|using|autoincrement|auto_increment|serial|view|trigger|begin|commit|rollback|pragma|rename|to|as|with|order|group|by|limit|offset|join|left|inner|distinct|cascade)\b)/gi;

  // 与正则分组顺序一一对应的着色类（注释 / 字符串 / 关键字）
  const SQL_CLS = ['tok-com', 'tok-str', 'tok-str', 'tok-kw'];

  function highlightSQL(src) {
    let out = '';
    let last = 0;
    let m;
    SQL_TOKEN_RE.lastIndex = 0;
    while ((m = SQL_TOKEN_RE.exec(src)) !== null) {
      out += esc(src.slice(last, m.index));
      let cls = '';
      for (let i = 0; i < SQL_CLS.length; i++) {
        if (m[i + 1] !== undefined) { cls = SQL_CLS[i]; break; }
      }
      out += '<span class="' + cls + '">' + esc(m[0]) + '</span>';
      last = m.index + m[0].length;
    }
    out += esc(src.slice(last));
    return out;
  }

  // lines 高亮：# 开头注释行灰显，其余不着色（规则清单语义）
  function highlightLines(src) {
    return src.split('\n').map(function (line) {
      if (line.indexOf('#') === 0) return '<span class="tok-com">' + esc(line) + '</span>';
      return esc(line);
    }).join('\n');
  }

  function highlight(id) {
    const st = instances[id];
    if (!st) return;
    const layer = $('#' + id + '-layer');
    if (!layer) return;
    let html;
    if (st.lang === 'lua') html = Rock.comp.luaEditor.highlight(st.value);
    else if (st.lang === 'lines') html = highlightLines(st.value);
    else if (st.lang === 'sql') html = highlightSQL(st.value);
    else html = esc(st.value);
    layer.innerHTML = html + '\n';
  }

  function sync(id, input) {
    const st = instances[id];
    if (!st) return;
    st.value = input.value;
    highlight(id);
  }

  // html(id, opts)：opts.lang / opts.height（默认 440px）/ opts.placeholder / opts.value
  function html(id, opts) {
    opts = opts || {};
    const height = opts.height ? ' style="height:' + esc(String(opts.height)) + '"' : '';
    instances[id] = { value: opts.value || '', initial: opts.value || '', lang: opts.lang || null };
    return '<div class="editor-wrap"' + height + '>' +
      '<pre class="code-layer" id="' + esc(id) + '-layer"></pre>' +
      '<textarea class="code-input" id="' + esc(id) + '" spellcheck="false" placeholder="' + esc(opts.placeholder || '') + '"></textarea>' +
      '</div>';
  }

  // wire(id, opts)：opts.onChange(src) / opts.onSave()（Ctrl/Cmd+S）；须在 html() 之后、节点已插入 DOM 后调用
  function wire(id, opts) {
    opts = opts || {};
    const input = $('#' + id);
    if (!input || !instances[id]) return;
    input.value = instances[id].value;
    highlight(id);
    input.addEventListener('input', function () {
      sync(id, input);
      if (opts.onChange) opts.onChange(input.value);
    });
    input.addEventListener('scroll', function () {
      const layer = $('#' + id + '-layer');
      if (layer) { layer.scrollTop = input.scrollTop; layer.scrollLeft = input.scrollLeft; }
    });
    input.addEventListener('keydown', function (e) {
      if (e.key === 'Tab') {
        e.preventDefault();
        insertAtCursor(input, '  ');
        sync(id, input);
        if (opts.onChange) opts.onChange(input.value);
      } else if ((e.ctrlKey || e.metaKey) && e.key === 's') {
        e.preventDefault();
        if (opts.onSave) opts.onSave();
      }
    });
  }

  function value(id) {
    const st = instances[id];
    if (!st) return '';
    const input = $('#' + id);
    if (input) st.value = input.value; // 键盘触发未及回调时兜底
    return st.value;
  }

  function setValue(id, v, keepInitial) {
    const st = instances[id];
    if (!st) return;
    st.value = v || '';
    if (!keepInitial) st.initial = st.value; // keepInitial=true：保留脏基线（如"恢复默认"回填后应保持可保存）
    const input = $('#' + id);
    if (input) input.value = st.value;
    highlight(id);
  }

  function dirty(id) {
    const st = instances[id];
    if (!st) return false;
    return value(id) !== st.initial;
  }

  window.Rock.comp.codeEditor = { html, wire, value, setValue, dirty };
})();
