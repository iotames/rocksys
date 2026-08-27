/* ==========================================================================
 * RockSys 管理控制台 - components/logStream.js SSE 实时流客户端
 * fetch + ReadableStream 实现（支持自定义请求头/鉴权，无 EventSource 限制）。
 * 业务无关：URL / 请求头 / 帧回调 / 鉴权 / 错误与重连策略均由调用方注入。
 * create(opts) → { start(), stop(), isRunning() }
 * opts:
 *   url            流地址
 *   headers()      动态请求头（如带 Token）
 *   onFrame(lines) 每个 SSE 事件（data: 行数组）
 *   onAuth()       401 时调用（凭证失效，不自动重连）
 *   onError(e)     非中止错误
 *   onStateChange(running)
 *   shouldReconnect()  意外断开后是否自动重连
 *   reconnectMs        重连间隔（默认 2000ms）
 * 挂载到全局命名空间 window.Rock.comp.logStream。
 * ========================================================================== */
(function () {
  'use strict';

  window.Rock = window.Rock || {};
  window.Rock.comp = window.Rock.comp || {};

  function create(opts) {
    opts = opts || {};
    let seq = 0;      // 流代际：重连/停止时 +1，防止旧流回调污染
    let ctl = null;   // 当前 AbortController
    let running = false;

    function isRunning() { return running; }

    function start() {
      if (running) return;
      seq++;
      const mySeq = seq;
      const ac = new AbortController();
      ctl = ac;
      running = true;
      if (opts.onStateChange) opts.onStateChange(true);
      (async function () {
        try {
          const res = await fetch(opts.url, {
            headers: (opts.headers && opts.headers()) || {},
            cache: 'no-store',
            signal: ac.signal,
          });
          if (res.status === 401) {
            if (opts.onAuth) opts.onAuth();
            throw new Error('__auth__');
          }
          if (!res.ok || !res.body) throw new Error('HTTP ' + res.status);
          const reader = res.body.getReader();
          const decoder = new TextDecoder();
          let buf = '';
          for (;;) {
            const part = await reader.read();
            if (part.done) break;
            buf += decoder.decode(part.value, { stream: true });
            // SSE 事件以空行分隔；data: 前缀为有效行，: ping 为心跳（忽略）
            let idx;
            while ((idx = buf.indexOf('\n\n')) >= 0) {
              const frame = buf.slice(0, idx);
              buf = buf.slice(idx + 2);
              if (mySeq !== seq) return;
              const lines = frame.split('\n')
                .filter(function (l) { return l.indexOf('data:') === 0; })
                .map(function (l) { return l.slice(5); });
              if (lines.length && opts.onFrame) opts.onFrame(lines);
            }
          }
        } catch (e) {
          if (mySeq !== seq) return;
          if (e && e.name === 'AbortError') return;
          if (e && e.message === '__auth__') {
            running = false;
            if (opts.onStateChange) opts.onStateChange(false);
            return;
          }
          if (opts.onError) opts.onError(e);
        } finally {
          if (mySeq === seq) {
            running = false;
            if (opts.onStateChange) opts.onStateChange(false);
            if (opts.reconnectMs && opts.shouldReconnect && opts.shouldReconnect()) {
              setTimeout(start, opts.reconnectMs);
            }
          }
        }
      })();
    }

    function stop() {
      seq++;
      running = false;
      if (ctl) { try { ctl.abort(); } catch (e) { /* ignore */ } }
      ctl = null;
      if (opts.onStateChange) opts.onStateChange(false);
    }

    return { start: start, stop: stop, isRunning: isRunning };
  }

  window.Rock.comp.logStream = { create: create };
})();
