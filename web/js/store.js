// Estado global de la interfaz con suscripciones selectivas (re-render mínimo).
import { useState, useEffect, useRef } from '../vendor/preact-htm.js';
import { rpc } from './rpc.js';
import { t as t0 } from './i18n.js';

export const HIDDEN_TYPES = new Set([
  'm.room.redaction', 'm.reaction', 'm.call.candidates', 'm.call.answer', 'm.call.select_answer', 'm.call.negotiate',
  'm.call.sdp_stream_metadata_changed', 'org.matrix.call.sdp_stream_metadata_changed', 'm.call.reject',
  'org.matrix.msc3401.call.member', 'm.call.member', 'im.ponies.room_emotes', 'm.room.image_pack',
  'org.matrix.msc3381.poll.response', 'm.poll.response', 'm.room.encryption.rotation',
]);

const MAX_ITEMS = 600; // tope de elementos en memoria por sala (fluidez del scroll)

function load(key, def) {
  try { const v = localStorage.getItem(key); return v === null ? def : JSON.parse(v); } catch { return def; }
}
export function save(key, v) {
  try { localStorage.setItem(key, JSON.stringify(v)); } catch { /* almacenamiento no disponible */ }
}

export const state = {
  connected: false,
  booted: false,
  info: null,
  settings: {},
  accounts: [],
  cur: load('hv.cur', null),
  acct: {},
  tl: {},
  threads: {},
  view: { room: load('hv.room', null), space: null, panel: load('hv.panel', null), thread: null, highlight: null, grid: load('hv.grid', null) },
  verify: {},
  toasts: [],
  modal: null,
  calls: {},
  transfers: {},
  voice: load('hv.voice', {}),
  callFull: false,
  lastRoom: load('hv.lastRoom', {}),
};

const subs = new Set();
let scheduled = false;

function notify() {
  if (scheduled) return;
  scheduled = true;
  queueMicrotask(() => {
    scheduled = false;
    for (const fn of [...subs]) fn();
  });
}

export function update(fn) {
  fn(state);
  notify();
}

// useStore(selector): re-renderiza solo si el valor seleccionado cambia (por referencia).
export function useStore(sel) {
  const [, force] = useState(0);
  const selRef = useRef(sel);
  selRef.current = sel;
  const valRef = useRef();
  valRef.current = sel(state);
  useEffect(() => {
    const fn = () => {
      const v = selRef.current(state);
      if (!Object.is(v, valRef.current)) {
        valRef.current = v;
        force((x) => x + 1);
      }
    };
    subs.add(fn);
    fn();
    return () => subs.delete(fn);
  }, []);
  return valRef.current;
}

export const tlKey = (a, room) => `${a}\n${room}`;

// mutAcct aplica cambios a una cuenta creando un objeto nuevo (para que los selectores lo detecten).
export function mutAcct(a, fn) {
  update(() => {
    const next = { ...acct(a) };
    fn(next);
    state.acct[a] = next;
  });
}

export function acct(a = state.cur) {
  if (!a) return null;
  if (!state.acct[a]) {
    state.acct[a] = {
      rooms: {}, ready: false, status: 'connecting', statusErr: '', mode: '', drafts: {}, scheduled: [],
      ad: {}, crypto: null, typing: {}, presence: {}, receipts: {}, loaded: false,
    };
  }
  return state.acct[a];
}

export function toast(text, kind = '') {
  const id = Math.random().toString(36).slice(2);
  update((s) => { s.toasts = [...s.toasts, { id, text, kind }]; });
  setTimeout(() => update((s) => { s.toasts = s.toasts.filter((t) => t.id !== id); }), kind === 'err' ? 6000 : 3500);
}

export function openModal(type, props = {}) {
  update((s) => { s.modal = { type, props }; });
}
export function closeModal() {
  update((s) => { s.modal = null; });
}

export function setView(patch) {
  update((s) => {
    s.view = { ...s.view, ...patch };
    if ('room' in patch) {
      save('hv.room', s.view.room);
      if (s.cur && patch.room) {
        s.lastRoom = { ...s.lastRoom, [s.cur]: patch.room };
        save('hv.lastRoom', s.lastRoom);
      }
    }
    if ('panel' in patch) save('hv.panel', s.view.panel);
    if ('grid' in patch) save('hv.grid', s.view.grid);
  });
  if ('room' in patch) reportFocus();
}

export function switchAccount(uid) {
  update((s) => {
    s.cur = uid;
    save('hv.cur', uid);
    s.view = { ...s.view, room: s.lastRoom[uid] || null, space: null, thread: null };
    if (s.view.panel === 'thread') s.view.panel = null;
  });
  ensureRooms(uid);
  reportFocus();
}

