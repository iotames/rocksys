/* ==========================================================================
 * RockSys 管控控制台 - views/proxy.js 可信代理文件在线编辑（全局配置·页签）
 * 数据来源（admin API，internal/netutil/proxies_admin.go）：
 *   - GET  /admin/proxy/trusted       可信代理文件清单（外挂覆写状态/行数/修改时间）
 *   - GET  /admin/proxy/trusted/file  读当前生效内容 + 内嵌默认内容
 *   - POST /admin/proxy/trusted/save  保存到 HOT_SCRIPTS_DIR/trusted_proxies/<name>
 * 保存前服务端先解析校验（非法 IP/CIDR 直接 400）；保存后由 ScriptHub 监控
 * 自动感知（≤3s）重读快照热更生效，无需重启。
 * 页面骨架复用 Rock.views.fileEditor 公共工厂（同 WAF 规则文件编辑）；
 * 入口为「全局配置」页顶部页签（无独立路由，head 由宿主页承担）。
 * 挂载到全局命名空间 window.Rock.views.proxy。
 * ==========================================================================
 */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const api = Rock.api;

  const v = Rock.views.fileEditor.create({
    ns: 'tp',
    head: null, // 内嵌在全局配置页签内，页头由宿主页承担
    bannerHTML: '<b>可信代理模型：</b>直连源 IP（TCP 层）命中可信代理列表时才信任 X-Forwarded-For / X-Real-IP 等转发头，否则直接使用直连源 IP，防止公网直连伪造来源。每行一个 IP 或 CIDR 网段（如 10.0.0.1、192.168.0.0/16），# 开头为注释。',
    listTitle: '文件清单',
    pickText: '请在左侧选择文件开始编辑',
    saveToast: '已保存，可信代理快照将在 ≤3s 内自动重读热更生效（无需重启）',
    listUrl: '/admin/proxy/trusted',
    fileUrl: function (name) { return '/admin/proxy/trusted/file?name=' + encodeURIComponent(name); },
    save: function (name, content) {
      return api.post('/admin/proxy/trusted/save')({ name: name, content: content });
    },
    editorHint: function () { return '每行一个 IP 或 CIDR 网段，# 开头为注释，空行忽略。'; },
  });

  window.Rock.views.proxy = {
    // 供全局配置页签调用：渲染进指定容器并确保清单已加载
    renderPanel: async function (host) {
      v.render(host);
      await v.ensureLoaded();
    },
    actions: v.actions,
  };
})();
