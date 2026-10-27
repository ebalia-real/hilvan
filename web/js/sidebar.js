// Riel de cuentas/espacios y lista de salas (virtualizada).
import { html, useState, useRef, useEffect, useMemo, useLayoutEffect } from '../vendor/preact-htm.js';
import { useStore, state, update, setView, switchAccount, acct, openModal } from './store.js';
import { t } from './i18n.js';
import { Icon } from './icons.js';
import { Avatar, memo, openMenu, closeMenu, confirmDialog, presenceOf } from './ui.js';
import { formatShort, localpart } from './util.js';
import { rpc } from './rpc.js';
import { CryptoBanner } from './security.js';
import { UpdateBanner } from './update.js';
import { CallBar, toggleMute, toggleDeafen } from './call.js';
import { off } from './policy.js';

export function roomName(r) {
  if (!r) return '';
  if (r.name) return r.name;
  const heroes = (r.heroes || []).map((h) => h.name || localpart(h.id));
  if (heroes.length === 0) return r.membership === 'invite' ? t('Invitación') : t('Sala vacía');
  if (heroes.length === 1) return heroes[0];
  if (heroes.length === 2) return t('{a} y {b}', { a: heroes[0], b: heroes[1] });
  const others = Math.max((r.joined || heroes.length) - 1 - 2, heroes.length - 2);
  return t('{a}, {b} y {n} más', { a: heroes[0], b: heroes[1], n: others });
}

const norm = (s) => (s || '').normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase();

export function isUnread(r) {
  return r.membership === 'invite' || r.unread > 0 || r.marked_unread;
}

// descendientes de un espacio (recursivo, con protección de ciclos).
export function spaceDescendants(rooms, spaceId, seen = new Set()) {
  if (seen.has(spaceId)) return seen;
  seen.add(spaceId);
  const sp = rooms[spaceId];
  if (!sp) return seen;
  for (const ch of sp.children || []) {
    seen.add(ch.id);
    if (rooms[ch.id] && rooms[ch.id].type === 'm.space') spaceDescendants(rooms, ch.id, seen);
  }
  // Salas que declaran a este espacio como padre.
  for (const r of Object.values(rooms)) if ((r.parents || []).includes(spaceId)) seen.add(r.id);
  return seen;
}

