/* ==========================================================================
 * RockSys 管理控制台 - components/chart.js 图表组件
 * 原生 Canvas 自绘趋势折线图，颜色取自 CSS 变量（主题自适应）。
 * 依赖 Rock.util.toDate / Rock.util.pad2 / Rock.util.fmtInt。
 * 挂载到全局命名空间 window.Rock.comp.chart。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  const toDate = Rock.util.toDate;
  const pad2 = Rock.util.pad2;
  const fmtInt = Rock.util.fmtInt;

  // 读取 CSS 变量（主题自适应）
  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }

  // 把 #rrggbb 转 rgba；若变量已是 rgba() 则直接返回
  function hexToRgba(color, alpha) {
    if (/^rgba?\(/.test(color)) return color;
    const m = /^#?([0-9a-fA-F]{6})$/.exec(color);
    if (m) {
      const n = parseInt(m[1], 16);
      return 'rgba(' + ((n >> 16) & 255) + ',' + ((n >> 8) & 255) + ',' + (n & 255) + ',' + alpha + ')';
    }
    return color;
  }

  // 时间刻度：toDate → HH:MM:SS
  function fmtClock(ts) {
    const d = toDate(ts);
    return d ? pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds()) : '';
  }

  // Canvas 自绘趋势折线图：data 为采样数组，value(p) 取每个采样的数值。
  // opts.fmtX 可选：X 轴刻度格式化（缺省 HH:MM:SS；天粒度桶等场景可传自定义）。
  function line(canvas, opts) {
    const data = opts.data;
    const value = opts.value;
    const fmtX = opts.fmtX || fmtClock;
    if (!canvas) return;
    const container = canvas.parentElement;
    const W = container.clientWidth;
    const H = container.clientHeight;
    if (!W || !H) return;
    const dpr = window.devicePixelRatio || 1;
    canvas.width = W * dpr;
    canvas.height = H * dpr;
    canvas.style.width = W + 'px';
    canvas.style.height = H + 'px';
    const ctx = canvas.getContext('2d');
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, W, H);
    if (data.length < 2) {
      ctx.fillStyle = cssVar('--text-2');
      ctx.font = '12px sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText('等待采样数据…', W / 2, H / 2);
      return;
    }
    const padL = 46, padR = 12, padT = 14, padB = 26;
    const iw = W - padL - padR;
    const ih = H - padT - padB;
    if (iw <= 0 || ih <= 0) return;
    let max = 10;
    data.forEach(p => { if (value(p) > max) max = value(p); });
    max = max * 1.15;
    const n = data.length;
    const xAt = i => padL + (n === 1 ? 0 : (i / (n - 1)) * iw);
    const yAt = v => padT + ih - (v / max) * ih;

    // 横向网格 + Y 轴刻度
    ctx.strokeStyle = hexToRgba(cssVar('--text-2'), 0.4);
    ctx.fillStyle = cssVar('--text-2');
    ctx.font = '11px sans-serif';
    ctx.textAlign = 'right';
    ctx.lineWidth = 1;
    for (let i = 0; i <= 4; i++) {
      const y = padT + ih - (ih * i) / 4;
      ctx.beginPath();
      ctx.moveTo(padL, y);
      ctx.lineTo(W - padR, y);
      ctx.stroke();
      const v = (max * i) / 4;
      ctx.fillText(v >= 1000 ? fmtInt(v) : v.toFixed(1), padL - 6, y + 4);
    }
    // X 轴时间刻度（首/尾刻度左/右对齐防边缘裁剪；采样点少时 0.5 档与首尾档横向重叠，
    // 刻度间最小间距不足则跳过中间档）
    const minLabelGap = 70;
    const drawX = (idx, align) => {
      ctx.textAlign = align;
      ctx.fillText(fmtX(data[idx].t), xAt(idx), H - 8);
    };
    drawX(0, 'left');
    const midIdx = Math.round((n - 1) * 0.5);
    if (xAt(midIdx) - xAt(0) >= minLabelGap && xAt(n - 1) - xAt(midIdx) >= minLabelGap) {
      drawX(midIdx, 'center');
    }
    drawX(n - 1, 'right');
    // 面积渐变
    const grad = ctx.createLinearGradient(0, padT, 0, padT + ih);
    grad.addColorStop(0, hexToRgba(cssVar('--primary'), 0.3));
    grad.addColorStop(1, hexToRgba(cssVar('--primary'), 0));
    ctx.beginPath();
    data.forEach((p, i) => {
      if (i === 0) ctx.moveTo(xAt(i), yAt(value(p)));
      else ctx.lineTo(xAt(i), yAt(value(p)));
    });
    ctx.lineTo(xAt(n - 1), padT + ih);
    ctx.lineTo(xAt(0), padT + ih);
    ctx.closePath();
    ctx.fillStyle = grad;
    ctx.fill();
    // 折线
    ctx.beginPath();
    data.forEach((p, i) => {
      if (i === 0) ctx.moveTo(xAt(i), yAt(value(p)));
      else ctx.lineTo(xAt(i), yAt(value(p)));
    });
    ctx.strokeStyle = cssVar('--primary');
    ctx.lineWidth = 1.8;
    ctx.stroke();
    // 最新点
    const last = data[n - 1];
    ctx.beginPath();
    ctx.arc(xAt(n - 1), yAt(value(last)), 3, 0, Math.PI * 2);
    ctx.fillStyle = cssVar('--primary');
    ctx.fill();
  }

  // Canvas 自绘柱状图（按时间桶计数，如 WAF 按日趋势）。
  // opts：{ data, value, fmtX, emptyText }；刻度画在每根柱中心（天然远离画布边缘），
  // 零值柱淡显（日期对齐不失真）。
  function bar(canvas, opts) {
    const data = opts.data;
    const value = opts.value;
    const fmtX = opts.fmtX;
    if (!canvas) return;
    const container = canvas.parentElement;
    const W = container.clientWidth;
    const H = container.clientHeight;
    if (!W || !H) return;
    const dpr = window.devicePixelRatio || 1;
    canvas.width = W * dpr;
    canvas.height = H * dpr;
    canvas.style.width = W + 'px';
    canvas.style.height = H + 'px';
    const ctx = canvas.getContext('2d');
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, W, H);
    if (data.length < 2) {
      ctx.fillStyle = cssVar('--text-2');
      ctx.font = '12px sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText(opts.emptyText || '暂无数据', W / 2, H / 2);
      return;
    }
    const padL = 46, padR = 12, padT = 14, padB = 26;
    const iw = W - padL - padR;
    const ih = H - padT - padB;
    if (iw <= 0 || ih <= 0) return;
    let max = 10;
    data.forEach(p => { if (value(p) > max) max = value(p); });
    max = max * 1.15;
    const n = data.length;
    const bw = Math.max(2, (iw / n) * 0.6);
    // 横向网格 + Y 轴刻度
    ctx.strokeStyle = hexToRgba(cssVar('--text-2'), 0.4);
    ctx.fillStyle = cssVar('--text-2');
    ctx.font = '11px sans-serif';
    ctx.textAlign = 'right';
    ctx.lineWidth = 1;
    for (let i = 0; i <= 4; i++) {
      const y = padT + ih - (ih * i) / 4;
      ctx.beginPath();
      ctx.moveTo(padL, y);
      ctx.lineTo(W - padR, y);
      ctx.stroke();
      const v = (max * i) / 4;
      ctx.fillText(v >= 1000 ? fmtInt(v) : String(Math.round(v)), padL - 6, y + 4);
    }
    // 柱体 + X 轴刻度（首/中/尾，画在柱中心）
    ctx.textAlign = 'center';
    [0, 0.5, 1].forEach(f => {
      const idx = Math.min(n - 1, Math.round((n - 1) * f));
      const x = padL + ((idx + 0.5) / n) * iw;
      ctx.fillText(fmtX(data[idx].t), x, H - 8);
    });
    const primary = cssVar('--primary');
    data.forEach((p, i) => {
      const x = padL + ((i + 0.5) / n) * iw - bw / 2;
      const h = (value(p) / max) * ih;
      ctx.fillStyle = primary;
      ctx.globalAlpha = value(p) > 0 ? 0.85 : 0.25;
      ctx.fillRect(x, padT + ih - h, bw, Math.max(value(p) > 0 ? 1 : 0, h));
    });
    ctx.globalAlpha = 1;
  }

  window.Rock.comp.chart = { line, bar, fmtClock, cssVar };
})();