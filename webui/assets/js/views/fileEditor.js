/* ==========================================================================
 * RockSys 管控控制台 - views/fileEditor.js 外挂文件在线编辑通用视图工厂
 * 面向「ScriptHub 外挂目录文件」类编辑页（WAF 规则文件 / 可信代理列表）的
 * 业务无关骨架：文件清单（外挂覆写状态/行数）+ codeEditor 编辑卡片 +
 * 保存（原子写外挂目录，≤3s 自动热更）/ 恢复默认（回填内嵌内容不落盘）。
 * 数据来源由 cfg 注入（清单 URL / 读文件 URL / 保存函数），本工厂不感知业务语义。
 * 用法：const v = Rock.views.fileEditor.create(cfg)；cfg 字段见 create 注释。
 * ==========================================================================
 */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  window.Rock.views.fileEditor = {
    /**
     * @param {Object} cfg 视图配置：
     *   ns          {string}   action 前缀（如 'rf' / 'tp'，防多实例冲突）
     *   head        {Object}   {title, desc} 页头（Rock.comp.head）
     *   bannerHTML  {string}   页头下方的 alert 提示 HTML（可空）
     *   listTitle   {string}   左侧清单卡片标题
     *   pickText    {string}   未选文件时编辑区占位文案
     *   saveToast   {string}   保存成功提示文案
     *   listUrl     {string}   GET 清单 → {files:[{name,title,desc,override,lines,modified?}]}
     *   fileUrl     {Function} name → GET 读文件 URL → {content,embedded,override,modified,hot_path}
     *   save        {Function} (name, content) → Promise（POST 保存）
     *   editorHint  {Function} 可选 (file) → 编辑区提示文案追加
     */
    create: function (cfg) {
      const $ = Rock.util.$;
      const esc = Rock.util.esc;
      const fmtDateTime = Rock.util.fmtDateTime;
      const api = Rock.api;
      const toast = Rock.ui.toast;
      const confirmDialog = Rock.ui.confirmDialog;
      const codeEditor = Rock.comp.codeEditor;

      const ns = cfg.ns;
      const EDITOR_ID = ns + '-file-editor';

      // 页内私有状态：清单 / 当前编辑文件 / 未保存标记
      // 渲染宿主：外部 render(host) 注入；清单/文件加载后的内部刷新沿用同一宿主
      let viewHost = null;

      const st = {
        files: [], loaded: false, error: '',
        name: '',   // 当前编辑文件名（空 = 未选）
        file: null, // {content, embedded, override, modified, hot_path}
        saving: false,
      };

      // ── 数据加载 ──────────────────────────────────────────────────────

      async function loadFiles(opts) {
        opts = opts || {};
        st.loaded = true;
        try {
          const r = await api.get(cfg.listUrl);
          st.files = r.files || [];
          st.error = '';
        } catch (e) {
          st.error = e.message || '加载失败';
          if (!opts.silent && e.status !== 0) toast('文件列表加载失败：' + e.message, 'error');
        }
        render(viewHost);
      }

      async function ensureLoaded() {
        if (!st.loaded) await loadFiles();
      }

      async function openFile(name) {
        if (!name) return;
        if (codeEditor.dirty(EDITOR_ID) && st.name && st.name !== name) {
          const ok = await confirmDialog({
            title: '放弃未保存的修改',
            message: '当前文件有未保存的修改，切换后将丢失。确认切换？',
            confirmText: '放弃修改并切换',
            danger: true,
          });
          if (!ok) return;
        }
        try {
          const f = await api.get(cfg.fileUrl(name));
          st.name = name;
          st.file = f;
          render(viewHost);
        } catch (e) {
          toast('读取失败：' + e.message, 'error');
        }
      }

      async function save() {
        if (!st.name || st.saving) return;
        const content = codeEditor.value(EDITOR_ID);
        st.saving = true;
        try {
          await cfg.save(st.name, content);
          codeEditor.setValue(EDITOR_ID, content);
          toast(cfg.saveToast, 'success');
          await loadFiles({ silent: true }); // 保存已成功，列表刷新失败不再叠加报错
          await openFileReload();
        } catch (e) {
          toast('保存失败：' + e.message, 'error');
        } finally {
          st.saving = false;
        }
      }

      // 保存后重读当前文件（刷新覆写状态/修改时间），setValue 已重置脏标记
      async function openFileReload() {
        try {
          const f = await api.get(cfg.fileUrl(st.name));
          st.file = f;
          render(viewHost);
        } catch (e) { /* 刷新失败不阻断，保存已成功 */ }
      }

      // 恢复内嵌默认：确认后把内嵌内容回填编辑器（不直接落盘，用户可再修改后保存）
      async function resetToEmbedded() {
        if (!st.file || !st.name) return;
        const ok = await confirmDialog({
          title: '恢复内嵌默认内容',
          message: '将把编译期内置的默认内容回填到编辑器（尚未落盘）。确认回填？',
          confirmText: '回填默认内容',
        });
        if (!ok) return;
        // 回填内嵌内容：脏基线仍是文件当前内容（内容有差异即亮起保存按钮）
        render(viewHost);
        codeEditor.setValue(EDITOR_ID, st.file.embedded || '', true);
        refreshSaveBtn();
      }

      // ── 渲染 ──────────────────────────────────────────────────────────

      // 保存按钮随脏状态刷新（仅更新卡片头部，避免重绘编辑器丢焦点）
      function refreshSaveBtn() {
        const wrap = document.querySelector('[data-act="' + ns + '-save"]');
        if (!wrap) return;
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

      function fileListHTML() {
        if (st.error) return '<div class="empty">' + esc(st.error) + '</div>';
        if (!st.files.length) return Rock.comp.empty.message({ text: cfg.listTitle + '加载中…' });
        return st.files.map(function (f) {
          const active = f.name === st.name;
          return '<div class="script-item' + (active ? ' active' : '') + '" data-act="' + ns + '-open" data-name="' + esc(f.name) + '">' +
            '<div class="script-name">' + esc(f.title || f.name) +
            (f.override ? ' <span class="tag tag-blue" title="存在外挂覆写文件（' + esc(f.name) + '），当前生效外挂内容">外挂</span>' : ' <span class="tag tag-gray" title="无外挂覆写文件，当前生效编译期内置默认">内置</span>') +
            '</div>' +
            '<div class="rf-meta mono">' + esc(f.name) + ' · ' + esc(String(f.lines || 0)) + ' 行</div>' +
            '</div>';
        }).join('');
      }

      function editorCardHTML() {
        if (!st.name || !st.file) {
          return '<div class="card">' + Rock.comp.empty.message({ text: cfg.pickText }) + '</div>';
        }
        const f = st.file;
        const meta = st.files.filter(function (x) { return x.name === st.name; })[0] || {};
        const dirtyNow = codeEditor.dirty(EDITOR_ID);
        return '<div class="card">' +
          '<div class="card-title">' +
          '<span>' + esc(meta.title || st.name) + ' <i class="mono" style="font-size:12px;color:var(--text-2)">' + esc(st.name) + '</i>' +
          (f.override
            ? ' <span class="tag tag-blue">外挂覆写生效</span>'
            : ' <span class="tag tag-gray">内置默认生效</span>') +
          (dirtyNow ? ' <span class="tag tag-orange">未保存</span>' : '') +
          '</span>' +
          '<span class="comp-actions">' +
          '<button class="btn btn-sm" data-act="' + ns + '-reset">恢复默认</button>' +
          '<button class="btn btn-sm' + (dirtyNow ? ' btn-primary' : '') + '" data-act="' + ns + '-save"' + (dirtyNow ? '' : ' disabled') + '>保存</button>' +
          '</span></div>' +
          '<div class="form-hint" style="margin-bottom:8px">' +
          (cfg.editorHint ? cfg.editorHint(f) : '') +
          (f.modified ? ' 上次修改：' + esc(fmtDateTime(f.modified)) + '；' : '') +
          '保存落点 <span class="mono">' + esc(f.hot_path || '') + '</span>，保存后 ≤3s 自动热更生效（无需重启）。Ctrl/Cmd + S 快速保存。' +
          '</div>' +
          codeEditor.html(EDITOR_ID, {
            lang: 'lines',
            height: 420,
            value: f.content || '',
            placeholder: '# 每行一条，# 开头为注释',
          }) +
          '</div>';
      }

      function render(host) {
        viewHost = host || viewHost;
        if (!viewHost) return;
        viewHost.innerHTML =
          (cfg.head ? Rock.comp.head.headHTML({
            title: cfg.head.title,
            desc: cfg.head.desc,
            actions: '<button class="btn btn-sm" data-act="' + ns + '-reload">⟳ 手动刷新</button>',
          }) : '') +
          (cfg.tabsHTML ? cfg.tabsHTML() : '') +
          (cfg.bannerHTML ? '<div class="alert alert-info">' + cfg.bannerHTML + '</div>' : '') +
          '<div class="scripts-layout">' +
          '<div class="card"><div class="card-title">' + esc(cfg.listTitle) + '</div>' +
          '<div class="script-list">' + fileListHTML() + '</div></div>' +
          editorCardHTML() +
          '</div>';

        if (st.name && st.file) {
          codeEditor.wire(EDITOR_ID, {
            onChange: function () { refreshSaveBtn(); },
            onSave: function () { save(); },
          });
        }
      }

      return {
        ensureLoaded: ensureLoaded,
        render: render,
        save: save,
        state: st,
        actions: {
          [ns + '-reload']: function () { loadFiles(); },
          [ns + '-open']: function (el) { openFile(el.getAttribute('data-name') || ''); },
          [ns + '-save']: function () { save(); },
          [ns + '-reset']: function () { resetToEmbedded(); },
        },
      };
    },
  };
})();
