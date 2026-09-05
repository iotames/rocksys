/* ==========================================================================
 * RockSys 管理控制台 - components/cfgSearch.js 局部配置搜索组件
 * 与全局配置页搜索同一风格（输入 + 下拉结果 + 回车/点击定位并编辑），
 * 但作用域限定在调用方给定的配置项列表（组件/服务详情页「配置」页签顶部，
 * 供 shield 等数十项配置的组件局部检索）。纯前端过滤，无新接口。
 * 依赖 Rock.util.esc / Rock.state。挂载到全局命名空间 window.Rock.comp.cfgSearch。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const esc = Rock.util.esc;

  // 匹配评分与高亮逻辑与全局配置页（views/config.js）同口径：
  // KEY 前缀 0 < KEY 包含 1 < 标题包含 2 < 示例/说明包含 3；-1 = 不匹配。
  function searchScore(item, q) {
    const k = item.key.toUpperCase();
    const t = String(item.title || '').toUpperCase();
    if (k.indexOf(q) === 0) return 0;
    if (k.indexOf(q) >= 0) return 1;
    if (t.indexOf(q) >= 0) return 2;
    if (String(item.example || '').toUpperCase().indexOf(q) >= 0) return 3;
    return -1;
  }

  function hiText(text, q) {
    const s = String(text || '');
    const i = s.toUpperCase().indexOf(q);
    if (i < 0) return esc(s);
    return esc(s.slice(0, i)) + '<mark>' + esc(s.slice(i, i + q.length)) + '</mark>' + esc(s.slice(i + q.length));
  }

  // 示例/说明命中摘录：命中位置前后各 ~24 字符窗口，超长以 … 截断
  function hiSnippet(text, q) {
    const s = String(text || '');
    const i = s.toUpperCase().indexOf(q);
    if (i < 0) return '';
    const from = Math.max(0, i - 24), to = Math.min(s.length, i + q.length + 24);
    return (from > 0 ? '…' : '') + hiText(s.slice(from, to), q) + (to < s.length ? '…' : '');
  }

  function maskOf(v) { return String(v == null ? '' : v) === '' ? '（空）' : '••••••••'; }

  /**
   * 在 host 容器顶部挂载搜索栏，并返回承载配置行的子容器（调用方将
   * Rock.comp.configEditor.render 渲染到该子容器，刷新时搜索栏不被重建）。
   * @param {HTMLElement} host 配置页签面板容器
   * @param {Array} items 本作用域配置项列表（store.configList 过滤后的子集）
   * @param {Function} onLocate 定位回调（key）→ 命中后滚动高亮/进入行内编辑
   */
  function mount(host, items, onLocate) {
    const st = { q: '', sel: 0, results: [], timer: null };
    const bar = document.createElement('div');
    bar.className = 'card cfg-searchbar';
    bar.innerHTML = '<div class="cfg-search-wrap">' +
      '<input class="input" placeholder="🔍 搜索本组件配置项 KEY / 标题，选择结果定位并编辑" autocomplete="off" spellcheck="false">' +
      '<div class="cfg-search-drop" hidden></div></div>';
    const rows = document.createElement('div');
    host.appendChild(bar);
    host.appendChild(rows);

    const inp = bar.querySelector('input');
    const drop = bar.querySelector('.cfg-search-drop');

    function dropHTML() {
      if (!st.q) return '';
      if (!st.results.length) return '<div class="cfg-search-empty">无匹配配置项（支持 KEY 与标题，KEY 优先）</div>';
      const total = items.filter(it => searchScore(it, st.q) >= 0).length;
      const rowsHTML = st.results.map(function (it, i) {
        const sensitive = Rock.state.isSensitiveKey(it.key);
        const restart = Rock.state.RESTART_KEYS.indexOf(it.key) >= 0;
        const display = sensitive ? maskOf(it.current) : (it.current === '' ? '（空）' : it.current);
        return '<div class="cfg-search-item' + (i === st.sel ? ' is-sel' : '') + '" data-key="' + esc(it.key) + '">' +
          '<div class="cfg-search-main">' +
          '<span class="cfg-search-key">' + hiText(it.key, st.q) + '</span>' +
          '<span class="cfg-search-title">' + hiText(it.title, st.q) + '</span>' +
          '</div>' +
          '<div class="cfg-search-meta">' +
          (restart ? '<span class="tag tag-gray">需重启</span>' : '') +
          (sensitive ? '<span class="tag tag-orange">敏感</span>' : '') +
          '<span class="cfg-search-val mono">' + esc(display) + '</span>' +
          '<span class="cfg-search-go">回车定位并编辑 →</span>' +
          '</div>' +
          (hiSnippet(it.example, st.q)
            ? '<div class="cfg-search-usage">说明：' + hiSnippet(it.example, st.q) + '</div>'
            : '') +
          '</div>';
      }).join('');
      return rowsHTML + (total > st.results.length
        ? '<div class="cfg-search-more">共 ' + total + ' 项匹配，仅展示前 ' + st.results.length + ' 项，请细化关键字</div>'
        : '');
    }

    function run(q) {
      st.q = q.trim().toUpperCase();
      st.sel = 0;
      st.results = st.q
        ? items.map(function (it) { return { it: it, s: searchScore(it, st.q) }; })
            .filter(function (x) { return x.s >= 0; })
            .sort(function (a, b) { return a.s - b.s || a.it.key.localeCompare(b.it.key); })
            .slice(0, 20).map(function (x) { return x.it; })
        : [];
      drop.innerHTML = dropHTML();
      drop.hidden = !st.q;
    }

    function locate(key) {
      drop.hidden = true;
      inp.value = '';
      st.q = '';
      onLocate(key);
    }

    inp.addEventListener('input', function () {
      clearTimeout(st.timer);
      const v = inp.value;
      st.timer = setTimeout(function () { run(v); }, 150);
    });
    inp.addEventListener('focus', function () { if (st.q) drop.hidden = false; });
    inp.addEventListener('keydown', function (e) {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        if (!st.results.length) return;
        st.sel = (st.sel + (e.key === 'ArrowDown' ? 1 : st.results.length - 1)) % st.results.length;
        drop.innerHTML = dropHTML();
        const sel = drop.querySelector('.cfg-search-item.is-sel');
        if (sel) sel.scrollIntoView({ block: 'nearest' });
        return;
      }
      if (e.key === 'Enter') {
        const it = st.results[st.sel];
        if (it) locate(it.key);
        return;
      }
      if (e.key === 'Escape') {
        inp.value = '';
        st.q = '';
        drop.hidden = true;
      }
    });
    drop.addEventListener('mousedown', function (e) {
      // mousedown：先于 input blur 触发，保证点击结果行可靠
      const row = e.target.closest('.cfg-search-item[data-key]');
      if (row) { e.preventDefault(); locate(row.getAttribute('data-key')); }
    });
    inp.addEventListener('blur', function () { setTimeout(function () { drop.hidden = true; }, 120); });

    return rows;
  }

  window.Rock.comp.cfgSearch = { mount: mount };
})();