// --- foco (para que el núcleo no notifique lo que ya se ve) ---
let lastFocus = '';
export function reportFocus() {
  const f = { account: state.cur || '', room: state.view.room || '', window: document.hasFocus() && document.visibilityState === 'visible' };
  const k = JSON.stringify(f);
  if (k === lastFocus) return;
  lastFocus = k;
  rpc.call('focus.set', f).catch(() => {});
}
window.addEventListener('focus', reportFocus);
window.addEventListener('blur', reportFocus);
document.addEventListener('visibilitychange', reportFocus);

// --- carga de datos ---

export async function ensureRooms(a) {
  if (!a) return;
  const ac = acct(a);
  if (ac.loaded || ac.loading) return;
  ac.loading = true;
  void ac;
  try {
    const r = await rpc.call('rooms.list', { a });
    mutAcct(a, (x) => {
      const rooms = {};
      for (const room of r.rooms || []) rooms[room.id] = room;
      x.rooms = rooms;
      x.drafts = r.drafts || {};
      x.scheduled = r.scheduled || [];
      x.ad = r.account_data || {};
      x.ready = x.ready || r.ready;
      if (r.status) x.status = r.status;
      x.loaded = true;
      x.loading = false;
    });
    rpc.call('crypto.status', { a }).then((c) => mutAcct(a, (x) => { x.crypto = c; })).catch(() => {});
  } catch (e) {
    console.warn('rooms.list', e);
    acct(a).loading = false;
  }
}

function sortItems(items) {
  return items.sort((x, y) => x.sort - y.sort);
}

// mergeItems integra elementos nuevos/actualizados manteniendo orden y reemplazando ecos locales.
function mergeItems(tl, incoming, isNew) {
  if (!incoming || !incoming.length) return false;
  const items = tl.items.slice();
  const idx = new Map(items.map((it, i) => [it.id, i]));
  const txnIdx = new Map();
  items.forEach((it, i) => { if (it.txn) txnIdx.set(it.txn, i); });
  let changed = false;
  let needSort = false;
  const removed = new Set();
  for (const it of incoming) {
    if (it.in_thread || HIDDEN_TYPES.has(it.type)) continue;
    let i = idx.get(it.id);
    if (i === undefined && it.txn && txnIdx.has(it.txn)) i = txnIdx.get(it.txn);
    if (i !== undefined && !removed.has(i)) {
      if (items[i].sort !== it.sort) needSort = true;
      items[i] = it;
      idx.set(it.id, i);
      changed = true;
    } else if (isNew) {
      // Solo agregamos si encaja después de lo que tenemos o la línea está completa.
      items.push(it);
      idx.set(it.id, items.length - 1);
      changed = true;
      needSort = true;
    }
  }
  if (!changed) return false;
  if (needSort) sortItems(items);
  // Dedupe por id (por si llegó eco y evento a la vez).
  const seen = new Set();
  tl.items = items.filter((it) => (seen.has(it.id) ? false : (seen.add(it.id), true)));
  if (tl.items.length > MAX_ITEMS && tl.atBottom !== false) {
    tl.items = tl.items.slice(tl.items.length - MAX_ITEMS);
    tl.hasMore = true;
  }
  return true;
}

export async function loadTimeline(a, room, older = false) {
  const key = tlKey(a, room);
  let tl = state.tl[key];
  if (tl && (tl.loading || (older && !tl.hasMore))) return;
  if (tl && !older && tl.items.length) return;
  update((s) => {
    tl = s.tl[key] = { items: tl ? tl.items : [], hasMore: true, profiles: tl ? tl.profiles : {}, loading: true, err: '', atBottom: tl ? tl.atBottom : true };
  });
  const before = older && tl.items.length ? tl.items[0].sort : 0;
  try {
    const page = await rpc.call('room.timeline', { a, room, before, limit: older ? 60 : 50 });
    update((s) => {
      const cur = s.tl[key] || tl;
      const known = new Set(cur.items.map((i) => i.id));
      const fresh = (page.items || []).filter((i) => !known.has(i.id) && !HIDDEN_TYPES.has(i.type));
      const items = older ? [...fresh, ...cur.items] : sortItems([...cur.items, ...fresh]);
      s.tl[key] = { ...cur, items, hasMore: page.has_more, loading: false, profiles: { ...cur.profiles, ...page.profiles } };
      if (page.receipts) {
        const ac = { ...acct(a) };
        s.acct[a] = ac;
        const byUser = {};
        for (const [ev, users] of Object.entries(page.receipts)) for (const u of users) byUser[u] = ev;
        ac.receipts = { ...ac.receipts, [room]: { ...(ac.receipts[room] || {}), ...byUser } };
      }
    });
  } catch (e) {
    update((s) => { s.tl[key] = { ...(s.tl[key] || tl), loading: false, err: e.message }; });
  }
}

