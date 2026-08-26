/* ==========================================================================
 * RockSys 管理控制台 - views/syslogs.js 运行日志页（系统进程实时日志）
 * 与「访问日志」（HTTP 数据请求日志，/admin/logs 复数）分开：
 *   本页读进程日志 /admin/log/*（单数）——ring buffer 实时监控（SSE 实时流
 *   + HTTP tail 历史）、级别热切、文件存档开关、状态卡。
 * 挂载到全局命名空间 window.Rock.views.syslogs。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.views = window.Rock.views || {};

  const $ = Rock.util.$;
  const esc = Rock.util.esc;
  const fmtBytes = Rock.util.fmtBytes;
  const store = Rock.state.store;
  const api = Rock.api;
  const toast = Rock.ui.toast;
  const noteUpdated = Rock.ui.noteUpdated;

  // 级别 → 展示样式 / 中文名
  const LEVELS = {
    DEBUG: { cls: 'lvl-debug', label: '调试' },
    INFO:  { cls: 'lvl-info',  label: '信息' },
    WARN:  { cls: 'lvl-warn',  label: '警告' },
    ERROR: { cls: 'lvl-error', label: '错误' },
  };
  const LEVEL_OPTIONS = [['DEBUG', 'DEBUG 调试'], ['INFO', 'INFO 信息'], ['WARN', 'WARN 警告'], ['ERROR', 'ERROR 错误']];

  // 页内私有状态
  const st = {
    streaming: false,     // SSE 是否连接中
    paused: false,        // 用户手动暂停（断开 SSE）
    autoScroll: true,     // 新日志到达自动滚到底部
    maxLines: 3000,       // DOM 最大行数，超出丢弃最旧
    streamCtl: null,      // 当前 SSE AbortController
    streamSeq: 0,         // 流代际（重连时 +1，防止旧流回调污染）
  };

  // 日志行解析：默认模板 time={{.time}} level={{.level}} msg={{.msg}}
  // 兼容自定义外挂模板：解析失败则整行作为消息展示（time/level 留空）。
  function parseLine(raw) {
    const line = String(raw || '').replace(/\n$/, '');
    const m = line.match(/time=(.+?)\s+level=(\S+)\s+msg=(.*)$/);
    if (m) return { time: m[1], level: m[2].toUpperCase(), msg: m[3] };
    // 兼容 "time=... level=..." 但无 msg（模板缺省 msg 段）
    const m2 = line.match(/time=(.+?)\s+level=(\S+)\s*$/);
    if (m2) return { time: m2[1], level: m2[2].toUpperCase(), msg: '' };
    return { time: '', level: '', msg: line };
  }

  function lvlBadge(level) {
    const L = LEVELS[level] || { cls: '', label: level || '' };
    return '<span class="lvl ' + L.cls + '">' + esc(L.label || level) + '</span>';
  }

  // 追加日志行到实时区（行级 DOM 增量，超限丢弃最旧；自动滚动）
  function appendLines(lines) {
    const box = $('#syslog-lines');
    if (!box || !lines.length) return;
    const frag = document.createDocumentFragment();
    lines.forEach(l => {
      const div = document.createElement('div');
      div.className = 'syslog-row';
      const p = parseLine(l);
      div.appendChild(elTime(p.time));
      if (p.level) div.appendChild(elBadge(p.level));
      const msg = document.createElement('span');
      msg.className = 'syslog-msg';
      msg.textContent = p.msg;
      div.appendChild(msg);
      frag.appendChild(div);
    });
    box.appendChild(frag);
    // 超限丢弃最旧（批量移除，避免逐条 DOM 删除）
    const rows = box.children;
    while (rows.length > st.maxLines) box.removeChild(rows[0]);
    if (st.autoScroll && !st.paused) box.scrollTop = box.scrollHeight;
  }

  function elTime(t) {
    const s = document.createElement('span');
    s.className = 'syslog-time mono';
    s.textContent = t || '';
    return s;
  }

  function elBadge(level) {
    const s = document.createElement('span');
    const L = LEVELS[level] || { cls: '', label: level || '' };
    s.className = 'lvl ' + L.cls;
    s.textContent = L.label || level || '';
    return s;
  }

  // ========== 状态卡 ==========
  async function loadInfo() {
    try {
      const info = await api.get('/admin/log/info');
      store.syslogInfo = info || null;
      store.syslogInfoError = null;
    } catch (e) {
      store.syslogInfo = null;
      store.syslogInfoError = e.message;
    }
    renderInfo();
  }

  function infoItem(k, v) {
    return '<div class="info-item"><span class="k">' + k + '</span><span class="v">' + v + '</span></div>';
  }

  function renderInfo() {
    const el = $('#syslog-info');
    if (!el) return;
    const info = store.syslogInfo;
    if (store.syslogInfoError) {
      el.innerHTML = '<div class="empty">状态不可用：' + esc(store.syslogInfoError) + '</div>';
      return;
    }
    if (!info) {
      el.innerHTML = '<div class="empty">状态加载中…</div>';
      return;
    }
    const ringPct = info.ring_cap > 0 ? Math.round((info.ring_used / info.ring_cap) * 1000) / 10 : 0;
    el.innerHTML =
      infoItem('当前级别', lvlBadge((info.level || 'INFO').toUpperCase())) +
      infoItem('输出模板', esc(info.template || '')) +
      infoItem('文件存档', info.file_on ? '<span class="tag-ok">已开启</span> · ' + esc(info.file_path || '') : '<span class="tag-off">未开启</span>') +
      infoItem('大小上限', info.max_size_mb === 0 ? '不限制' : info.max_size_mb + ' MB') +
      infoItem('内存缓冲', fmtBytes(info.ring_used) + ' / ' + fmtBytes(info.ring_cap) + '（' + ringPct + '%）');
    // 同步级别下拉与文件开关的当前值（与后端状态一致）
    const lv = $('#syslog-level');
    if (lv && info.level) lv.value = info.level.toUpperCase();
    const fw = $('#syslog-file');
    if (fw) fw.checked = !!info.file_on;
  }

  // ========== 实时流（SSE：fetch + ReadableStream，带 Authorization，无 5s 超时） ==========
  function authHeaders() {
    const h = {};
    const t = api.getToken();
    if (t) h['Authorization'] = 'Bearer ' + t;
    return h;
  }

  async function startStream() {
    if (st.streaming) return;
    st.streamSeq++;
    const seq = st.streamSeq;
    const ac = new AbortController();
    st.streamCtl = ac;
    st.streaming = true;
    setStreamState();
    try {
      const res = await fetch('/admin/log/stream', {
        headers: authHeaders(),
        cache: 'no-store',
        signal: ac.signal,
      });
      if (res.status === 401) {
        // 回环免鉴权通常不会走到；非回环/凭证失效时交给统一认证引导
        Rock.ui.onUnauthorized();
        throw new Error('__auth__');
      }
      if (!res.ok || !res.body) throw new Error('HTTP ' + res.status);
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buf = '';
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        // SSE 事件以空行分隔；data: 前缀为日志行，: ping 为心跳（忽略）
        let idx;
        while ((idx = buf.indexOf('\n\n')) >= 0) {
          const frame = buf.slice(0, idx);
          buf = buf.slice(idx + 2);
          if (seq !== st.streamSeq) return; // 已被新流/停止取代
          const lines = frame.split('\n').filter(l => l.indexOf('data:') === 0).map(l => l.slice(5));
          if (lines.length) appendLines(lines);
        }
      }
    } catch (e) {
      if (seq !== st.streamSeq) return; // 主动停止/换代，忽略错误
      if (e && e.name === 'AbortError') return;
      if (e && e.message === '__auth__') { st.streaming = false; setStreamState(); return; } // 凭证问题交给认证引导，不自动重连
      if (store.syslogPageVisible) {
        toast('实时流已断开，将自动重连', 'warning');
        setStreamState(); // 显示已断开
      }
    } finally {
      if (seq === st.streamSeq) {
        st.streaming = false;
        setStreamState();
        if (store.syslogPageVisible && !st.paused) {
          // 自动重连（非暂停状态下的意外断开）
          setTimeout(startStream, 2000);
        }
      }
    }
  }

  function stopStream() {
    st.streamSeq++;
    st.streaming = false;
    if (st.streamCtl) { try { st.streamCtl.abort(); } catch (e) { /* ignore */ } }
    st.streamCtl = null;
    setStreamState();
  }

  // 拉取历史：首次进入或点击「载入历史」；depth 防御 reset 递归（上限 3 次）
  async function loadHistory(n, depth) {
    depth = depth || 0;
    try {
      const res = await api.get('/admin/log/tail?n=' + (Number(n) || 200));
      const lines = (res && Array.isArray(res.lines)) ? res.lines : [];
      if (lines.length) {
        appendLines(lines);
        if (st.autoScroll) {
          const box = $('#syslog-lines');
          if (box) box.scrollTop = box.scrollHeight;
        }
      }
      // reset 语义：游标已被覆盖 → 以缺省 since 重拉（尾部首拉）
      if (res && res.reset && depth < 3) return loadHistory(n, depth + 1);
      return res;
    } catch (e) {
      if (e.status !== 0) toast('历史日志加载失败：' + e.message, 'error');
      return null;
    }
  }

  // ========== 级别 / 文件通道 ==========
  async function setLevel(level) {
    try {
      await api.post('/admin/log/level')({ level: level });
      toast('日志级别已切换为 ' + level.toUpperCase(), 'success');
      await loadInfo();
    } catch (e) {
      toast('级别切换失败：' + e.message, 'error');
    }
  }

  async function setFile(on) {
    try {
      await api.post('/admin/log/output')({ file: on });
      toast(on ? '文件存档已开启' : '文件存档已关闭', 'success');
      await loadInfo();
    } catch (e) {
      toast('文件存档切换失败：' + e.message, 'error');
      if (!$('#syslog-file').checked) $('#syslog-file').checked = !on;
    }
  }

  // ========== 页面渲染 ==========
  function render() {
    const host = $('#page-syslogs');
    if (!host) return;
    host.innerHTML =
      Rock.comp.head.headHTML({
        title: '系统日志',
        desc: '系统进程实时日志（ring buffer 实时监控），与 HTTP 入网数据日志分开',
        actions:
          '<button class="btn btn-sm" data-act="syslog-clear">清空</button>' +
          '<button class="btn btn-sm" data-act="syslog-history">⟳ 载入历史</button>',
      }) +

      '<div class="syslog-layout">' +
      // 左：实时日志流
      '<div class="card syslog-main">' +
      '<div class="syslog-toolbar">' +
      '<button class="btn btn-sm btn-primary" data-act="syslog-toggle-stream" id="syslog-toggle-btn">▶ 开始实时</button>' +
      '<label class="chk"><input type="checkbox" id="syslog-autoscroll" checked><span>自动滚动</span></label>' +
      '<span class="muted" id="syslog-stream-state"></span>' +
      '<span class="toolbar-spacer"></span>' +
      '<span class="muted">实时区最多保留 ' + st.maxLines + ' 行</span>' +
      '</div>' +
      '<div class="syslog-lines mono" id="syslog-lines"><div class="empty">尚未连接实时流。点击「▶ 开始实时」订阅新日志；或「⟳ 载入历史」拉取最近日志。</div></div>' +
      '</div>' +

      // 右：状态 + 控制
      '<div class="syslog-side">' +
      '<div class="card">' +
      '<div class="card-title">日志状态</div>' +
      '<div class="info-grid" id="syslog-info">' + infoLoading() + '</div>' +
      '</div>' +
      '<div class="card">' +
      '<div class="card-title">运行控制</div>' +
      '<div class="form-row">' +
      '<label class="form-label">日志级别</label>' +
      '<select class="select" id="syslog-level">' + Rock.comp.select.options(LEVEL_OPTIONS, null) + '</select>' +
      '</div>' +
      '<div class="form-hint">热切后立即生效并写回配置（重启保留）</div>' +
      '<div class="form-row">' +
      '<label class="form-label">文件存档</label>' +
      '<input type="checkbox" id="syslog-file" class="switch">' +
      '</div>' +
      '<div class="form-hint">异步落盘，故障不影响实时监控；路径见状态卡</div>' +
      '</div>' +
      '</div>' +
      '</div>';

    // 绑定控件
    const levelSel = $('#syslog-level');
    levelSel.addEventListener('change', () => setLevel(levelSel.value));
    const fileChk = $('#syslog-file');
    fileChk.addEventListener('change', () => setFile(fileChk.checked));
    const auto = $('#syslog-autoscroll');
    auto.addEventListener('change', () => { st.autoScroll = auto.checked; });
    // 手动滚动到顶部时临时停止自动滚动（离开底部再滚回）
    const lines = $('#syslog-lines');
    lines.addEventListener('scroll', () => {
      if (st.autoScroll) {
        const atBottom = lines.scrollHeight - lines.scrollTop - lines.clientHeight < 40;
        if (!atBottom) st.autoScroll = false;
      }
    });

    renderInfo();
    loadInfo();
    setStreamState();
  }

  function infoLoading() {
    return '<div class="empty">状态加载中…</div>';
  }

  // 流状态文案 + 按钮文字
  function setStreamState() {
    const btn = $('#syslog-toggle-btn');
    const state = $('#syslog-stream-state');
    if (btn) btn.textContent = st.streaming ? '⏸ 暂停实时' : (st.paused ? '▶ 恢复实时' : '▶ 开始实时');
    if (state) {
      state.textContent = st.streaming ? '● 实时推送中' : (st.paused ? '已暂停（不接收新日志）' : '未连接');
      state.classList.toggle('stream-on', st.streaming);
    }
  }

  function toggleStream() {
    if (st.streaming) {
      st.paused = true;
      stopStream();
    } else {
      st.paused = false;
      startStream();
    }
  }

  function clearLines() {
    const box = $('#syslog-lines');
    if (box) {
      box.innerHTML = '';
      const d = document.createElement('div');
      d.className = 'empty';
      d.textContent = st.streaming ? '实时流继续推送中…' : '已清空。点击「▶ 开始实时」或「⟳ 载入历史」获取日志。';
      box.appendChild(d);
    }
  }

  // 页面加载（路由进入时调用）
  // 首次进入渲染完整页面 + 拉历史 + 开始实时；再次进入仅刷新状态 + 恢复实时流，
  // 避免重建 DOM 丢失已累积的日志行。
  function load(opts) {
    const host = $('#page-syslogs');
    const first = !host || !host.innerHTML.trim();
    store.syslogPageVisible = true;
    st.paused = false;
    if (first) {
      render();
    } else {
      renderInfo();
      loadInfo();
    }
    if (opts && opts.autoStart !== false) {
      if (first) loadHistory(200);
      startStream();
    }
    noteUpdated();
  }

  // 页面离开时（main.js 路由切换）关闭流，避免泄漏
  function leave() {
    store.syslogPageVisible = false;
    st.paused = true;
    stopStream();
  }

  window.Rock.views.syslogs = {
    load,
    leave,
    render,
    renderInfo,
    loadInfo,
    loadHistory,
    startStream,
    stopStream,
    toggleStream,
    setLevel,
    setFile,
    clearLines,
    parseLine,
    LEVEL_OPTIONS,
  };
})();
