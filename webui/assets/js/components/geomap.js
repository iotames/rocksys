/* ==========================================================================
 * RockSys 管理控制台 - components/geomap.js 地理热力图组件
 * ECharts 薄封装（vendor/echarts.min.js，全局 window.echarts）：
 *  - 懒加载注册 world / china 两张地图（assets/geo/*.json，ISO2 码 / 中文省名直连）；
 *  - 主题色取自 CSS 变量（与 chart.js 同款主题自适应）；
 *  - 页面侧只调 geoMap.heat(container, scope, data)，不直接触碰 ECharts API，
 *    未来换定制构建或替换实现只改本文件。
 * 挂载到全局命名空间 window.Rock.comp.geoMap。
 *
 * vendor 资产来源（升级时手工替换，npmmirror 为 npm 官方包的国内镜像）：
 *  - assets/vendor/echarts.min.js  v5.6.0，Apache-2.0
 *    https://registry.npmmirror.com/echarts/5.6.0/files/dist/echarts.min.js
 *  - assets/geo/world.json  取自 echarts v4.9.0 包内 map/json/world.json（ECharts 5 起不再随包发行地图）：
 *    https://registry.npmmirror.com/echarts/4.9.0/files/map/json/world.json
 *    本仓库已做预处理：各区划名 name 由英文改为 ISO 3166-1 alpha-2 码（如 United States→US，
 *    原名保留在 name_en），与后端 access_log.country 列直连；
 *  - assets/geo/china.json  取自 echarts v4.9.0 包内 map/json/china.json（省级区划，含九段线）：
 *    https://registry.npmmirror.com/echarts/4.9.0/files/map/json/china.json
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  // 读 CSS 变量（主题自适应，同 chart.js）
  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }
  function hexToRgba(color, alpha) {
    if (/^rgba?\(/.test(color)) return color;
    const m = /^#?([0-9a-fA-F]{6})$/.exec(color);
    if (m) {
      const n = parseInt(m[1], 16);
      return 'rgba(' + ((n >> 16) & 255) + ',' + ((n >> 8) & 255) + ',' + (n & 255) + ',' + alpha + ')';
    }
    return color;
  }

  // 地图注册缓存（会话内一次）；加载失败清除缓存允许重试，reject 交调用方给行内兜底
  const registered = {};
  function ensureMap(scope) {
    if (registered[scope]) return registered[scope];
    registered[scope] = fetch('./assets/geo/' + scope + '.json')
      .then(function (r) {
        if (!r.ok) throw new Error('地图数据 ' + scope + '.json 加载失败（HTTP ' + r.status + '）');
        return r.json();
      })
      .then(function (geojson) {
        // 中国地图为含九段线的省级区划；世界地图区域名已预处理为 ISO2 码，与后端 country 列直连
        echarts.registerMap(scope === 'china' ? 'rock-china' : 'rock-world', geojson);
      })
      .catch(function (err) {
        delete registered[scope]; // 失败不缓存，重试按钮可再次拉取
        throw err;
      });
    return registered[scope];
  }

  // 中国省名映射：city 列存 zh-CN 全称（如「广东省」「内蒙古自治区」），地图 geojson 用短名（「广东」「内蒙古」）
  const chinaShort = {
    '北京市': '北京', '天津市': '天津', '上海市': '上海', '重庆市': '重庆',
    '河北省': '河北', '山西省': '山西', '辽宁省': '辽宁', '吉林省': '吉林', '黑龙江省': '黑龙江',
    '江苏省': '江苏', '浙江省': '浙江', '安徽省': '安徽', '福建省': '福建', '江西省': '江西',
    '山东省': '山东', '河南省': '河南', '湖北省': '湖北', '湖南省': '湖南', '广东省': '广东',
    '海南省': '海南', '四川省': '四川', '贵州省': '贵州', '云南省': '云南', '陕西省': '陕西',
    '甘肃省': '甘肃', '青海省': '青海', '台湾省': '台湾',
    '内蒙古自治区': '内蒙古', '广西壮族自治区': '广西', '西藏自治区': '西藏',
    '宁夏回族自治区': '宁夏', '新疆维吾尔自治区': '新疆',
    '香港特别行政区': '香港', '澳门特别行政区': '澳门',
  };

  // 世界地图 tooltip：ISO2 码 → 中文国名（浏览器内建本地化，失败回落原码）
  let regionNames = null;
  function isoToCn(code) {
    if (!regionNames) {
      try { regionNames = new Intl.DisplayNames(['zh-CN'], { type: 'region' }); } catch (e) { return code; }
    }
    try { return regionNames.of(code) || code; } catch (e) { return code; }
  }

  // 当前实例缓存（按容器，切 scope/source 时复用 setOption 原地切换）
  const instances = new WeakMap();

  // 热力图主入口：container 为 DOM 元素；scope='world'|'china'；
  // data 为 [{name, value}]（世界=ISO2 码，中国=短省名，调用方负责映射）。
  // opts.fmtName 可选：tooltip 展示名映射；opts.mode 提示文案（'访问'|'拦截'）。
  function heat(container, scope, data, opts) {
    opts = opts || {};
    if (typeof echarts === 'undefined') {
      container.innerHTML = '<div class="empty" style="padding:16px">图表库未加载</div>';
      return;
    }
    let chart = instances.get(container);
    if (!chart) {
      chart = echarts.init(container);
      instances.set(container, chart);
      // 容器尺寸变化自适应（侧栏折叠/窗口缩放）
      if (window.ResizeObserver) {
        new ResizeObserver(function () { chart.resize(); }).observe(container);
      }
    }
    const primary = cssVar('--primary') || '#3b82f6';
    const text2 = cssVar('--text-2') || '#94a3b8';
    const border = cssVar('--border') || 'rgba(127,127,127,.3)';
    const max = data.reduce(function (m, d) { return Math.max(m, d.value || 0); }, 0) || 1;
    chart.setOption({
      tooltip: {
        trigger: 'item',
        formatter: function (p) {
          const name = opts.fmtName ? opts.fmtName(p.name) : p.name;
          return esc(name) + '<br>' + (opts.mode || '访问') + '：' + Rock.util.fmtInt(p.value || 0);
        },
      },
      visualMap: {
        min: 0, max: max,
        left: 'left', bottom: 'bottom',
        text: ['多', '少'],
        calculable: false,
        itemHeight: 80,
        textStyle: { color: text2, fontSize: 11 },
        inRange: { color: [hexToRgba(primary, 0.12), primary] },
      },
      series: [{
        type: 'map',
        map: scope === 'china' ? 'rock-china' : 'rock-world',
        roam: false,
        data: data,
        label: { show: false },
        itemStyle: { borderColor: border, borderWidth: 0.5 },
        emphasis: { label: { show: false }, itemStyle: { areaColor: hexToRgba(primary, 0.55) } },
        select: { label: { show: false }, itemStyle: { areaColor: hexToRgba(primary, 0.55) } },
      }],
    });
  }

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  window.Rock.comp.geoMap = {
    ensureMap: ensureMap,
    heat: heat,
    chinaShort: chinaShort,
    isoToCn: isoToCn,
  };
})();
