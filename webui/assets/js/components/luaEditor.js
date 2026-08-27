/* ==========================================================================
 * RockSys 管理控制台 - components/luaEditor.js Lua 编辑器组件（业务无关）
 * textarea 透明文字 + 底层语法高亮层：输入/滚动联动、Tab 缩进、Ctrl/Cmd+S。
 * 近似语法校验（引号 / 括号 / 关键字配对），最终以服务端编译结果为准。
 * wire(inputEl, layerEl, opts)：
 *   opts.value     初始内容
 *   opts.onChange(src)  输入变更回调
 *   opts.onSave()       Ctrl/Cmd+S 回调
 * 依赖 Rock.util.esc / Rock.util.insertAtCursor。
 * 挂载到全局命名空间 window.Rock.comp.luaEditor。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const esc = Rock.util.esc;
  const insertAtCursor = Rock.util.insertAtCursor;

  // Lua 基础语法着色（注释 / 字符串 / 数字 / 关键字 / 函数调用）
  const LUA_TOKEN_RE = /(--\[\[[\s\S]*?\]\]--)|(--[^\n]*)|("(?:\\.|[^"\\\n])*")|('(?:\\.|[^'\\\n])*')|(\b\d+(?:\.\d+)?\b)|(\b(?:and|break|do|else|elseif|end|false|for|function|if|in|local|nil|not|or|repeat|return|then|true|until|while)\b)|([A-Za-z_]\w*)(?=\s*\()/g;

  function highlight(src) {
    let out = '';
    let last = 0;
    let m;
    const clsMap = ['tok-com', 'tok-com', 'tok-str', 'tok-str', 'tok-num', 'tok-kw', 'tok-fn'];
    LUA_TOKEN_RE.lastIndex = 0;
    while ((m = LUA_TOKEN_RE.exec(src)) !== null) {
      out += esc(src.slice(last, m.index));
      let cls = '';
      for (let i = 0; i < 7; i++) {
        if (m[i + 1] !== undefined) { cls = clsMap[i]; break; }
      }
      out += '<span class="' + cls + '">' + esc(m[0]) + '</span>';
      last = m.index + m[0].length;
    }
    out += esc(src.slice(last));
    return out;
  }

  // 绑定编辑器联动：初始内容 / 输入重绘 / 滚动同步 / Tab / Ctrl+S
  function wire(input, layer, opts) {
    opts = opts || {};
    if (!input || !layer) return;
    input.value = opts.value || '';
    layer.innerHTML = highlight(input.value) + '\n';
    input.addEventListener('input', function () {
      layer.innerHTML = highlight(input.value) + '\n';
      if (opts.onChange) opts.onChange(input.value);
    });
    input.addEventListener('scroll', function () {
      layer.scrollTop = input.scrollTop;
      layer.scrollLeft = input.scrollLeft;
    });
    input.addEventListener('keydown', function (e) {
      if (e.key === 'Tab') {
        e.preventDefault();
        insertAtCursor(input, '  ');
      } else if ((e.ctrlKey || e.metaKey) && e.key === 's') {
        e.preventDefault();
        if (opts.onSave) opts.onSave();
      }
    });
  }

  // 近似语法校验：引号 / 括号 / 关键字配对，返回错误文案数组（空 = 通过）
  function check(src) {
    const errs = [];
    const stripped = src.replace(/--\[\[[\s\S]*?\]\]--|--[^\n]*/g, '');
    ['"', "'"].forEach(function (ch) {
      let n = 0;
      for (let i = 0; i < stripped.length; i++) if (stripped[i] === ch) n++;
      if (n % 2) errs.push('存在未配对的 ' + ch + ' 引号');
    });
    [['(', ')'], ['[', ']'], ['{', '}']].forEach(function (pair) {
      let a = 0, b = 0;
      for (let i = 0; i < stripped.length; i++) {
        if (stripped[i] === pair[0]) a++;
        if (stripped[i] === pair[1]) b++;
      }
      if (a !== b) errs.push('括号 ' + pair[0] + pair[1] + ' 不配对（' + a + ' vs ' + b + '）');
    });
    const cnt = function (kw) { return (stripped.match(new RegExp('\\b' + kw + '\\b', 'g')) || []).length; };
    const needEnd = cnt('if') + cnt('for') + cnt('while') + cnt('function') + cnt('do');
    const ends = cnt('end');
    if (needEnd !== ends) errs.push('if/for/while/function/do 与 end 数量不匹配（期望 ' + needEnd + ' 个 end，实际 ' + ends + ' 个）');
    return errs;
  }

  window.Rock.comp.luaEditor = { highlight: highlight, wire: wire, check: check };
})();
