/* ==========================================================================
 * RockSys 管控控制台 - views/ruleFiles.js WAF 规则文件编辑（「文件编辑」子视图）
 * 数据来源（admin API，plugins/shield/rules_admin.go）：
 *   - GET  /admin/shield/rules        规则文件清单（外挂覆写状态/生效行数/修改时间）
 *   - GET  /admin/shield/rules/file   读单个文件当前生效内容 + 内嵌默认内容
 *   - POST /admin/shield/rules/save   保存到 HOT_SCRIPTS_DIR/rules/<name>
 * 保存后由 ScriptHub 监控自动感知（≤3s）重建规则快照热更生效，无需重启。
 * 编辑器复用 Rock.comp.codeEditor 公共组件（lines 语法：# 注释行灰显）。
 * 主 Tab 与页面级渲染由 waf.js 协调（同 blacklist 子视图 bindPage 模式）。
 * 挂载到全局命名空间 window.Rock.views.ruleFiles。
 * ==========================================================================
 */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const fmtDateTime = Rock.util.fmtDateTime;
  const api = Rock.api;
  const toast = Rock.ui.toast;
  const confirmDialog = Rock.ui.confirmDialog;
  const codeEditor = Rock.comp.codeEditor;

  // 页内私有状态：清单 / 当前编辑文件 / 未保存标记
  const rfState = {
    files: [], loaded: false, error: '',
    name: '',           // 当前编辑文件名（空 = 未选）
    file: null,         // {content, embedded, override, modified, hot_path}
    saving: false,
  };

  // 页面上下文（waf.js 注入）：tabsHTML() / activeTab()，同 blacklist 子视图
  let pageCtx = { tabsHTML: function () { return ''; }, activeTab: function () { return 'stats'; } };
  function bindPage(ctx) { pageCtx = ctx || pageCtx; }

  const EDITOR_ID = 'rule-file-editor';

  // ── 数据加载 ────────────────────────────────────────────────────────

  async function loadFiles() {
    rfState.loaded = true;
    try {
      const r = await api.get('/admin/shield/rules');
      rfState.files = r.files || [];
      rfState.error = '';
    } catch (e) {
      rfState.error = e.message || '加载失败';
    }
    render($('#page-waf'));
  }

  async function ensureLoaded() {
    if (!rfState.loaded) await loadFiles();
  }

  async function openFile(name) {
    if (!name) return;
    if (codeEditor.dirty(EDITOR_ID) && rfState.name && rfState.name !== name) {
      const ok = await confirmDialog({
        title: '放弃未保存的修改',
        message: '当前文件有未保存的修改，切换后将丢失。确认切换？',
        confirmText: '放弃修改并切换',
        danger: true,
      });
      if (!ok) return;
    }
    try {
      const f = await api.get('/admin/shield/rules/file?name=' + encodeURIComponent(name));
      rfState.name = name;
      rfState.file = f;
      render($('#page-waf'));
    } catch (e) {
      toast('读取失败：' + e.message, 'error');
    }
  }

  async function save() {
    if (!rfState.name || rfState.saving) return;
    const content = codeEditor.value(EDITOR_ID);
    rfState.saving = true;
    try {
      await api.post('/admin/shield/rules/save')({ name: rfState.name, content: content });
      codeEditor.setValue(EDITOR_ID, content);
      toast('已保存，规则将在 ≤3s 内自动热更生效（无需重启）', 'success');
      await loadFiles();
      await openFileReload();
    } catch (e) {
      toast('保存失败：' + e.message, 'error');
    } finally {
      rfState.saving = false;
    }
  }

  // 保存后重读当前文件（刷新覆写状态/修改时间），不清脏标记（setValue 已重置）
  async function openFileReload() {
    try {
      const f = await api.get('/admin/shield/rules/file?name=' + encodeURIComponent(rfState.name));
      rfState.file = f;
      render($('#page-waf'));
    } catch (e) { /* 刷新失败不阻断，保存已成功 */ }
  }

  // 恢复内嵌默认：确认后把内嵌内容回填编辑器（不直接落盘，用户可再修改后保存）
  async function resetToEmbedded() {
    if (!rfState.file || !rfState.name) return;
    const ok = await confirmDialog({
      title: '恢复内嵌默认规则',
      message: '将把编译期内置的默认规则回填到编辑器（尚未落盘）。如需删除外挂覆写文件、彻底回到默认，请回填后直接保存，再手动删除外挂文件。确认回填？',
      confirmText: '回填默认内容',
    });
    if (!ok) return;
    codeEditor.setValue(EDITOR_ID, rfState.file.embedded || '');
    render($('#page-waf'));
  }

  // ── 渲染 ────────────────────────────────────────────────────────────

  function fileListHTML() {
    if (rfState.error) return '<div class="empty">' + esc(rfState.error) + '</div>';
    if (!rfState.files.length) return Rock.comp.empty.message({ text: '规则清单加载中…' });
    return rfState.files.map(function (f) {
      const active = f.name === rfState.name;
      return '<div class="script-item' + (active ? ' active' : '') + '" data-act="rf-open" data-name="' + esc(f.name) + '">' +
        '<div class="script-name">' + esc(f.title || f.name) +
        (f.override ? ' <span class="tag tag-blue" title="存在外挂覆写文件（' + esc(f.name) + '），当前生效外挂内容">外挂</span>' : ' <span class="tag tag-gray" title="无外挂覆写文件，当前生效编译期内置默认">内置</span>') +
        '</div>' +
        '<div class="rf-meta mono">' + esc(f.name) + ' · ' + esc(String(f.lines || 0)) + ' 行</div>' +
        '</div>';
    }).join('');
  }

  function editorCardHTML() {
    if (!rfState.name || !rfState.file) {
      return '<div class="card">' + Rock.comp.empty.message({ text: '请在左侧选择规则文件开始编辑' }) + '</div>';
    }
    const f = rfState.file;
    const meta = rfState.files.filter(function (x) { return x.name === rfState.name; })[0] || {};
    const dirtyNow = codeEditor.dirty(EDITOR_ID);
    return '<div class="card">' +
      '<div class="card-title">' +
      '<span>' + esc(meta.title || rfState.name) + ' <i class="mono" style="font-size:12px;color:var(--text-2)">' + esc(rfState.name) + '</i>' +
      (f.override
        ? ' <span class="tag tag-blue">外挂覆写生效</span>'
        : ' <span class="tag tag-gray">内置默认生效</span>') +
      (dirtyNow ? ' <span class="tag tag-orange">未保存</span>' : '') +
      '</span>' +
      '<span class="comp-actions">' +
      '<button class="btn btn-sm" data-act="rf-reset">恢复默认</button>' +
      '<button class="btn btn-sm' + (dirtyNow ? ' btn-primary' : '') + '" data-act="rf-save"' + (dirtyNow ? '' : ' disabled') + '>保存</button>' +
      '</span></div>' +
      '<div class="form-hint" style="margin-bottom:8px">' +
      '每行一个特征，# 开头为注释，空行忽略；匹配不区分大小写。' +
      (f.modified ? ' 上次修改：' + esc(fmtDateTime(f.modified)) + '；' : '') +
      '保存落点 <span class="mono">' + esc(f.hot_path || '') + '</span>，保存后 ≤3s 自动热更生效（无需重启）。Ctrl/Cmd + S 快速保存。' +
      '</div>' +
      codeEditor.html(EDITOR_ID, {
        lang: 'lines',
        height: 420,
        value: f.content || '',
        placeholder: '# 每行一个特征，# 开头为注释',
      }) +
      '</div>';
  }

  function render(host) {
    if (!host) return;
    host.innerHTML =
      Rock.comp.head.headHTML({
        title: 'WAF安全防护',
        desc: '规则文件在线编辑：保存到外挂目录（HOT_SCRIPTS_DIR/rules/），≤3s 自动热更生效，无需重启',
        actions: '<button class="btn btn-sm" data-act="rf-reload">⟳ 手动刷新</button>',
      }) +
      pageCtx.tabsHTML() +
      '<div class="alert alert-info"><b>提示：</b>规则文件为外挂覆写机制——保存即在外挂目录创建同名文件并覆盖内置默认；对应 WAF 检测开关（如 SHIELD_WAF_SQL_INJECTION）需开启，规则才会参与拦截。</div>' +
      '<div class="scripts-layout">' +
      '<div class="card"><div class="card-title">规则文件</div>' +
      '<div class="script-list">' + fileListHTML() + '</div></div>' +
      editorCardHTML() +
      '</div>';

    if (rfState.name && rfState.file) {
      codeEditor.wire(EDITOR_ID, {
        onChange: function () {
          // 脏状态变化时刷新标题区按钮态（仅更新卡片头部，避免重绘编辑器丢焦点）
          const wrap = document.querySelector('[data-act="rf-save"]');
          if (wrap) {
            const d = codeEditor.dirty(EDITOR_ID);
            wrap.disabled = !d;
            wrap.classList.toggle('btn-primary', d);
            const card = wrap.closest('.card');
            if (card) {
              const title = card.querySelector('.card-title > span');
              const tag = title && title.querySelector('.tag-orange');
              if (d && !tag) title.insertAdjacentHTML('beforeend', ' <span class="tag tag-orange">未保存</span>');
              if (!d && tag) tag.remove();
            }
          }
        },
        onSave: function () { save(); },
      });
    }
  }

  window.Rock.views.ruleFiles = {
    ensureLoaded,
    render,
    save,
    bindPage,
    actions: {
      'rf-reload': function () { loadFiles(); },
      'rf-open': function (el) { openFile(el.getAttribute('data-name') || ''); },
      'rf-save': function () { save(); },
      'rf-reset': function () { resetToEmbedded(); },
    },
  };
})();