// ---------- riel ----------
export function Rail() {
  const accounts = useStore((s) => s.accounts);
  const cur = useStore((s) => s.cur);
  const rooms = useStore((s) => (s.cur && s.acct[s.cur] ? s.acct[s.cur].rooms : null)) || {};
  const space = useStore((s) => s.view.space);
  const gridOn = useStore((s) => !!(s.view.grid && s.view.grid.length));
  const acctsState = useStore((s) => s.acct);
  const spaces = useMemo(() => Object.values(rooms).filter((r) => r.type === 'm.space' && r.membership === 'join')
    .filter((sp) => !(sp.parents || []).some((p) => rooms[p] && rooms[p].type === 'm.space'))
    .sort((a, b) => (a.name || '').localeCompare(b.name || '')), [rooms]);

  const unreadIn = (ids) => {
    let n = 0, hl = 0;
    for (const id of ids) {
      const r = rooms[id];
      if (!r || r.type === 'm.space' || r.notify === 'mute') continue;
      if (r.unread > 0 || r.marked_unread) n++;
      hl += r.highlight || 0;
    }
    return { n, hl };
  };
  const acctUnread = (uid) => {
    const rs = acctsState[uid] ? acctsState[uid].rooms : {};
    let n = 0;
    for (const r of Object.values(rs)) if (r.type !== 'm.space' && r.notify !== 'mute' && (r.unread > 0 || r.membership === 'invite')) n++;
    return n;
  };

  const me = accounts.find((a) => a.user_id === cur);
  return html`<nav class="rail">
    ${accounts.map((a) => {
      const n = a.user_id === cur ? 0 : acctUnread(a.user_id);
      return html`<button class=${`rail-btn${a.user_id === cur ? ' active' : ''}`} title=${a.displayname || a.user_id}
        onClick=${() => switchAccount(a.user_id)} onContextMenu=${(e) => { e.preventDefault(); accountMenu(e, a); }}>
        <${Avatar} a=${a.user_id} mxc=${a.avatar} name=${a.displayname || localpart(a.user_id)} id=${a.user_id} size=${40} square />
        ${n ? html`<span class="badge">${n > 99 ? '99+' : n}</span>` : null}
      </button>`;
    })}
    ${off('multi_account') ? null : html`<button class="rail-btn" title=${t('Agregar cuenta')} onClick=${() => openModal('login')}><${Icon} name="userPlus" /></button>`}
    <div class="rail-sep"></div>
    ${me ? html`
      <button class=${`rail-btn${!space ? ' active' : ''}`} title=${t('Todos los chats')} onClick=${() => setView({ space: null })}>
        <${Icon} name="home" size=22 />
      </button>
      ${spaces.map((sp) => {
        const u = unreadIn(spaceDescendants(rooms, sp.id));
        return html`<button class=${`rail-btn${space === sp.id ? ' active' : ''}`} title=${sp.name}
          onClick=${() => setView({ space: sp.id })} onContextMenu=${(e) => { e.preventDefault(); spaceMenu(e, sp); }}>
          <${Avatar} a=${cur} mxc=${sp.avatar} name=${sp.name} id=${sp.id} size=${40} square />
          ${u.hl ? html`<span class="badge">${u.hl}</span>` : u.n ? html`<span class="badge dot"></span>` : null}
        </button>`;
      })}
      <button class="rail-btn" title=${t('Crear o unirse a un espacio')} onClick=${(e) => openMenu(e.currentTarget, [
        { icon: 'layers', label: t('Crear espacio'), onClick: () => openModal('create', { space: true }) },
        { icon: 'compass', label: t('Explorar salas públicas'), onClick: () => openModal('explore') },
      ])}><${Icon} name="plus" /></button>
    ` : null}
    <div class="rail-spacer"></div>
    <button class=${`rail-btn${gridOn ? ' on' : ''}`} title=${t('Vista en cuadrícula: varias salas a la vez')} onClick=${toggleGrid}><${Icon} name="grid" /></button>
  </nav>`;
}

function accountMenu(e, a) {
  openMenu(e, [
    { icon: 'settings', label: t('Ajustes de la cuenta'), onClick: () => { switchAccount(a.user_id); openModal('settings', { tab: 'account' }); } },
    { icon: 'logout', label: t('Cerrar sesión'), danger: true, onClick: async () => {
      if (await confirmDialog({ title: t('Cerrar sesión'), text: t('¿Cerrar la sesión de {u} en este dispositivo?', { u: a.user_id }), ok: t('Cerrar sesión'), danger: true })) {
        rpc.call('account.logout', { a: a.user_id });
      }
    } },
  ]);
}

function spaceMenu(e, sp) {
  openMenu(e, [
    { icon: 'compass', label: t('Explorar el espacio'), onClick: () => openModal('space', { space: sp.id }) },
    { icon: 'plus', label: t('Crear sala en el espacio'), onClick: () => openModal('create', { parent: sp.id }) },
    { icon: 'userPlus', label: t('Invitar'), onClick: () => openModal('invite', { room: sp.id }) },
    { icon: 'settings', label: t('Ajustes del espacio'), onClick: () => openModal('roomSettings', { room: sp.id }) },
    '-',
    { icon: 'door', label: t('Salir del espacio'), danger: true, onClick: () => leaveRoom(sp.id) },
  ]);
}