export async function loadThread(a, room, root) {
  const key = tlKey(a, root);
  update((s) => { s.threads[key] = { ...(s.threads[key] || { items: [], profiles: {} }), loading: true }; });
  try {
    const page = await rpc.call('room.thread', { a, room, root });
    update((s) => { s.threads[key] = { items: page.items || [], profiles: page.profiles || {}, loading: false }; });
  } catch (e) {
    update((s) => { s.threads[key] = { ...(s.threads[key] || {}), loading: false, err: e.message }; });
  }
}

// --- eventos del núcleo ---

function applySync(a, d) {
  const ac = { ...acct(a) };
  state.acct[a] = ac;
  if (d.rooms) {
    const rooms = { ...ac.rooms };
    for (const r of d.rooms) rooms[r.id] = r;
    ac.rooms = rooms;
  }
  if (d.removed) {
    const rooms = { ...ac.rooms };
    for (const [id, why] of Object.entries(d.removed)) {
      delete rooms[id];
      delete state.tl[tlKey(a, id)];
      if (state.cur === a && state.view.room === id) {
        state.view = { ...state.view, room: null };
        if (why === 'kick') toast('Te quitaron de la sala', 'err');
        else if (why === 'ban') toast('Fuiste expulsado de la sala', 'err');
      }
    }
    ac.rooms = rooms;
  }
  for (const id of d.reset || []) {
    const key = tlKey(a, id);
    if (state.tl[key]) {
      delete state.tl[key];
      if (state.cur === a && state.view.room === id) queueMicrotask(() => loadTimeline(a, id));
    }
  }
  if (d.timeline) {
    for (const [room, t] of Object.entries(d.timeline)) {
      const key = tlKey(a, room);
      const tl = state.tl[key];
      const profs = (d.profiles && d.profiles[room]) || {};
      if (tl) {
        const next = { ...tl, profiles: { ...tl.profiles, ...profs } };
        const c1 = mergeItems(next, t.new, true);
        const c2 = mergeItems(next, t.updated, false);
        if (c1 || c2 || Object.keys(profs).length) state.tl[key] = next;
      }
      // Hilos abiertos.
      for (const it of [...(t.new || []), ...(t.updated || [])]) {
        const root = it.in_thread || (it.thread ? it.id : null);
        if (!root) continue;
        const th = state.threads[tlKey(a, root)];
        if (!th) continue;
        const items = th.items.slice();
        const i = items.findIndex((x) => x.id === it.id || (it.txn && x.txn === it.txn));
        if (i >= 0) items[i] = it;
        else if (it.in_thread) items.push(it);
        items.sort((x, y) => (x.id === root ? -1 : y.id === root ? 1 : x.ts - y.ts));
        state.threads[tlKey(a, root)] = { ...th, items, profiles: { ...th.profiles, ...profs } };
      }
    }
  }
  if (d.typing) ac.typing = { ...ac.typing, ...d.typing };
  if (d.receipts) {
    const rec = { ...ac.receipts };
    for (const [room, users] of Object.entries(d.receipts)) {
      const cur = { ...(rec[room] || {}) };
      for (const [u, evs] of Object.entries(users)) cur[u] = evs[0];
      rec[room] = cur;
    }
    ac.receipts = rec;
  }
  if (d.presence) ac.presence = { ...ac.presence, ...d.presence };
  if (d.account_data) ac.ad = { ...ac.ad, ...d.account_data };
}

