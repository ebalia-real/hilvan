// Cabecera de sala, panel derecho y tarjeta de usuario.
import { html, useState, useEffect, useMemo, useRef } from '../vendor/preact-htm.js';
import { off } from './policy.js';
import { useStore, state, update, setView, tlKey, loadThread, toast, openModal } from './store.js';
import { t } from './i18n.js';
import { Icon } from './icons.js';
import { Avatar, openMenu, closeMenu, confirmDialog, Spinner, Empty, useAsync, Switch } from './ui.js';
import { rpc } from './rpc.js';
import { localpart, formatShort, copyText, escapeHTML, linkify } from './util.js';
import { roomName, leaveRoom, roomMenu } from './sidebar.js';
import { Composer } from './composer.js';
import { startCall, joinGroupCall } from './call.js';

export function RoomHeader({ a, room, summary }) {
  const panel = useStore((s) => s.view.panel);
  const presence = useStore((s) => (summary && summary.dm && s.acct[a] ? s.acct[a].presence[summary.dm] : null));
  const name = roomName(summary);
  const toggle = (p) => setView({ panel: panel === p ? null : p, thread: null });
  let sub = summary.topic || '';
  if (summary.dm) sub = presence && presence.presence === 'online' ? t('En línea') : presence && presence.last_active_ago ? t('Activo hace {t}', { t: ago(presence.last_active_ago) }) : summary.dm;
  else if (!sub) sub = t('{n} miembro|{n} miembros', { n: summary.joined || 0 });
  const canCall = summary.membership === 'join' && !off('calls');
  return html`<header class="room-head">
    <button class="icon-btn mobile-back" style="display:none" onClick=${() => setView({ room: null })}><${Icon} name="chevronLeft" /></button>
    <div onClick=${() => toggle('info')} style="cursor:pointer"><${Avatar} a=${a} mxc=${summary.avatar} name=${name} id=${summary.dm || room} size=${38} presence=${presence && presence.presence} /></div>
    <div class="titles" onClick=${() => toggle('info')}>
      <div class="title">${name}${summary.encrypted ? html`<span title=${t('Cifrado de extremo a extremo')} class="muted"><${Icon} name="lock" size=14 /></span>` : null}
        ${summary.bridge ? html`<span class="muted small">· ${summary.bridge.name || summary.bridge.protocol}</span>` : null}</div>
      <div class="subtitle">${sub}</div>
    </div>
    ${summary.call && summary.call.length && !off('calls') ? html`<button class="btn primary" style="height:32px" onClick=${() => joinGroupCall(a, room)}><${Icon} name="video" size=16 /> ${t('Unirse')}</button>` : null}
    ${canCall ? html`<button class="icon-btn" title=${t('Llamada de voz')} onClick=${() => startCall(a, room, false)}><${Icon} name="phone" /></button>
      <button class="icon-btn" title=${t('Videollamada')} onClick=${() => startCall(a, room, true)}><${Icon} name="video" /></button>` : null}
    <button class=${`icon-btn hide-mobile${panel === 'threads' ? ' on' : ''}`} title=${t('Hilos')} onClick=${() => toggle('threads')}><${Icon} name="thread" /></button>
    <button class=${`icon-btn${panel === 'search' ? ' on' : ''}`} title=${t('Buscar en la sala')} onClick=${() => toggle('search')}><${Icon} name="search" /></button>
    <button class=${`icon-btn${panel === 'info' || panel === 'members' ? ' on' : ''}`} title=${t('Información')} onClick=${() => toggle('info')}><${Icon} name="info" /></button>
  </header>`;
}

