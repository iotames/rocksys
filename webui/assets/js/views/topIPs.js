/* ==========================================================================
 * RockSys 管控控制台 - views/topIPs.js 攻击源 IP Top N 卡片（拦截统计 Tab 子模块）
 * 数据来源：GET /admin/shield/stats → {days, top_ips:[{client_ip,cnt,in_blacklist}], blacklist_addable}
 * 能力：Top N 下拉（10/20/30/50/100，改即拉）/ 勾选列 + 全选 / 黑名单标注（与拦截判定
 * 同源，已在黑名单的行禁选）/ 批量加入黑名单（一次 import 请求，确认框）/ 地区列（TODO 占位，
 * 待接入 IP 归属地库）。
 * 宿主协作（waf.js）：setData() 喂统计数据 → html() 出卡片 → wire(hooks) 绑交互；
 * hooks = { refresh(): 数据变更后由宿主重新拉取并渲染 }。
 * 挂载到全局命名空间 window.Rock.views.topIPs。
 * ==========================================================================
 */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const fmtInt = Rock.util.fmtInt;
  const api = Rock.api;
  const toast = Rock.ui.toast;
  const confirmDialog = Rock.ui.confirmDialog;

  const TOP_N_OPTIONS = [['10', '10'], ['20', '20'], ['30', '30'], ['50', '50'], ['100', '100']];

  // 模块状态：统计数据 / Top N / 宿主 hooks
  let stats = null;      // 最近一次 /admin/shield/stats 响应
  let addable = false;   // DB 黑名单可用（决定勾选列与批量按钮是否渲染）
  let topN = 10;
  let hooks = { refresh: function () {} };

  // ── 行为 ────────────────────────────────────────────────────────────

  // 批量把勾选 IP 加入黑名单（永久、类别=人工收录 11）：一次 import 请求（body=每行一个 IP），
  // 重复/已在黑名单的 IP 由后端计入 skipped 不报错；成功后经 hooks.refresh 刷新标注
  async function addCheckedToBlacklist() {
    const ips = [...document.querySelectorAll('.waf-topip-check:checked')].map(cb => cb.getAttribute('data-ip'));
    if (!ips.length) { toast('请先勾选要加黑的 IP', 'warn'); return; }
    const ok = await confirmDialog({
      title: '批量加入黑名单',
      message: '将把 ' + ips.length + ' 个 IP 加入黑名单（永久生效，立即拦截）：<br><span class="mono">' + esc(ips.join('、')) + '</span>',
      confirmText: '加入黑名单',
      danger: true,
    });
    if (!ok) return;
    // 照抄 blacklist.js import 调用方式：api.post 对字符串 body 走 JSON 字符串编码，后端已双向兼容
    try {
      const r = await api.post('/admin/shield/blacklist/import?title=' + encodeURIComponent('攻击源TOP批量加黑') + '&block_type=11')(ips.join('\n'));
      toast('已导入 ' + fmtInt(Number(r && r.imported) || 0) + ' 条，跳过 ' + fmtInt(Number(r && r.skipped) || 0) + ' 条', 'success');
    } catch (e) {
      toast('批量加黑失败：' + (e.message || '未知错误') + '。请稍后重试或逐个手工加入', 'error');
    }
    hooks.refresh();
  }

  // ── 列声明（唯一事实源）────────────────────────────────────────────
  // 数组顺序 = 显示顺序；width 可选（th 内联样式）；when 可选（返回 false 整列隐藏）；
  // th/td 由同一数组生成，调整列顺序/宽度/增删列只改本数组，不会表头行体错位。
  const COLUMNS = [
    { label: 'IP', render: r => '<td class="mono">' + esc(r.client_ip || '') + '</td>' },
    { label: '拦截次数', render: r => '<td class="mono">' + fmtInt(Number(r.cnt) || 0) + '</td>' },
    {
      label: '黑名单', width: '96px', when: () => addable,
      render: r => '<td>' + (r.in_blacklist
        ? '<span class="tag tag-red">在黑名单</span>'
        : '<span class="tag tag-gray">否</span>') + '</td>',
    },
    // 地区：TODO 待接入 IP 归属地库，先占位展示
    { label: '地区（TODO）', render: () => '<td>—</td>' },
  ];

  // ── 渲染 ────────────────────────────────────────────────────────────

  function html() {
    if (!stats) return '';
    const rows = stats.top_ips || [];
    if (!rows.length) return '';
    const cols = COLUMNS.filter(c => !c.when || c.when());
    const head = addable
      ? '<th style="width:36px"><input type="checkbox" id="waf-topip-all" title="全选（已在黑名单的行不可选）"></th>'
      : '';
    const body = rows.map(r => {
      const cb = addable && !r.in_blacklist
        ? '<input type="checkbox" class="waf-topip-check" data-ip="' + esc(r.client_ip || '') + '">'
        : (addable ? '<input type="checkbox" disabled>' : '');
      return '<tr><td>' + cb + '</td>' + cols.map(c => c.render(r)).join('') + '</tr>';
    }).join('');
    const actions = addable
      ? '<button class="btn btn-sm btn-primary" data-act="waf-topip-black">批量加入黑名单</button>'
      : '';
    return '<div class="card"><div class="card-title">' +
      '<span>攻击源 IP Top <select class="select select-sm" id="waf-top-n" style="margin-left:4px">' +
      Rock.comp.select.options(TOP_N_OPTIONS, String(topN)) +
      '</select><span class="card-sub" style="margin-left:8px">近 ' + esc(String(stats.days)) +
      ' 天 · 按拦截次数（聚合查询无分页）</span></span>' +
      '<span class="comp-actions">' + actions + '</span></div>' +
      '<div class="table-wrap"><table class="table"><thead><tr>' + head +
      cols.map(c => '<th' + (c.width ? ' style="width:' + esc(c.width) + '"' : '') + '>' + esc(c.label) + '</th>').join('') +
      '</tr></thead><tbody>' + body + '</tbody></table></div></div>';
  }

  // 绑定卡片内交互（须在 html() 插入 DOM 后调用）：Top N 改即拉 + 全选联动
  function wire() {
    const topSel = $('#waf-top-n');
    if (topSel) topSel.addEventListener('change', () => {
      topN = Number(topSel.value) || 10;
      hooks.refresh();
    });
    const allCb = $('#waf-topip-all');
    if (allCb) allCb.addEventListener('change', () => {
      document.querySelectorAll('.waf-topip-check').forEach(cb => { cb.checked = allCb.checked; });
    });
  }

  window.Rock.views.topIPs = {
    // 宿主启动时注入协作钩子：refresh() = 按当前 Top N 重新拉取统计并渲染
    bindHooks: function (h) { hooks = h || hooks; },
    // 当前 Top N（宿主拼 /admin/shield/stats?top= 参数用）
    topN: function () { return topN; },
    // 宿主每次拉到 /admin/shield/stats 响应后调用（resp 为 null/错误时清空展示）
    setData: function (resp) {
      stats = resp && resp.top_ips ? resp : null;
      addable = !!(resp && resp.blacklist_addable);
    },
    html: html,
    wire: wire,
    actions: {
      'waf-topip-black': function () { addCheckedToBlacklist(); },
    },
  };
})();