rpc.on('$open', async () => {
  try {
    const info = await rpc.call('app.info');
    update((s) => {
      s.connected = true;
      s.info = info;
      s.settings = info.settings || {};
      document.title = (info.policy && info.policy.brand && info.policy.brand.name) || 'Hilvan';
      s.accounts = info.accounts || [];
      if (!s.cur || !s.accounts.find((x) => x.user_id === s.cur)) s.cur = s.accounts[0] ? s.accounts[0].user_id : null;
      for (const k of Object.keys(s.acct)) s.acct[k] = { ...s.acct[k], loaded: false, loading: false };
      s.booted = true;
    });
    for (const ac of state.accounts) ensureRooms(ac.user_id);
    // Recargar la sala abierta por si nos perdimos eventos mientras estábamos desconectados.
    if (state.cur && state.view.room) {
      delete state.tl[tlKey(state.cur, state.view.room)];
      loadTimeline(state.cur, state.view.room);
    }
    reportFocus();
  } catch (e) {
    console.error(e);
  }
});
rpc.on('$close', () => update((s) => { s.connected = false; }));
rpc.on('accounts', (m) => update((s) => {
  s.accounts = m.d || [];
  if (!s.cur || !s.accounts.find((x) => x.user_id === s.cur)) {
    s.cur = s.accounts[0] ? s.accounts[0].user_id : null;
    save('hv.cur', s.cur);
  }
  for (const ac of s.accounts) ensureRooms(ac.user_id);
}));
rpc.on('account.status', (m) => mutAcct(m.a, (ac) => {
  ac.status = m.d.status;
  ac.statusErr = m.d.error;
  ac.mode = m.d.mode;
}));
rpc.on('account.ready', (m) => {
  mutAcct(m.a, (ac) => { ac.ready = true; ac.loaded = false; });
  ensureRooms(m.a);
});
rpc.on('account.error', (m) => toast(`${m.a}: ${m.d.error}`, 'err'));
rpc.on('sync', (m) => update(() => applySync(m.a, m.d || {})));
rpc.on('crypto.status', (m) => mutAcct(m.a, (ac) => { ac.crypto = m.d; }));
rpc.on('settings', (m) => update((s) => { s.settings = m.d; }));
rpc.on('policy', (m) => update((s) => {
  s.info = { ...(s.info || {}), policy: m.d };
  document.title = (m.d && m.d.brand && m.d.brand.name) || 'Hilvan';
}));
rpc.on('scheduled', (m) => mutAcct(m.a, (ac) => { ac.scheduled = m.d || []; }));
rpc.on('verify', (m) => update((s) => {
  s.verify = { ...s.verify, [m.d.txn]: { ...m.d, account: m.a } };
}));
// Cuando una verificación termina, la lista de sesiones se actualiza sola; si hay una cola de
// sesiones por verificar, sigue con la próxima.
rpc.on('devices.changed', (m) => {
  update((s) => { s.devicesChanged = (s.devicesChanged || 0) + 1; });
  const q = state.verifyQueue;
  if (!q || q.a !== m.a) return;
  rpc.call('devices.list', { a: m.a }).then((devs) => {
    const left = q.ids.filter((id) => devs.some((d) => d.id === id && (d.trust === 'unverified' || d.trust === 'changed')));
    if (!left.length) { update((s) => { s.verifyQueue = null; }); toast(t0('Sesiones verificadas')); return; }
    if (left.length < q.ids.length) {
      update((s) => { s.verifyQueue = { ...q, ids: left }; });
      setTimeout(() => rpc.call('verify.start', { a: m.a, user: m.a }).catch(() => {}), 600);
    }
  }).catch(() => {});
});
rpc.on('local.removed', (m) => update((s) => {
  const key = tlKey(m.a, m.d.room);
  const tl = s.tl[key];
  if (tl) s.tl[key] = { ...tl, items: tl.items.filter((i) => i.id !== m.d.id) };
}));
rpc.on('identity', (m) => {
  const u = m.d.user;
  if (m.d.kind === 'changed') toast(`🔐 ${u}: ${t0('restableció su identidad criptográfica; se aceptó la nueva automáticamente.')}`);
  else toast(`⚠️ ${u}: ${t0('su identidad verificada cambió. No se le enviarán mensajes cifrados hasta que la verifiques de nuevo.')}`, 'err');
});
rpc.on('send.error', (m) => toast(`No se pudo enviar: ${m.d.error}`, 'err'));
// Progreso de subidas y descargas de archivos (de cualquier tamaño).
rpc.on('transfer', (m) => update((s) => {
  const d = m.d;
  s.transfers = { ...s.transfers, [d.id]: d };
  if (d.state === 'done' || d.state === 'error') {
    setTimeout(() => update((x) => { const tr = { ...x.transfers }; if (tr[d.id] && tr[d.id].state === d.state) delete tr[d.id]; x.transfers = tr; }), d.state === 'error' ? 15000 : 1500);
  }
}));
rpc.on('export', (m) => update((s) => {
  s.exports = { ...(s.exports || {}), [m.d.room]: { ...((s.exports || {})[m.d.room] || {}), ...m.d } };
  if (m.d.state === 'done') toast(`${t0('Chat exportado en')} ${m.d.path}`);
}));
rpc.on('file.saved', (m) => {
  if (m.d.ok) toast(`${t0('Guardado en')} ${m.d.path}`);
  else toast(`${t0('No se pudo guardar')}: ${m.d.error}`, 'err');
});
rpc.on('open', (m) => {
  if (m.a && m.a !== state.cur) switchAccount(m.a);
  if (m.d && m.d.room) setView({ room: m.d.room, space: null });
});
