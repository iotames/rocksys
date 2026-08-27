/* ==========================================================================
 * RockSys 管理控制台 - views/scripts.js 脚本页
 * 左侧脚本列表 + 右侧编辑器（Lua 基础语法着色）、发布、版本时间线回滚、
 * 移除、基础语法校验。挂载到全局命名空间 window.Rock.views.scripts。
 * ========================================================================== */
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
  const openModal = Rock.ui.openModal;
  const skeletonHTML = Rock.ui.skeletonHTML;
  const noteUpdated = Rock.ui.noteUpdated;

  // 脚本页内部状态
  const scriptsState = {
    loaded: false,
    list: [],
    error: null,
    selected: null,
    source: '',
  };

  function normalizeScripts(res) {
    if (!res || !Array.isArray(res.scripts)) return [];
    return res.scripts.map(s => ({
      name: String(s.name || ''),
      current_version: Number(s.current_version) || 0,
      versions: Array.isArray(s.versions)
        ? s.versions.map(v => ({ version: Number(v.version) || 0, published_at: v.published_at || '' }))
        : [],
    })).filter(s => s.name);
  }

  async function load(opts) {
    const first = !scriptsState.loaded && !opts.silent;
    if (first) skeleton();
    try {
      const res = await api.get('/admin/script/list');
      scriptsState.list = normalizeScripts(res);
      scriptsState.error = null;
      scriptsState.loaded = true;
      if (!scriptsState.list.some(s => s.name === scriptsState.selected)) {
        scriptsState.selected = scriptsState.list.length ? scriptsState.list[0].name : null;
        scriptsState.source = '';
      }
      noteUpdated();
    } catch (e) {
      scriptsState.error = e.message;
      scriptsState.loaded = true;
      if (opts.manual && e.status !== 0) toast('脚本列表加载失败：' + e.message, 'error');
    }
    render();
  }

  function skeleton() {
    const host = $('#page-scripts');
    if (host) host.innerHTML = skeletonHTML(5);
  }

  function render() {
    const host = $('#page-scripts');
    if (!host) return;
    if (!scriptsState.loaded) { skeleton(); return; }
    const list = scriptsState.list;
    if (scriptsState.error && !list.length) {
      host.innerHTML =
        Rock.comp.head.headHTML({
          title: '脚本',
          desc: '管理策略脚本：发布与回滚',
          actions: '<button class="btn btn-sm" data-act="scripts-reload">⟳ 刷新</button>',
        }) +
        '<div class="alert alert-danger">脚本接口不可用：' + esc(scriptsState.error) + '。请确认脚本组件（script）已启用。</div>';
      return;
    }
    const sel = list.find(s => s.name === scriptsState.selected) || null;
    const items = list.map(s => {
      const active = s.name === scriptsState.selected;
      const published = s.current_version > 0;
      return '<div class="script-item' + (active ? ' active' : '') + '" data-act="script-select" data-name="' + esc(s.name) + '">' +
        '<span class="dot ' + (published ? 'dot-ok' : 'dot-off') + '"></span>' +
        '<span class="script-name">' + esc(s.name) + '</span>' +
        '<span class="script-ver">' + (published ? 'v' + s.current_version + ' 生效中' : '未发布') + '</span>' +
        '</div>';
    }).join('');

    let editorHTML;
    if (!sel) {
      editorHTML = '<div class="card">' + Rock.comp.empty.message({ text: '请在左侧选择脚本，或点击「新建脚本」开始' }) + '</div>';
    } else {
      editorHTML =
        '<div class="card">' +
        '<div class="card-title">' +
        '<span>' + esc(sel.name) + (sel.current_version > 0
          ? ' <span class="tag tag-green">v' + sel.current_version + ' 生效中</span>'
          : ' <span class="tag tag-gray">未发布</span>') + '</span>' +
        '<span class="comp-actions">' +
        '<button class="btn btn-sm" data-act="script-check">语法校验</button>' +
        '<button class="btn btn-sm btn-primary" data-act="script-publish">发布</button>' +
        '<button class="btn btn-sm" data-act="script-rollback">版本回滚</button>' +
        '</span></div>' +
        '<div class="editor-wrap">' +
        '<pre class="code-layer" id="code-layer"></pre>' +
        '<textarea class="code-input" id="code-input" spellcheck="false" placeholder="-- 在此编写 RockScript（Lua）策略&#10;-- 示例：当 访问路径 = "/block" 时 返回 禁止访问"></textarea>' +
        '</div>' +
        '<div class="form-hint" style="margin-top:8px">编辑器支持基础 Lua 语法着色；发布前建议先「语法校验」。Ctrl/Cmd + S 快速发布。</div>' +
        '</div>';
    }

    host.innerHTML =
      Rock.comp.head.headHTML({
        title: '脚本',
        desc: '管理策略脚本：发布与回滚',
        actions: '<button class="btn btn-sm" data-act="scripts-reload">⟳ 刷新</button>',
      }) +
      '<div class="alert alert-info"><b>注意：</b>脚本保存在网关运行内存中，网关重启后需重新发布。回滚可选历史版本（仅限本次运行期内发布的版本）。</div>' +
      '<div class="scripts-layout">' +
      '<div class="card"><div class="card-title">脚本列表' +
      '<button class="btn btn-sm btn-primary" data-act="script-new">＋ 新建脚本</button></div>' +
      '<div class="script-list">' + (items || Rock.comp.empty.message({ text: '暂无脚本，点击「新建脚本」开始' })) + '</div></div>' +
      editorHTML +
      '</div>';

    Rock.comp.luaEditor.wire($('#code-input'), $('#code-layer'), {
      value: scriptsState.source,
      onChange: function (src) { scriptsState.source = src; },
      onSave: function () { publish(); },
    });
  }

  // Lua 编辑器（高亮 / 联动 / 近似校验）已下沉到通用组件 Rock.comp.luaEditor

  // "语法校验"按钮：对当前编辑区内容执行近似校验
  function checkCurrent() {
    const src = scriptsState.source;
    if (!src) { toast('脚本内容为空，请先编写', 'warning'); return; }
    const errs = Rock.comp.luaEditor.check(src);
    if (errs.length) {
      openModal({
        title: '语法校验未通过',
        width: 480,
        body: '<div class="alert alert-danger">' + errs.map(esc).join('<br>') + '</div>' +
          '<div class="form-hint">此为前端近似校验，最终以网关沙箱编译结果为准。</div>',
        footer: '<button class="btn btn-primary" data-modal-act="cancel">知道了</button>',
      });
    } else {
      toast('语法校验通过（近似）', 'success');
    }
  }

  async function publish() {
    if (!scriptsState.selected) { toast('请先选择脚本', 'warning'); return; }
    const src = scriptsState.source.trim();
    if (!src) { toast('脚本内容不能为空', 'warning'); return; }
    const errs = Rock.comp.luaEditor.check(src);
    if (errs.length) {
      const overlay = openModal({
        title: '语法校验未通过',
        width: 480,
        body: '<div class="alert alert-danger">' + errs.map(esc).join('<br>') + '</div>' +
          '<div class="form-hint">此为前端近似校验，最终以网关沙箱编译结果为准。可继续发布，网关拒绝时会给出具体原因。</div>',
        footer: '<button class="btn btn-primary" data-modal-act="cancel">知道了</button>',
      });
      return;
    }
    toast('语法校验通过', 'info', 1500);
    const ok = await confirmDialog({
      title: '发布脚本',
      message: '确定发布脚本 <code>' + esc(scriptsState.selected) + '</code> 吗？发布后策略立即生效。',
      confirmText: '发布',
    });
    if (!ok) return;
    try {
      const res = await api.post('/admin/script/publish')({ name: scriptsState.selected, source: src });
      if (res && res.ok === false) {
        toast('发布失败：' + (res.error || '未知错误'), 'error');
        return;
      }
      const ver = res && res.version != null ? res.version : '?';
      toast('已发布 v' + ver, 'success');
      load({ silent: true });
    } catch (e) {
      toast('发布失败：' + e.message, 'error');
    }
  }

  function openRollback() {
    const sel = scriptsState.list.find(s => s.name === scriptsState.selected);
    if (!sel) return;
    const versions = (sel.versions || []).slice().sort((a, b) => b.version - a.version);
    const rows = versions.map(v =>
      '<div class="ver-row">' +
      '<span class="tag tag-blue">v' + v.version + '</span>' +
      '<span class="ver-time">' + esc(fmtDateTime(v.published_at)) + '</span>' +
      (v.version === sel.current_version ? '<span class="ver-current">← 当前生效</span>' : '') +
      '<button class="btn btn-sm" data-ver="' + v.version + '">回滚到该版本</button>' +
      '</div>'
    ).join('');
    const overlay = openModal({
      title: '版本时间线 · ' + sel.name,
      width: 520,
      body:
        '<div class="form-hint" style="margin-bottom:6px">仅显示本次运行期内发布的版本；网关重启后历史版本清空。</div>' +
        (rows || '<div class="empty">暂无历史版本</div>') +
        '<div style="border-top:1px solid var(--border);margin-top:12px;padding-top:12px">' +
        '<button class="btn btn-sm btn-danger" id="script-remove">移除该脚本（下线）</button>' +
        '</div>',
    });
    overlay.addEventListener('click', async e => {
      const verBtn = e.target.closest('[data-ver]');
      if (verBtn) {
        const ver = Number(verBtn.getAttribute('data-ver'));
        const ok = await confirmDialog({
          title: '回滚确认',
          message: '确定将 <code>' + esc(sel.name) + '</code> 回滚到 <code>v' + ver + '</code> 吗？回滚后策略立即生效。',
          confirmText: '确认回滚',
          danger: true,
        });
        if (!ok) return;
        try {
          const res = await api.post('/admin/script/rollback')({ name: sel.name, version: ver });
          if (res && res.ok === false) throw new Error(res.error || '未知错误');
          overlay.remove();
          toast('已回滚到 v' + ver, 'success');
          load({ silent: true });
        } catch (err) {
          toast('回滚失败：' + err.message, 'error');
        }
        return;
      }
      const rmBtn = e.target.closest('#script-remove');
      if (rmBtn) {
        const ok = await confirmDialog({
          title: '移除脚本',
          message: '确定移除（下线）脚本 <code>' + esc(sel.name) + '</code> 吗？该操作不可撤销。',
          confirmText: '确认移除',
          danger: true,
        });
        if (!ok) return;
        try {
          const res = await api.post('/admin/script/rollback')({ name: sel.name, version: 0 });
          if (res && res.ok === false) throw new Error(res.error || '未知错误');
          overlay.remove();
          toast('已移除脚本 ' + sel.name, 'success');
          load({ silent: true });
        } catch (err) {
          toast('移除失败：' + err.message, 'error');
        }
      }
    });
  }

  function openNew() {
    const overlay = openModal({
      title: '新建脚本',
      width: 400,
      body:
        '<div class="form-row"><label class="form-label">脚本名称（小写字母 / 数字 / 下划线）</label>' +
        '<input class="input" id="new-script-name" placeholder="如 rule1" autocomplete="off"></div>',
      footer: '<button class="btn" data-modal-act="cancel">取消</button><button class="btn btn-primary" id="new-script-ok">创建</button>',
    });
    const input = $('#new-script-name');
    input.focus();
    const doCreate = () => {
      const name = input.value.trim();
      if (!name) { toast('请输入脚本名称', 'warning'); return; }
      if (!/^[a-zA-Z0-9_-]{1,64}$/.test(name)) { toast('名称仅支持字母、数字、下划线、连字符（≤64 字符）', 'warning'); return; }
      if (scriptsState.list.some(s => s.name === name)) { toast('已存在同名脚本：' + name, 'warning'); return; }
      scriptsState.list.push({ name: name, current_version: 0, versions: [], local: true });
      scriptsState.selected = name;
      scriptsState.source = '';
      overlay.remove();
      render();
      toast('已创建脚本 ' + name + '（未发布），编写内容后点击发布', 'info');
    };
    $('#new-script-ok').addEventListener('click', doCreate);
    input.addEventListener('keydown', e => { if (e.key === 'Enter') doCreate(); });
  }

  // 选择左侧脚本（main 的事件委托调用）
  function select(name) {
    scriptsState.selected = name;
    // 切换脚本时保留各自源码（内存缓存简单化：直接清空源码区）
    scriptsState.source = '';
    render();
  }

  window.Rock.views.scripts = {
    load,
    render,
    skeleton,
    select,
    checkCurrent,
    publish,
    openRollback,
    openNew,
    actions: {
      'scripts-reload': function () { load({ manual: true }); },
      'script-select': function (el) { select(el.getAttribute('data-name') || ''); },
      'script-new': function () { openNew(); },
      'script-check': function () { checkCurrent(); },
      'script-publish': function () { publish(); },
      'script-rollback': function () { openRollback(); },
    },
  };
})();