function ago(ms) {
  const m = Math.round(ms / 60000);
  if (m < 60) return `${Math.max(m, 1)} min`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h} h`;
  return `${Math.round(h / 24)} d`;
}

export function RightPanel({ a, room, summary }) {
  const panel = useStore((s) => s.view.panel);
  const thread = useStore((s) => s.view.thread);
  if (!panel || !summary) return null;
  const close = () => setView({ panel: null, thread: null });
  let title, body;
  switch (panel) {
    case 'members': title = t('Miembros'); body = html`<${Members} a=${a} room=${room} summary=${summary} />`; break;
    case 'thread': title = t('Hilo'); body = html`<${ThreadView} a=${a} room=${room} root=${thread} summary=${summary} />`; break;
    case 'threads': title = t('Hilos'); body = html`<${ThreadList} a=${a} room=${room} />`; break;
    case 'search': title = t('Buscar'); body = html`<${SearchPanel} a=${a} room=${room} />`; break;
    case 'pinned': title = t('Mensajes fijados'); body = html`<${Pinned} a=${a} room=${room} />`; break;
    default: title = t('Información'); body = html`<${RoomInfo} a=${a} room=${room} summary=${summary} />`;
  }
  return html`<aside class="panel">
    <div class="panel-head">
      ${panel === 'members' || panel === 'pinned' || (panel === 'thread' && false) ? html`<button class="icon-btn" onClick=${() => setView({ panel: 'info' })}><${Icon} name="chevronLeft" /></button>` : null}
      <h3>${title}</h3><button class="icon-btn" onClick=${close}><${Icon} name="close" /></button></div>
    <div class="panel-body">${body}</div>
  </aside>`;
}

function RoomInfo({ a, room, summary }) {
  const name = roomName(summary);
  const power = summary.power || 0;
  const notify = summary.notify;
  const fav = (summary.tags || []).includes('m.favourite');
  return html`<div>
    <div class="info-hero">
      <${Avatar} a=${a} mxc=${summary.avatar} name=${name} id=${summary.dm || room} size=${84} />
      <h2>${name}</h2>
      <div class="sub selectable">${summary.dm || summary.alias || ''}</div>
      ${summary.topic ? html`<div class="topic selectable" dangerouslySetInnerHTML=${{ __html: linkify(summary.topic) }}></div>` : null}
      <div class="row wrap" style="justify-content:center;margin-top:6px">
        ${summary.encrypted ? html`<span class="small muted"><${Icon} name="lock" size=13 /> ${t('Cifrada')}</span>` : html`<span class="small muted"><${Icon} name="globe" size=13 /> ${t('Sin cifrar')}</span>`}
        ${summary.bridge ? html`<span class="small muted">· <${Icon} name="bridge" size=13 /> ${summary.bridge.name || summary.bridge.protocol}${summary.bridge.channel ? ` (${summary.bridge.channel})` : ''}</span>` : null}
      </div>
    </div>
    <div class="menu-list">
      <button class="menu-item" onClick=${() => setView({ panel: 'members' })}><${Icon} name="users" /><span class="mi-text">${t('Miembros')}</span><span class="mi-sub">${summary.joined}</span></button>
      ${summary.pinned ? html`<button class="menu-item" onClick=${() => setView({ panel: 'pinned' })}><${Icon} name="pin" /><span class="mi-text">${t('Mensajes fijados')}</span><span class="mi-sub">${summary.pinned}</span></button>` : null}
      <button class="menu-item" onClick=${() => openModal('invite', { room })}><${Icon} name="userPlus" /><span class="mi-text">${t('Invitar')}</span></button>
      <button class="menu-item" onClick=${(e) => openMenu(e.currentTarget, [
        { label: t('Predeterminado'), check: notify === 'default', onClick: () => rpc.call('room.notify', { a, room, mode: 'default' }) },
        { label: t('Todos los mensajes'), check: notify === 'all', onClick: () => rpc.call('room.notify', { a, room, mode: 'all' }) },
        { label: t('Solo menciones'), check: notify === 'mentions', onClick: () => rpc.call('room.notify', { a, room, mode: 'mentions' }) },
        { label: t('Silenciar'), check: notify === 'mute', onClick: () => rpc.call('room.notify', { a, room, mode: 'mute' }) },
      ])}><${Icon} name=${notify === 'mute' ? 'bellOff' : 'bell'} /><span class="mi-text">${t('Notificaciones')}</span>
        <span class="mi-sub">${{ default: t('Predeterminado'), all: t('Todos'), mentions: t('Menciones'), mute: t('Silenciada') }[notify] || ''}</span></button>
      <button class="menu-item" onClick=${() => rpc.call('room.tag', { a, room, tag: 'm.favourite', on: !fav })}><${Icon} name="star" /><span class="mi-text">${fav ? t('Quitar de favoritos') : t('Agregar a favoritos')}</span></button>
      <button class="menu-item" onClick=${() => copyText(`https://matrix.to/#/${encodeURIComponent(summary.alias || room)}`).then(() => toast(t('Enlace copiado')))}><${Icon} name="link" /><span class="mi-text">${t('Copiar enlace')}</span></button>
      ${summary.dm ? html`<button class="menu-item" onClick=${() => openModal('verifyUser', { user: summary.dm, room })}><${Icon} name="shieldOk" /><span class="mi-text">${t('Verificar identidad')}</span></button>` : null}
      ${power >= 50 || !summary.dm ? html`<button class="menu-item" onClick=${() => openModal('roomSettings', { room })}><${Icon} name="settings" /><span class="mi-text">${t('Ajustes de la sala')}</span></button>` : null}
      <button class="menu-item danger" onClick=${() => leaveRoom(room)}><${Icon} name="door" /><span class="mi-text">${summary.dm ? t('Salir del chat') : t('Salir de la sala')}</span></button>
    </div>
  </div>`;
}

function Members({ a, room, summary }) {
  const [q, setQ] = useState('');
  const { loading, data, error } = useAsync(() => rpc.call('room.members', { a, room }), [room, summary.joined]);
  const list = (data || []).filter((m) => !q || (m.name || '').toLowerCase().includes(q.toLowerCase()) || m.id.toLowerCase().includes(q.toLowerCase()));
  const roleOf = (p) => (p >= 100 ? t('Administrador') : p >= 50 ? t('Moderador') : '');
  const joined = list.filter((m) => m.membership === 'join');
  const invited = list.filter((m) => m.membership === 'invite');
  return html`<div style="padding:8px">
    <div class="search" style="margin:4px 6px 8px"><${Icon} name="search" size=16 /><input placeholder=${t('Filtrar miembros')} value=${q} onInput=${(e) => setQ(e.currentTarget.value)} /></div>
    ${loading && !data ? html`<div class="tl-loader"><${Spinner} /></div>` : null}
    ${error ? html`<div class="error-text" style="padding:8px">${error.message}</div>` : null}
    ${joined.map((m) => html`<div class="member" onClick=${(e) => openUser(m.id, e.currentTarget, room)}>
      <${Avatar} a=${a} mxc=${m.avatar} name=${m.name || localpart(m.id)} id=${m.id} size=${34} />
      <div class="mn">${m.name || localpart(m.id)}<div class="small muted" style="overflow:hidden;text-overflow:ellipsis">${m.id}</div></div>
      <span class="mrole">${roleOf(m.power)}</span></div>`)}
    ${invited.length ? html`<div class="section-label">${t('Invitados')}</div>${invited.map((m) => html`<div class="member" onClick=${(e) => openUser(m.id, e.currentTarget, room)}>
      <${Avatar} a=${a} mxc=${m.avatar} name=${m.name || localpart(m.id)} id=${m.id} size=${34} /><div class="mn">${m.name || localpart(m.id)}</div></div>`)}` : null}
  </div>`;
}

function ThreadView({ a, room, root, summary }) {
  const th = useStore((s) => s.threads[tlKey(a, root)]);
  useEffect(() => { if (root) loadThread(a, room, root); }, [root]);
  if (!root) return null;
  const items = (th && th.items) || [];
  const profiles = (th && th.profiles) || {};
  const scRef = useRef();
  useEffect(() => { if (scRef.current) scRef.current.scrollTop = scRef.current.scrollHeight; }, [items.length]);
  return html`<div style="display:flex;flex-direction:column;height:100%">
    <div style="flex:1;overflow-y:auto" ref=${scRef}>
      ${th && th.loading && !items.length ? html`<div class="tl-loader"><${Spinner} /></div>` : null}
      ${items.map((it, i) => html`<${MiniEvent} a=${a} item=${it} profiles=${profiles} root=${i === 0} />`)}
    </div>
    <${Composer} a=${a} room=${room} summary=${summary} thread=${root} />
  </div>`;
}

function MiniEvent({ a, item, profiles, root, onClick }) {
  const p = profiles[item.sender] || {};
  const c = item.content || {};
  let body = c.body || '';
  if (item.redacted) body = t('Mensaje eliminado');
  else if (item.utd) body = `🔒 ${t('Esperando las claves para descifrar este mensaje…')}`;
  else if (c.msgtype === 'm.image') body = `📷 ${c.body || t('Imagen')}`;
  const htmlBody = c.format === 'org.matrix.custom.html' && !item.redacted && !item.utd ? null : linkify(body);
  return html`<div class="ev first" style=${root ? 'border-bottom:1px solid var(--line);padding-bottom:10px;margin-bottom:6px' : ''} onClick=${onClick}>
    <div class="gutter"><${Avatar} a=${a} mxc=${p.avatar} name=${p.name || localpart(item.sender)} id=${item.sender} size=${30} /></div>
    <div style="min-width:0"><div class="who"><span class="nm">${p.name || localpart(item.sender)}</span><span class="ts">${formatShort(item.ts)}</span></div>
      ${htmlBody !== null ? html`<div class="msg-body" dangerouslySetInnerHTML=${{ __html: htmlBody }}></div>` : html`<${RichBody} a=${a} content=${c} />`}
      ${item.reactions && item.reactions.length ? html`<div class="reactions">${item.reactions.map((r) => html`<span class="reaction"><span class="rk">${r.key}</span>${r.count > 1 ? r.count : ''}</span>`)}</div>` : null}
    </div></div>`;
}

function RichBody({ a, content }) {
  const [h, setH] = useState('');
  useEffect(() => { import('./message.js').then((m) => setH(m.bodyHTML(content, { a, me: a }))); }, [content]);
  return html`<div class="msg-body" dangerouslySetInnerHTML=${{ __html: h }}></div>`;
}

function ThreadList({ a, room }) {
  const { loading, data, error } = useAsync(() => rpc.call('room.threads', { a, room }), [room]);
  if (loading) return html`<div class="tl-loader"><${Spinner} /></div>`;
  if (error) return html`<div class="error-text" style="padding:16px">${error.message}</div>`;
  const items = (data && data.items) || [];
  if (!items.length) return html`<${Empty} icon="thread" text=${t('Todavía no hay hilos en esta sala.')} />`;
  return html`<div>${items.map((it) => html`<div style="cursor:pointer" onClick=${() => setView({ panel: 'thread', thread: it.id })}>
    <${MiniEvent} a=${a} item=${it} profiles=${data.profiles || {}} />
    <div class="small muted" style="padding:0 18px 10px 64px">${it.thread ? t('{n} respuesta|{n} respuestas', { n: it.thread.count }) : ''}</div></div>`)}</div>`;
}

function SearchPanel({ a, room }) {
  const [q, setQ] = useState('');
  const [scope, setScope] = useState('room');
  const [res, setRes] = useState(null);
  const [busy, setBusy] = useState(false);
  const [info, setInfo] = useState(null); // { complete, pending }
  const run = async (e) => {
    e && e.preventDefault();
    if (!q.trim()) return;
    setBusy(true);
    setInfo(null);
    try {
      const [local, server] = await Promise.all([
        rpc.call('search.local', { a, query: q, room: scope === 'room' ? room : '' }),
        rpc.call('search.server', { a, query: q, room: scope === 'room' ? room : '' }).catch(() => ({ hits: [] })),
      ]);
      const seen = new Set();
      const hits = [...(local.hits || []), ...(server.hits || [])].filter((h) => (seen.has(h.id) ? false : (seen.add(h.id), true)))
        .sort((x, y) => y.ts - x.ts);
      setRes(hits);
      setInfo({ complete: local.complete !== false, pending: local.pending_rooms || 0 });
    } catch (e2) { toast(e2.message, 'err'); } finally { setBusy(false); }
  };
  const rooms = state.acct[a] ? state.acct[a].rooms : {};
  return html`<div style="padding:12px">
    <form onSubmit=${run} class="col" style="gap:8px">
      <div class="search"><${Icon} name="search" size=16 /><input autofocus placeholder=${t('Buscar mensajes')} value=${q} onInput=${(e) => setQ(e.currentTarget.value)} /></div>
      <div class="seg"><button type="button" class=${scope === 'room' ? 'on' : ''} onClick=${() => setScope('room')}>${t('Esta sala')}</button>
        <button type="button" class=${scope === 'all' ? 'on' : ''} onClick=${() => setScope('all')}>${t('Todas')}</button></div>
      <div class="small muted">${t('La búsqueda incluye tus mensajes cifrados (se indexan solo en este dispositivo).')}</div>
    </form>
    ${busy ? html`<div class="tl-loader col" style="align-items:center;gap:6px"><${Spinner} />${scope === 'room' ? html`<span class="small muted">${t('Buscando en todo el historial de la sala…')}</span>` : null}</div>` : null}
    ${!busy && info && (scope === 'room' ? !info.complete : info.pending > 0) ? html`<div class="small muted" style="margin:8px 2px">${scope === 'room'
      ? t('Esta sala tiene mucho historial y todavía se está bajando: buscá de nuevo en un rato para ver lo más viejo.')
      : t('Todavía se está bajando el historial de {n} salas (de a poco, en segundo plano): lo más viejo puede faltar.', { n: info.pending })}</div>` : null}
    ${res && !res.length ? html`<${Empty} icon="search" text=${t('Sin resultados')} />` : null}
    ${(res || []).map((h) => html`<div class="member" style="align-items:flex-start" onClick=${() => {
        if (h.room !== state.view.room) setView({ room: h.room, panel: 'search' });
        setTimeout(() => setView({ highlight: { room: h.room, id: h.id } }), 100);
      }}>
      <div class="mn" style="white-space:normal">
        <div class="small muted">${localpart(h.sender)}${scope === 'all' && rooms[h.room] ? ` · ${roomName(rooms[h.room])}` : ''} · ${formatShort(h.ts)}</div>
        <div dangerouslySetInnerHTML=${{ __html: escapeHTML(h.snippet).replace(/\[\[/g, '<mark>').replace(/\]\]/g, '</mark>') }}></div>
      </div></div>`)}
  </div>`;
}

function Pinned({ a, room }) {
  const { loading, data } = useAsync(() => rpc.call('room.pinned', { a, room }), [room]);
  if (loading) return html`<div class="tl-loader"><${Spinner} /></div>`;
  const items = (data && data.items) || [];
  if (!items.length) return html`<${Empty} icon="pin" text=${t('No hay mensajes fijados.')} />`;
  return html`<div>${items.map((it) => html`<${MiniEvent} a=${a} item=${it} profiles=${data.profiles || {}} onClick=${() => setView({ highlight: { room, id: it.id } })} />`)}</div>`;
}

// --- tarjeta de usuario ---
export function openUser(uid, anchor, room) {
  openMenu(anchor, null, { render: () => html`<${UserCard} uid=${uid} room=${room} />` });
}

function UserCard({ uid, room }) {
  const a = state.cur;
  const { data } = useAsync(() => rpc.call('user.profile', { a, user: uid }), [uid]);
  const [devs, setDevs] = useState(null);
  useEffect(() => { rpc.call('user.devices', { a, user: uid }).then(setDevs).catch(() => {}); }, [uid]);
  const summary = room && state.acct[a] ? state.acct[a].rooms[room] : null;
  const myPower = summary ? summary.power : 0;
  const tl = room ? state.tl[tlKey(a, room)] : null;
  const prof = (tl && tl.profiles[uid]) || {};
  const name = (data && data.displayname) || prof.name || localpart(uid);
  const avatar = (data && data.avatar_url) || prof.avatar;
  const isMe = uid === a;
  const dm = async () => {
    closeMenu();
    try { const r = await rpc.call('dm.open', { a, user: uid }); setView({ room: r.room }); } catch (e) { toast(e.message, 'err'); }
  };
  const act = async (method, label, extra = {}) => {
    const reason = await confirmDialog({ title: label, text: t('Motivo (opcional)'), input: 'text', ok: label, danger: true });
    if (reason === null) return;
    closeMenu();
    rpc.call(method, { a, room, user: uid, reason, ...extra }).catch((e) => toast(e.message, 'err'));
  };
  return html`<div style="width:300px;padding:14px" class="col">
    <div class="row"><${Avatar} a=${a} mxc=${avatar} name=${name} id=${uid} size=${56} />
      <div style="min-width:0"><div style="font-weight:650;font-size:16px">${name}</div><div class="small muted selectable" style="word-break:break-all">${uid}</div>
        ${devs ? html`<div class="small" style="margin-top:2px">${devs.verified ? html`<span class="shield-ok"><${Icon} name="shieldOk" size=13 /> ${t('Verificado')}</span>`
          : devs.cross_signing ? html`<span class="muted"><${Icon} name="shield" size=13 /> ${t('No verificado')}</span>` : html`<span class="shield-warn"><${Icon} name="shieldWarn" size=13 /> ${t('Sin identidad criptográfica')}</span>`}</div>` : null}
      </div></div>
    ${!isMe ? html`<div class="row"><button class="btn primary grow" onClick=${dm}><${Icon} name="send" size=16 /> ${t('Mensaje')}</button>
      ${devs && !devs.verified && devs.cross_signing ? html`<button class="btn" onClick=${() => { closeMenu(); openModal('verifyUser', { user: uid, room }); }}>${t('Verificar')}</button>` : null}</div>` : null}
    <div class="menu-list" style="padding:0">
      <button class="menu-item" onClick=${() => { copyText(uid); closeMenu(); toast(t('Copiado')); }}><${Icon} name="copy" size=17 /><span class="mi-text">${t('Copiar ID')}</span></button>
      ${!isMe ? html`<button class="menu-item" onClick=${() => { closeMenu(); rpc.call('user.ignore', { a, user: uid, ignore: true }).then(() => toast(t('Usuario ignorado'))); }}><${Icon} name="eyeOff" size=17 /><span class="mi-text">${t('Ignorar')}</span></button>` : null}
      ${room && !isMe && myPower >= 50 ? html`
        <button class="menu-item" onClick=${() => { closeMenu(); openModal('power', { room, user: uid }); }}><${Icon} name="crown" size=17 /><span class="mi-text">${t('Cambiar rol')}</span></button>
        <button class="menu-item danger" onClick=${() => act('room.kick', t('Quitar de la sala'))}><${Icon} name="door" size=17 /><span class="mi-text">${t('Quitar de la sala')}</span></button>
        <button class="menu-item danger" onClick=${() => act('room.ban', t('Expulsar'), { ban: true })}><${Icon} name="ban" size=17 /><span class="mi-text">${t('Expulsar')}</span></button>
        <button class="menu-item danger" onClick=${async () => {
          closeMenu();
          const reason = await confirmDialog({ title: t('Borrar mensajes recientes'), text: t('Se borrarán para todos los últimos mensajes de {u} en esta sala (hasta 200). Útil contra spam o abuso.', { u: uid }),
            input: 'text', placeholder: t('Motivo (opcional)'), ok: t('Borrar mensajes'), danger: true });
          if (reason === null) return;
          toast(t('Borrando mensajes…'));
          rpc.call('room.redactRecent', { a, room, user: uid, limit: 200, reason }).then((r) => toast(t('{n} mensajes borrados', { n: r.redacted }))).catch((e) => toast(e.message, 'err'));
        }}><${Icon} name="trash" size=17 /><span class="mi-text">${t('Borrar mensajes recientes')}</span></button>` : null}
    </div>
  </div>`;
}

export async function openRoomLink(target, eventId, via = []) {
  const a = state.cur;
  const rooms = state.acct[a] ? state.acct[a].rooms : {};
  const found = Object.values(rooms).find((r) => r.id === target || r.alias === target);
  if (found && found.membership === 'join') {
    setView({ room: found.id });
    if (eventId) setTimeout(() => setView({ highlight: { room: found.id, id: eventId } }), 150);
    return;
  }
  openModal('join', { target, via });
}
