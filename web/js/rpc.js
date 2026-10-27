// Cliente RPC sobre WebSocket con reconexión automática.

class RPC {
  constructor() {
    this.seq = 0;
    this.pending = new Map();
    this.listeners = new Map();
    this.queue = [];
    this.open = false;
    this.backoff = 300;
    this.connect();
  }

  connect() {
    const ws = new WebSocket(`ws://${location.host}/ws`);
    this.ws = ws;
    ws.onopen = () => {
      this.open = true;
      this.backoff = 300;
      for (const msg of this.queue.splice(0)) ws.send(msg);
      this.emit('$open', {});
    };
    ws.onmessage = (e) => {
      let msg;
      try { msg = JSON.parse(e.data); } catch { return; }
      if (msg.id !== undefined && this.pending.has(msg.id)) {
        const p = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        if (msg.e) {
          const err = new Error(msg.e.msg || 'error');
          err.code = msg.e.code;
          p.reject(err);
        } else p.resolve(msg.r);
        return;
      }
      if (msg.ev) this.emit(msg.ev, msg);
    };
    ws.onclose = () => {
      const wasOpen = this.open;
      this.open = false;
      for (const [, p] of this.pending) {
        const err = new Error('Conexión interrumpida');
        err.code = 'disconnected';
        p.reject(err);
      }
      this.pending.clear();
      if (wasOpen) this.emit('$close', {});
      setTimeout(() => this.connect(), this.backoff);
      this.backoff = Math.min(this.backoff * 2, 5000);
    };
  }

  call(method, params = {}) {
    const id = ++this.seq;
    const msg = JSON.stringify({ id, m: method, p: params });
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      if (this.open) this.ws.send(msg);
      else this.queue.push(msg);
    });
  }

  on(ev, fn) {
    if (!this.listeners.has(ev)) this.listeners.set(ev, new Set());
    this.listeners.get(ev).add(fn);
    return () => this.listeners.get(ev).delete(fn);
  }

  emit(ev, msg) {
    recent.push([performance.now(), ev]);
    if (recent.length > 24) recent.shift();
    for (const fn of this.listeners.get(ev) || []) {
      try { fn(msg); } catch (err) { console.error('listener', ev, err); }
    }
  }
}

const recent = []; // últimos eventos del núcleo (para el detector de tirones)

export const rpc = new RPC();

// Detector de tirones: un reloj liviano (dos veces por segundo) mide si la interfaz se trabó.
// Si pasa de 200 ms con la ventana a la vista, lo anota en el registro de Hilvan junto con lo
// que venía pasando, para poder encontrar la causa.
{
  let expected = performance.now() + 500;
  let lastReport = 0;
  setInterval(() => {
    const now = performance.now();
    const late = now - expected;
    expected = now + 500;
    if (late < 200 || document.visibilityState !== 'visible' || now - lastReport < 10000) return;
    lastReport = now;
    const evs = recent.filter(([t]) => now - t < late + 1500).map(([, e]) => e);
    rpc.call('app.jank', { ms: Math.round(late), events: evs.slice(-12), view: (location.hash || '').slice(0, 40) }).catch(() => {});
  }, 500);
}

// Subida de archivos por HTTP (más eficiente que por WebSocket para binarios).
export async function upload(params, blob, onProgress) {
  // El tamaño viaja aparte: el núcleo comprueba que el archivo llegó completo.
  const q = new URLSearchParams({ ...params, ...(blob && blob.size !== undefined && !params.size ? { size: String(blob.size) } : {}) });
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', `/upload?${q}`);
    xhr.responseType = 'json';
    if (onProgress) xhr.upload.onprogress = (e) => e.lengthComputable && onProgress(e.loaded / e.total);
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) resolve(xhr.response);
      else reject(new Error((xhr.response && xhr.response.error) || `Error ${xhr.status}`));
    };
    xhr.onerror = () => reject(new Error('No se pudo subir el archivo'));
    xhr.send(blob);
  });
}