export async function leaveRoom(room) {
  const r = acct().rooms[room];
  if (!(await confirmDialog({ title: t('Salir'), text: t('¿Salir de {r}?', { r: roomName(r) }), ok: t('Salir'), danger: true }))) return;
  try {
    await rpc.call('room.leave', { a: state.cur, room, forget: true });
    if (state.view.room === room) setView({ room: null });
  } catch (e) { (await import('./store.js')).toast(e.message, 'err'); }
}

// ---------- lista de salas ----------
const ROW_H = 58, LABEL_H = 30;

export function RoomList() {
  const cur = useStore((s) => s.cur);
  const ac = useStore((s) => (s.cur ? s.acct[s.cur] : null));
  const rooms = (ac && ac.rooms) || {};
  const drafts = (ac && ac.drafts) || {};
  const space = useStore((s) => s.view.space);
  const active = useStore((s) => s.view.room);
  const accounts = useStore((s) => s.accounts);
  const roomSort = useStore((s) => (s.settings.ui && s.settings.ui.roomSort) || 'activity');
  const [filter, setFilter] = useState(() => localStorage.getItem('hv.filter') || 'all');
  const [query, setQuery] = useState('');
  const searchRef = useRef();
  const me = accounts.find((a) => a.user_id === cur);

  useEffect(() => {
    const onKey = (e) => {
      if ((e.ctrlKey || e.metaKey) && (e.key === 'k' || e.key === 'K')) { e.preventDefault(); searchRef.current && searchRef.current.focus(); }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  const rows = useMemo(() => {
    let list = Object.values(rooms).filter((r) => r.membership !== 'leave' && r.type !== 'm.space');
    if (space) {
      const ids = spaceDescendants(rooms, space);
      list = list.filter((r) => ids.has(r.id));
    }
    const q = norm(query.trim());
    if (q) list = list.filter((r) => norm(roomName(r)).includes(q) || norm(r.alias).includes(q) || norm(r.dm).includes(q));
    else if (filter === 'unread') list = list.filter((r) => isUnread(r) || r.id === active);
    else if (filter === 'people') list = list.filter((r) => r.dm);
    else if (filter === 'groups') list = list.filter((r) => !r.dm);
    else if (filter === 'fav') list = list.filter((r) => (r.tags || []).includes('m.favourite'));
    const sortMode = (state.settings.ui && state.settings.ui.roomSort) || 'activity';
    const byTime = sortMode === 'alpha' ? (a, b) => roomName(a).localeCompare(roomName(b))
      : sortMode === 'unread' ? (a, b) => (isUnread(b) - isUnread(a)) || (b.last_ts || 0) - (a.last_ts || 0)
      : (a, b) => (b.last_ts || 0) - (a.last_ts || 0);
    const invites = list.filter((r) => r.membership === 'invite' || r.membership === 'knock').sort(byTime);
    const joined = list.filter((r) => r.membership === 'join');
    const fav = filter === 'all' && !q ? joined.filter((r) => (r.tags || []).includes('m.favourite')).sort(byTime) : [];
    const low = !q ? joined.filter((r) => (r.tags || []).includes('m.lowpriority')).sort(byTime) : [];
    const favSet = new Set(fav.map((r) => r.id)), lowSet = new Set(low.map((r) => r.id));
    const rest = joined.filter((r) => !favSet.has(r.id) && !lowSet.has(r.id)).sort(byTime);
    const out = [];
    const section = (label, arr) => {
      if (!arr.length) return;
      if (label) out.push({ label });
      for (const r of arr) out.push({ room: r });
    };
    section(invites.length ? t('Invitaciones') : null, invites);
    section(fav.length ? t('Favoritos') : null, fav);
    section(fav.length || invites.length ? (low.length ? t('Chats') : t('Chats')) : null, rest);
    section(low.length ? t('Baja prioridad') : null, low);
    return out;
  }, [rooms, space, filter, query, active, roomSort]);

  // Virtualización con alturas fijas.
  const scRef = useRef();
  const [vp, setVp] = useState({ top: 0, h: 800 });
  const offsets = useMemo(() => {
    let y = 0;
    return rows.map((r) => { const o = y; y += r.label ? LABEL_H : ROW_H; return o; });
  }, [rows]);
  const total = rows.length ? offsets[offsets.length - 1] + (rows[rows.length - 1].label ? LABEL_H : ROW_H) : 0;
  useLayoutEffect(() => {
    const el = scRef.current;
    if (!el) return;
    const upd = () => setVp({ top: el.scrollTop, h: el.clientHeight });
    upd();
    el.addEventListener('scroll', upd, { passive: true });
    const ro = new ResizeObserver(upd);
    ro.observe(el);
    return () => { el.removeEventListener('scroll', upd); ro.disconnect(); };
  }, []);
  const overscan = 600;
  const visible = [];
  for (let i = 0; i < rows.length; i++) {
    const top = offsets[i];
    if (top + ROW_H < vp.top - overscan || top > vp.top + vp.h + overscan) continue;
    visible.push(i);
  }

  const spaceRoom = space ? rooms[space] : null;
  const setF = (f) => { setFilter(f); localStorage.setItem('hv.filter', f); };
  const onEnter = (e) => {
    if (e.key === 'Enter') {
      const first = rows.find((r) => r.room);
      if (first) { setView({ room: first.room.id }); setQuery(''); e.currentTarget.blur(); }
      else if (query.startsWith('@') && query.includes(':')) startDM(query.trim());
      else if ((query.startsWith('#') || query.startsWith('!')) && query.includes(':')) openModal('join', { target: query.trim() });
    } else if (e.key === 'Escape') { setQuery(''); e.currentTarget.blur(); }
  };

  return html`<aside class="list">
    <div class="list-head">
      <div class="list-title">
        <h1>${spaceRoom ? spaceRoom.name : (me ? (me.displayname || localpart(me.user_id)) : 'Hilvan')}</h1>
        <button class="icon-btn" title=${t('Nuevo chat')} onClick=${(e) => openMenu(e.currentTarget, [
          { icon: 'users', label: t('Nuevo chat directo'), onClick: () => openModal('newdm') },
          { icon: 'hash', label: t('Nueva sala'), onClick: () => openModal('create', { parent: space || '' }) },
          { icon: 'compass', label: t('Unirse o explorar salas'), onClick: () => openModal('explore') },
          { icon: 'bridge', label: t('Conectar otras redes'), onClick: () => openModal('bridges') },
        ], { align: 'end' })}><${Icon} name="edit" /></button>
      </div>
      <label class="search"><${Icon} name="search" size=17 />
        <input ref=${searchRef} placeholder=${t('Buscar')} value=${query} onInput=${(e) => setQuery(e.currentTarget.value)} onKeyDown=${onEnter} />
        ${query ? html`<button class="icon-btn sm" onClick=${() => setQuery('')}><${Icon} name="close" size=16 /></button>` : html`<kbd>Ctrl K</kbd>`}
      </label>
      ${!query ? html`<div class="chips">
        ${[['all', t('Todos')], ['unread', t('No leídos')], ['people', t('Personas')], ['groups', t('Grupos')], ['fav', t('Favoritos')]].map(([k, l]) =>
          html`<button class=${`chip${filter === k ? ' on' : ''}`} onClick=${() => setF(k)}>${l}</button>`)}
      </div>` : null}
    </div>
    <${StatusStrip} ac=${ac} />
    <${UpdateBanner} />
    <${CryptoBanner} />
    <div class="rooms" ref=${scRef}>
      ${rows.length === 0 ? html`<div class="empty">${!ac || !ac.ready ? t('Sincronizando…') : query ? t('Sin resultados. Probá con @usuario:servidor o #sala:servidor') : t('No hay chats aquí todavía.')}</div>` : null}
      <div class="rooms-inner" style=${`height:${total}px`}>
        ${visible.map((i) => {
          const row = rows[i];
          if (row.label) return html`<div key=${`l${row.label}`} class="section-label" style=${`position:absolute;left:6px;right:6px;top:${offsets[i]}px;height:${LABEL_H}px`}>${row.label}</div>`;
          return html`<${RoomRow} key=${row.room.id} room=${row.room} top=${offsets[i]} active=${row.room.id === active}
            me=${cur} draft=${drafts[row.room.id]} presence=${row.room.dm ? presenceOf(ac.presence[row.room.dm]) : null} />`;
        })}
      </div>
    </div>
    <${CallBar} />
    ${me ? html`<${MePanel} me=${me} />` : null}
  </aside>`;
}

// ---------- panel de usuario y estado ----------
const STATUS = () => [
  ['online', t('En línea'), ''],
  ['away', t('Ausente'), ''],
  ['dnd', t('No molestar'), t('No recibirás notificaciones')],
  ['invisible', t('Invisible'), t('Aparecés desconectado, pero podés usar Hilvan normalmente')],
];

export function statusLabel(p) {
  const s = STATUS().find((x) => x[0] === p);
  return s ? s[1] : t('En línea');
}

function MePanel({ me }) {
  const voice = useStore((s) => s.voice) || {};
  const status = me.presence || 'online';
  const statusText = me.status_msg || statusLabel(status);
  return html`<div class="me-panel">
    <button class="me-btn" onClick=${(e) => openMenu(e.currentTarget, null, { render: () => html`<${StatusMenu} me=${me} />` })} title=${t('Cambiar estado')}>
      <${Avatar} a=${me.user_id} mxc=${me.avatar} name=${me.displayname || localpart(me.user_id)} id=${me.user_id} size=${34} presence=${status} />
      <div style="min-width:0"><div class="me-name">${me.displayname || localpart(me.user_id)}</div><div class="me-status">${statusText}</div></div>
    </button>
    <button class=${`icon-btn${voice.muted ? ' off' : ''}`} title=${voice.muted ? t('Activar micrófono') : t('Silenciar micrófono')} onClick=${toggleMute}>
      <${Icon} name=${voice.muted ? 'micOff' : 'mic'} size=19 /></button>
    <button class=${`icon-btn${voice.deafened ? ' off' : ''}`} title=${voice.deafened ? t('Dejar de ensordecer') : t('Ensordecer')} onClick=${toggleDeafen}>
      <${Icon} name=${voice.deafened ? 'headphonesOff' : 'headphones'} size=19 /></button>
    <button class="icon-btn" title=${t('Ajustes')} onClick=${() => openModal('settings')}><${Icon} name="settings" size=19 /></button>
  </div>`;
}

function StatusMenu({ me }) {
  const set = (status, extra = {}) => {
    closeMenu();
    rpc.call('status.set', { a: me.user_id, status, message: me.status_msg || '', until: me.status_until || 0, ...extra }).catch((e) => import('./store.js').then((m) => m.toast(e.message, 'err')));
  };
  const custom = async () => {
    closeMenu();
    const msg = await confirmDialog({ title: t('Mensaje de estado'), input: 'text', value: me.status_msg || '', placeholder: t('¿Qué estás haciendo?'), ok: t('Siguiente') });
    if (msg === null) return;
    const dur = await new Promise((resolve) => openMenu({ left: window.innerWidth / 2 - 100, right: window.innerWidth / 2 + 100, top: window.innerHeight / 2 - 120, bottom: window.innerHeight / 2 - 120 }, [
      { label: t('No borrar'), onClick: () => resolve(0) },
      { label: t('30 minutos'), onClick: () => resolve(30 * 60000) },
      { label: t('1 hora'), onClick: () => resolve(3600000) },
      { label: t('4 horas'), onClick: () => resolve(4 * 3600000) },
      { label: t('Hoy'), onClick: () => { const d = new Date(); d.setHours(23, 59, 59, 0); resolve(d.getTime() - Date.now()); } },
    ]));
    rpc.call('status.set', { a: me.user_id, status: me.presence || 'online', message: msg.trim(), until: dur ? Date.now() + dur : 0 });
  };
  const current = me.presence || 'online';
  return html`<div class="status-menu">
    ${STATUS().map(([k, l, sub]) => html`<button class="menu-item" onClick=${() => set(k)}>
      <span class=${`dot presence ${k === 'invisible' ? 'offline' : k}`} style="position:relative;right:auto;bottom:auto;border-color:var(--bg)"></span>
      <span class="mi-text">${l}${sub ? html`<span class="mi-sub">${sub}</span>` : null}</span>${current === k ? html`<${Icon} name="check" size=16 />` : null}</button>`)}
    <div class="rail-sep" style="width:auto;margin:4px 8px"></div>
    <button class="menu-item" onClick=${custom}><${Icon} name="smile" size=18 /><span class="mi-text">${me.status_msg ? t('Editar mensaje de estado') : t('Definir mensaje de estado')}</span></button>
    ${me.status_msg ? html`<button class="menu-item" onClick=${() => set(current, { message: '', until: 0 })}><${Icon} name="close" size=18 /><span class="mi-text">${t('Borrar mensaje de estado')}</span></button>` : null}
  </div>`;
}

function StatusStrip({ ac }) {
  const connected = useStore((s) => s.connected);
  if (!connected) return html`<div class="status-strip err"><${Icon} name="refresh" size=14 /> ${t('Reconectando con Hilvan…')}</div>`;
  if (!ac) return null;
  if (ac.status === 'logged_out') return html`<div class="status-strip err">${t('La sesión se cerró en el servidor.')}
    <button class="btn ghost" style="height:24px;padding:0 8px" onClick=${() => openModal('login')}>${t('Volver a entrar')}</button></div>`;
  if (ac.status === 'offline') return html`<div class="status-strip err"><${Icon} name="refresh" size=14 /> ${t('Sin conexión con el servidor; reintentando…')}</div>`;
  if (!ac.ready) return html`<div class="status-strip"><div class="spinner sm"></div> ${t('Sincronizando…')}</div>`;
  return null;
}

async function startDM(user) {
  try {
    const r = await rpc.call('dm.open', { a: state.cur, user });
    setView({ room: r.room });
  } catch (e) { (await import('./store.js')).toast(e.message, 'err'); }
}

function previewText(r, me) {
  const p = r.preview;
  if (r.membership === 'invite') return r.inviter_name ? t('{u} te invitó', { u: r.inviter_name }) : t('Invitación pendiente');
  if (!p) return r.topic || '';
  const who = p.sender === me ? t('Vos') : (r.dm ? '' : (p.name || localpart(p.sender)));
  let body;
  if (p.utd) body = `🔒 ${t('Mensaje cifrado')}`;
  else if (p.type === 'm.sticker') body = t('Sticker');
  else if ((p.type || '').endsWith('poll.start')) body = `📊 ${t('Encuesta')}`;
  else if (p.type === 'm.call.invite') body = `📞 ${t('Llamada')}`;
  else switch (p.msgtype) {
    case 'm.image': body = `📷 ${t('Imagen')}`; break;
    case 'm.video': body = `🎬 ${t('Video')}`; break;
    case 'm.audio': body = `🎤 ${t('Audio')}`; break;
    case 'm.file': body = `📎 ${p.body}`; break;
    case 'io.hilvan.multipart': body = `📎 ${p.filename || String(p.body || '').replace(/^📦 /, '').split(' (')[0]}`; break;
    default: body = p.body || '';
  }
  return who ? `${who}: ${body}` : body;
}

const RoomRow = memo(function RoomRow({ room: r, top, active, me, draft, presence }) {
  const unread = isUnread(r);
  const muted = r.notify === 'mute';
  const name = roomName(r);
  const draftText = draft && typeof draft === 'object' && draft.text ? draft.text : (typeof draft === 'string' ? draft : '');
  return html`<div class=${`room-row${active ? ' active' : ''}${unread && !muted ? ' unread' : ''}`} style=${`top:${top}px`}
    onClick=${() => setView({ room: r.id })} onContextMenu=${(e) => { e.preventDefault(); roomMenu(e, r); }}>
    <${Avatar} a=${me} mxc=${r.avatar} name=${name} id=${r.dm || r.id} size=${42} presence=${presence} />
    <div class="meta">
      <div class="line1"><span class="name">${name}</span>
        ${r.bridge ? html`<span title=${r.bridge.name || r.bridge.protocol} class="muted"><${Icon} name="bridge" size=14 /></span>` : null}
        ${r.call && r.call.length ? html`<span class="shield-ok" title=${t('Llamada en curso')}><${Icon} name="phone" size=14 /></span>` : null}
        <span class="time">${formatShort(r.last_ts)}</span></div>
      <div class="line2">
        <span class="preview">${draftText && !active ? html`<span style="color:var(--danger)">${t('Borrador')}: </span>${draftText}` : previewText(r, me)}</span>
        ${muted ? html`<${Icon} name="bellOff" size=14 />` : null}
        ${r.membership === 'invite' ? html`<span class="count">1</span>`
          : r.highlight > 0 ? html`<span class="count hl">${r.highlight > 99 ? '99+' : r.highlight}</span>`
          : r.unread > 0 && r.notify !== 'mentions' && !muted ? html`<span class="count">${r.unread > 99 ? '99+' : r.unread}</span>`
          : (r.unread > 0 || r.marked_unread) ? html`<span class="count muted dot"></span>` : null}
      </div>
    </div>
  </div>`;
});

// Vista en cuadrícula (como Element Pro): seguir y responder en varias salas a la vez.
export function addToGrid(id) {
  const g = (state.view.grid || []).filter((x) => x !== id);
  if (g.length >= 6) g.shift();
  setView({ grid: [...g, id] });
}

function toggleGrid() {
  if (state.view.grid && state.view.grid.length) { setView({ grid: null }); return; }
  const ac = acct();
  const recent = ac ? Object.values(ac.rooms).filter((r) => r.membership === 'join' && r.type !== 'm.space').sort((x, y) => y.last_ts - x.last_ts).map((r) => r.id) : [];
  const first = state.view.room ? [state.view.room, ...recent.filter((x) => x !== state.view.room)] : recent;
  setView({ grid: first.slice(0, 4) });
}

export function roomMenu(e, r) {
  const fav = (r.tags || []).includes('m.favourite');
  const low = (r.tags || []).includes('m.lowpriority');
  const a = state.cur;
  openMenu(e, [
    { icon: 'unread', label: isUnread(r) ? t('Marcar como leído') : t('Marcar como no leído'), onClick: () => (isUnread(r) ? rpc.call('room.read', { a, room: r.id, id: '' }) : rpc.call('room.unread', { a, room: r.id, unread: true })) },
    { icon: 'star', label: fav ? t('Quitar de favoritos') : t('Agregar a favoritos'), onClick: () => rpc.call('room.tag', { a, room: r.id, tag: 'm.favourite', on: !fav }) },
    { icon: 'arrowDown', label: low ? t('Quitar baja prioridad') : t('Baja prioridad'), onClick: () => rpc.call('room.tag', { a, room: r.id, tag: 'm.lowpriority', on: !low }) },
    { icon: r.notify === 'mute' ? 'bell' : 'bellOff', label: r.notify === 'mute' ? t('Activar notificaciones') : t('Silenciar'), onClick: () => rpc.call('room.notify', { a, room: r.id, mode: r.notify === 'mute' ? 'default' : 'mute' }) },
    { icon: 'grid', label: t('Abrir en la cuadrícula'), onClick: () => addToGrid(r.id) },
    '-',
    { icon: 'door', label: t('Salir'), danger: true, onClick: () => leaveRoom(r.id) },
  ]);
}
