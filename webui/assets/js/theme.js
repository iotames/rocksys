/* ==========================================================================
 * RockSys 管理控制台 - theme.js 主题切换
 * 主题定义、本地持久化（localStorage）、应用与下拉框绑定。
 * 默认主题为浅色（light）。不依赖其他模块，挂载到 window.Rock.theme。
 * 注意：为避免页面加载闪烁，index.html <head> 中已有内联脚本提前设置
 *       document.documentElement 的 data-theme 属性，本模块负责持久化与交互。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};

  const THEME_KEY = 'rocksys.theme';
  const DEFAULT_THEME = 'light';

  // 预设主题列表（与 index.html 中下拉框选项一致）
  const THEMES = [
    { name: 'light', label: '浅色' },
    { name: 'dark',  label: '深色' },
    { name: 'green', label: '护眼绿' },
  ];

  function isValid(name) {
    for (let i = 0; i < THEMES.length; i++) {
      if (THEMES[i].name === name) return true;
    }
    return false;
  }

  // 读取当前主题（本地保存值，非法或缺省时回退默认）
  function get() {
    let v = null;
    try { v = localStorage.getItem(THEME_KEY); } catch (e) { /* 存储不可用则用默认 */ }
    return isValid(v) ? v : DEFAULT_THEME;
  }

  // 应用主题并持久化，同步下拉框选中态
  function apply(name) {
    if (!isValid(name)) name = DEFAULT_THEME;
    document.documentElement.setAttribute('data-theme', name);
    try { localStorage.setItem(THEME_KEY, name); } catch (e) { /* 忽略存储异常 */ }
    const sel = document.getElementById('theme-select');
    if (sel) sel.value = name;
    return name;
  }

  // 绑定顶部工具条主题下拉框
  function bind() {
    const sel = document.getElementById('theme-select');
    if (!sel) return;
    sel.value = get();
    sel.addEventListener('change', () => apply(sel.value));
  }

  window.Rock.theme = {
    THEMES,
    get,
    apply,
    bind,
  };
})();
