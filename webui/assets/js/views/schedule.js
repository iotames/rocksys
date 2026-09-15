/* ==========================================================================
 * RockSys 管理控制台 - views/schedule.js 定时任务页（只读）
 * 数据源 GET /admin/schedule/list（只读登记 + 状态汇总，不驱动任务）。
 * 展示：任务名称 / 说明 / 类型 / 关联开关（enabled 由服务端按配置现值计算）/
 *       上次执行时间（仅本期回写者 geoip_sync 有值，其余行标注「未登记」并说明）。
 * UX 红线：load 透传 refreshPage 的 opts（含 silent）；非引导态加载失败弹统一 error toast。
 * 挂载到全局命名空间 window.Rock.views.schedule。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const toast = Rock.ui.toast;
  const api = Rock.api;

  let rows = [];
  let loaded = false;
  let loading = false;
  let errText = '';

  function fmtRunAt(v) {
    if (!v) return '';
    return esc(String(v).replace('T', ' ').slice(0, 19)) + ' UTC';
  }

  function statusTag(st) {
    if (!st) return '<span class="tag">未登记</span>';
    if (st === 'success') return '<span class="tag tag-green">成功</span>';
    if (st === 'failed') return '<span class="tag tag-red">失败</span>';
    if (st === 'skipped') return '<span class="tag">跳过</span>';
    if (st === 'partial') return '<span class="tag tag-orange">部分完成</span>';
    return esc(st);
  }

  function enabledTag(enabled) {
    return enabled
      ? '<span class="tag tag-green">启用</span>'
      : '<span class="tag tag-red">停用</span>';
  }

  function kindTag(kind) {
    return kind === 'configurable'
      ? '<span class="tag tag-blue">可配型</span>'
      : '<span class="tag">系统级只读</span>';
  }

  function tableHTML() {
    const head = '<tr><th>任务</th><th>计划</th><th>类型</th><th>关联开关</th><th>最近执行时间</th><th>最近执行结果</th><th>说明</th></tr>';
    const body = rows.map(function (t) {
      const cfg = t.config_key
        ? '<code>' + esc(t.config_key) + '</code>'
        : '<span class="form-hint">—（系统级）</span>';
      // 上次执行时间仅对本期回写者（geoip_sync）有值；其余行「未登记」须带说明，防误读为「从未执行」
      const runCell = t.last_run_at
        ? fmtRunAt(t.last_run_at)
        : '<span class="form-hint">未登记' +
          (t.name === 'geoip_sync' ? '' : '（本类任务不在本期状态回写范围）') + '</span>';
      return '<tr>' +
        '<td><b>' + esc(t.title || t.name) + '</b><div class="form-hint"><code>' + esc(t.name) + '</code></div></td>' +
        '<td>' + esc(t.plan || '—') + '</td>' +
        '<td>' + kindTag(t.kind) + '</td>' +
        '<td>' + cfg + '<div style="margin-top:4px">' + enabledTag(!!t.enabled) + '</div></td>' +
        '<td>' + runCell + '</td>' +
        '<td>' + statusTag(t.last_status) +
          (t.last_message ? '<div class="form-hint" style="max-width:280px;white-space:normal">' + esc(t.last_message) + '</div>' : '') + '</td>' +
        '<td class="form-hint" style="max-width:260px;white-space:normal">' + esc(t.remark || '—') + '</td>' +
        '</tr>';
    }).join('');
    return '<div class="card"><div class="table-wrap"><table class="table">' + head + body + '</table></div></div>';
  }

  function render() {
    const host = $('#page-schedule');
    if (!host) return;
    let body;
    if (errText && !rows.length) {
      body = Rock.comp.empty.emptyCard({ text: '定时任务清单加载失败：' + errText, br: true,
        action: '<button class="btn btn-sm btn-primary" data-act="schedule-reload">重试</button>' });
    } else if (!rows.length) {
      body = loading ? '<div class="empty" style="padding:16px">加载中…</div>'
        : Rock.comp.empty.emptyCard({ text: '暂无登记任务（数据访问层未就绪时清单不可用）' });
    } else {
      body = tableHTML();
    }
    host.innerHTML = '<div class="page-head"><h2>定时任务</h2>' +
      '<button class="btn btn-sm" data-act="schedule-reload">⟳ 刷新</button></div>' +
      '<div class="alert alert-info"><b>口径说明：</b>本页为只读登记与状态汇总，不驱动任何任务' +
      '（各任务由各自触发循环执行）。「最近执行时间」（执行结束时刻）仅对纳入状态回写的任务（当前为 GeoIP 关联表同步）有值，' +
      '其余任务显示「未登记」不代表从未执行；需要实时/精确触发的内部机制不纳入统一登记。' +
      '启用状态来自配置中心实时值（无独立开关列，可配型任务的开关即其关联配置项）。</div>' + body;
  }

  async function load(opts) {
    const o = opts || {};
    if (loading) return;
    loading = true;
    if (!loaded) render(); // 首次进入先渲染骨架/旧数据
    try {
      const res = await api.get('/admin/schedule/list');
      rows = (res && Array.isArray(res.tasks)) ? res.tasks : [];
      loaded = true;
      errText = '';
    } catch (e) {
      errText = (e && e.message) || '未知错误';
      // 非程序化静默刷新的失败必须弹统一 error toast（不自动消失）；行内错误态同时保留
      if (!o.silent) {
        toast('定时任务清单加载失败：' + errText + '，请确认服务可达后点击「刷新」重试', 'error');
      }
    } finally {
      loading = false;
      render();
    }
  }

  window.Rock.views.schedule = {
    load: load,
    render: render,
    actions: {
      'schedule-reload': function () { load({ force: true }); },
    },
  };
})();
