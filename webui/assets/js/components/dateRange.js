/* ==========================================================================
 * RockSys 管理控制台 - components/dateRange.js 时间范围查询组件
 * 组装后端时间参数（精确到分）：fromDate/fromTime → 'YYYY-MM-DDTHH:mm'。
 * 业务无关，供日志 / WAF 等带时间筛选的视图复用。
 * 依赖 Rock.util.fmtDate。挂载到全局命名空间 window.Rock.comp.dateRange。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const fmtDate = Rock.util.fmtDate;

  // 今天日期（YYYY-MM-DD），作为时间筛选默认值
  function today() { return fmtDate(new Date()); }

  // 组装起止时间：query.fromDate/fromTime、query.toDate/toTime，缺省用今天与 00:00/23:59
  function from(query) {
    query = query || {};
    return (query.fromDate || today()) + 'T' + (query.fromTime || '00:00');
  }

  function to(query) {
    query = query || {};
    return (query.toDate || today()) + 'T' + (query.toTime || '23:59');
  }

  window.Rock.comp.dateRange = { today, from, to };
})();
