/* ==========================================================================
 * RockSys 管控控制台 - views/ruleFiles.js WAF 规则文件编辑（「文件编辑」子视图）
 * 数据来源（admin API，plugins/shield/rules_admin.go）：
 *   - GET  /admin/shield/rules        规则文件清单（外挂覆写状态/生效行数/修改时间）
 *   - GET  /admin/shield/rules/file   读单个文件当前生效内容 + 内嵌默认内容
 *   - POST /admin/shield/rules/save   保存到 HOT_SCRIPTS_DIR/rules/<name>
 * 保存后由 ScriptHub 监控自动感知（≤3s）重建规则快照热更生效，无需重启。
 * 页面骨架复用 Rock.views.fileEditor 公共工厂（同 trustedProxies 视图）。
 * 主 Tab 与页面级渲染由 waf.js 协调（同 blacklist 子视图 bindPage 模式）。
 * 挂载到全局命名空间 window.Rock.views.ruleFiles。
 * ==========================================================================
 */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const api = Rock.api;

  // 页面上下文（waf.js 注入）：tabsHTML() / activeTab()，同 blacklist 子视图
  let pageCtx = { tabsHTML: function () { return ''; }, activeTab: function () { return 'stats'; } };

  const v = Rock.views.fileEditor.create({
    ns: 'rf',
    page: 'waf',
    head: {
      title: 'WAF安全防护',
      desc: '规则文件在线编辑：保存到外挂目录（HOT_SCRIPTS_DIR/rules/），≤3s 自动热更生效，无需重启',
    },
    tabsHTML: function () { return pageCtx.tabsHTML(); },
    bannerHTML: '<b>提示：</b>规则文件为外挂覆写机制——保存即在外挂目录创建同名文件并覆盖内置默认；对应 WAF 检测开关（如 SHIELD_WAF_SQL_INJECTION）需开启，规则才会参与拦截。',
    listTitle: '规则文件',
    pickText: '请在左侧选择规则文件开始编辑',
    saveToast: '已保存，规则将在 ≤3s 内自动热更生效（无需重启）',
    listUrl: '/admin/shield/rules',
    fileUrl: function (name) { return '/admin/shield/rules/file?name=' + encodeURIComponent(name); },
    save: function (name, content) {
      return api.post('/admin/shield/rules/save')({ name: name, content: content });
    },
    editorHint: function () { return '每行一个特征，# 开头为注释，空行忽略；匹配不区分大小写。'; },
  });

  window.Rock.views.ruleFiles = {
    ensureLoaded: v.ensureLoaded,
    render: v.render,
    save: v.save,
    bindPage: function (ctx) { pageCtx = ctx || pageCtx; },
    actions: v.actions,
  };
})();
