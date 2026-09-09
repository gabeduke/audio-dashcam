// WebSocket client for the live level stream, with backoff reconnect.
//
// The server pushes real min/max peak bins (~100/s) rather than a single RMS
// scalar, which is what makes an actual waveform drawable on the client.

export function connectLive({ onFrame, onOpen, onClose }) {
  let ws = null;
  let backoff = 500;
  let closed = false;
  let timer = null;

  function url() {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    return `${proto}//${location.host}/api/live`;
  }

  function open() {
    if (closed) return;
    try {
      ws = new WebSocket(url());
    } catch {
      return schedule();
    }

    ws.onopen = () => {
      backoff = 500;
      onOpen?.();
    };

    ws.onmessage = (ev) => {
      let f;
      try {
        f = JSON.parse(ev.data);
      } catch {
        return;
      }
      onFrame?.(f);
    };

    ws.onclose = () => {
      onClose?.();
      schedule();
    };

    ws.onerror = () => {
      try { ws.close(); } catch {}
    };
  }

  function schedule() {
    if (closed || timer) return;
    timer = setTimeout(() => {
      timer = null;
      open();
    }, backoff);
    backoff = Math.min(backoff * 1.8, 10000);
  }

  open();

  // A phone suspends sockets on lock; reconnect as soon as we are visible.
  const onVis = () => {
    if (!document.hidden && (!ws || ws.readyState > WebSocket.OPEN)) {
      backoff = 500;
      open();
    }
  };
  document.addEventListener('visibilitychange', onVis);

  return {
    close() {
      closed = true;
      document.removeEventListener('visibilitychange', onVis);
      clearTimeout(timer);
      try { ws?.close(); } catch {}
    },
  };
}
