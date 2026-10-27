// Línea de tiempo: scroll fluido con anclaje manual, carga anticipada y elementos memoizados.
import { html, useRef, useEffect, useLayoutEffect, useState, useMemo, useCallback } from '../vendor/preact-htm.js';
import { useStore, state, update, tlKey, loadTimeline, setView, toast, openModal, HIDDEN_TYPES } from './store.js';
import { t } from './i18n.js';
import { Icon } from './icons.js';
import { Avatar, memo, openMenu, confirmDialog } from './ui.js';
import { rpc } from './rpc.js';
import { formatTime, formatDay, sameDay, localpart, copyText, colorFor, throttle } from './util.js';
import { TextBody, ImageContent, VideoContent, AudioContent, FileContent, LocationContent, PollContent, stateText, callText, onBodyClick, senderNameFn, saveAs } from './message.js';
import { setComposer } from './composer.js';
import { showEmojiPicker } from './emoji.js';
import { openUser, openRoomLink } from './panels.js';

const QUICK = ['👍', '❤️', '😂', '🎉', '😮', '🙏'];
const EMPTY_UI = {};
const GROUP_MS = 5 * 60 * 1000;

export function Timeline({ a, room, summary }) {
  const key = tlKey(a, room);
  const tl = useStore((s) => s.tl[key]);
  const receipts = useStore((s) => (s.acct[a] ? s.acct[a].receipts[room] : null));
  const highlight = useStore((s) => s.view.highlight);
  const typing = useStore((s) => (s.acct[a] ? s.acct[a].typing[room] : null));
  const scRef = useRef();
  const innerRef = useRef();
  const stick = useRef(true);
  const anchor = useRef(null);
  const [showJump, setShowJump] = useState(false);
  const readAtOpen = useRef(null);

  useEffect(() => {
    stick.current = true;
    anchor.current = null;
    readAtOpen.current = null;
    setShowJump(false);
    loadTimeline(a, room);
  }, [key]);

  // Posición de "nuevos mensajes": la tomamos al abrir la sala.
  useEffect(() => {
    if (readAtOpen.current === null && tl && tl.items.length && summary) {
      readAtOpen.current = summary.unread > 0 ? Math.max(0, tl.items.length - summary.unread) : -1;
    }
  }, [tl, summary]);

  // Retención de la sala: lo vencido no se muestra (y el núcleo lo borra de este equipo).
  const retention = summary && summary.retention_ms;
  const items = useMemo(() => {
    const all = tl ? tl.items : [];
    if (!retention) return all;
    const cut = Date.now() - retention;
    return all.filter((it) => it.ts >= cut || it.id.startsWith('~'));
  }, [tl && tl.items, retention]);
  const profiles = tl ? tl.profiles : {};
  const me = a;

  // --- anclaje de scroll ---
  const findAnchor = () => {
    const sc = scRef.current, inner = innerRef.current;
    if (!sc || !inner) return;
    const top = sc.scrollTop;
    const kids = inner.children;
    // búsqueda binaria del primer hijo visible
    let lo = 0, hi = kids.length - 1, found = -1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      const el = kids[mid];
      if (el.offsetTop + el.offsetHeight > top) { found = mid; hi = mid - 1; } else lo = mid + 1;
    }
    if (found >= 0 && kids[found].dataset.k) anchor.current = { k: kids[found].dataset.k, off: kids[found].offsetTop - top };
  };

  const restore = () => {
    const sc = scRef.current;
    if (!sc) return;
    if (stick.current) {
      sc.scrollTop = sc.scrollHeight;
      return;
    }
    const an = anchor.current;
    if (!an) return;
    const el = innerRef.current && innerRef.current.querySelector(`[data-k="${CSS.escape(an.k)}"]`);
    if (!el) return;
    const want = el.offsetTop - an.off;
    if (Math.abs(sc.scrollTop - want) > 1) sc.scrollTop = want;
  };

  useLayoutEffect(restore, [items]);

  useEffect(() => {
    const inner = innerRef.current;
    if (!inner) return;
    const ro = new ResizeObserver(() => restore());
    ro.observe(inner);
    return () => ro.disconnect();
  }, [key]);

  const markRead = useMemo(() => throttle(() => {
    const st = state.tl[key];
    if (!st || !st.items.length || !document.hasFocus()) return;
    const last = [...st.items].reverse().find((i) => !i.id.startsWith('~'));
    if (last) rpc.call('room.read', { a, room, id: last.id }).catch(() => {});
  }, 1500), [key]);

  const onScroll = () => {
    const sc = scRef.current;
    if (!sc) return;
    const dist = sc.scrollHeight - sc.scrollTop - sc.clientHeight;
    stick.current = dist < 60;
    const st = state.tl[key];
    if (st) st.atBottom = stick.current;
    setShowJump(dist > 900);
    findAnchor();
    if (sc.scrollTop < 1600 && st && st.hasMore && !st.loading) loadTimeline(a, room, true);
    if (stick.current) markRead();
  };

  // Al llegar mensajes nuevos estando abajo, marcamos como leído.
  useEffect(() => { if (stick.current && items.length) markRead(); }, [items.length]);
  useEffect(() => {
    const f = () => stick.current && markRead();
    window.addEventListener('focus', f);
    return () => window.removeEventListener('focus', f);
  }, [key]);

  // Si la página inicial no llena la pantalla, pedimos más.
  useEffect(() => {
    const sc = scRef.current;
    if (sc && tl && !tl.loading && tl.hasMore && sc.scrollHeight <= sc.clientHeight + 200) loadTimeline(a, room, true);
  }, [tl && tl.items.length, tl && tl.loading]);

  // Saltar a un evento resaltado (búsqueda, respuesta).
  useEffect(() => {
    if (!highlight || highlight.room !== room) return;
    let tries = 0;
    const go = async () => {
      const el = innerRef.current && innerRef.current.querySelector(`[data-k="${CSS.escape(highlight.id)}"]`);
      if (el) {
        stick.current = false;
        el.scrollIntoView({ block: 'center' });
        el.classList.add('flash');
        setTimeout(() => el.classList.remove('flash'), 1700);
        findAnchor();
        update((s) => { s.view = { ...s.view, highlight: null }; });
        return;
      }
      const st = state.tl[key];
      if (tries++ < 25 && st && st.hasMore) {
        await loadTimeline(a, room, true);
        setTimeout(go, 50);
      } else {
        update((s) => { s.view = { ...s.view, highlight: null }; });
        toast(t('No se encontró el mensaje en el historial cargado'));
      }
    };
    setTimeout(go, 30);
  }, [highlight]);

  const nm = useMemo(() => senderNameFn(profiles), [profiles]);
  const ui = useStore((s) => (s.settings && s.settings.ui) || EMPTY_UI);
  const showJoinLeave = ui.showJoinLeave !== false;
  const showProfile = ui.showProfileChanges !== false;
  const showReceipts = ui.showReceipts !== false;
  const hiddenState = (it) => {
    if (it.type !== 'm.room.member') return false;
    const m = it.content && it.content.membership, pm = it.prev && it.prev.membership;
    if (m === 'join' && pm === 'join') return !showProfile;
    if ((m === 'join' && pm !== 'invite') || (m === 'leave' && it.sender === it.sk && pm === 'join')) return !showJoinLeave;
    return false;
  };

  // Recibos: evento -> usuarios (excluyéndome).
  const receiptMap = useMemo(() => {
    const m = {};
    if (!receipts) return m;
    for (const [u, ev] of Object.entries(receipts)) {
      if (u === me) continue;
      (m[ev] = m[ev] || []).push(u);
    }
    return m;
  }, [receipts]);

  // Construcción de filas.
  const rows = [];
  let prev = null;
  let stateRun = [];
  const flushState = () => {
    if (!stateRun.length) return;
    if (stateRun.length >= 4) rows.push(html`<${StateGroup} key=${`g${stateRun[0].id}`} items=${stateRun} nm=${nm} />`);
    else for (const it of stateRun) rows.push(html`<${StateLine} key=${it.id} item=${it} nm=${nm} />`);
    stateRun = [];
  };
  const unreadIdx = readAtOpen.current;
  let utdRun = [];
  const flushUTD = () => {
    if (!utdRun.length) return;
    if (utdRun.length >= 3) rows.push(html`<${UTDGroup} key=${`u${utdRun[0].id}`} items=${utdRun} a=${a} />`);
    else for (const it of utdRun) rows.push(html`<${EventTile} key=${it.txn || it.id} item=${it} a=${a} first=${true} me=${me}
      name=${nm(it.sender)} avatar=${profiles[it.sender] && profiles[it.sender].avatar} />`);
    utdRun = [];
  };
  items.forEach((it, idx) => {
    if (HIDDEN_TYPES.has(it.type)) return;
    if (it.utd && !it.redacted) { flushState(); utdRun.push(it); prev = it; return; }
    flushUTD();
    if (!prev || !sameDay(prev.ts, it.ts)) {
      flushState();
      rows.push(html`<div class="day-sep" key=${`d${it.id}`} data-k=${`d${it.id}`}>${formatDay(it.ts)}</div>`);
    }
    if (unreadIdx !== null && unreadIdx >= 0 && idx === unreadIdx && idx > 0) {
      flushUTD();
      flushState();
      rows.push(html`<div class="unread-sep" key="unread" data-k="unread">${t('Mensajes nuevos')}</div>`);
      prev = null; // tras el separador, el siguiente mensaje muestra remitente
    }
    const isState = it.sk !== undefined && it.sk !== null && !it.redacted;
    const isCall = it.type.startsWith('m.call.') || it.type.includes('rtc.notification') || it.type.includes('call.notify');
    if (isState || isCall) {
      if (isState && hiddenState(it)) { prev = it; return; }
      const text = isState ? stateText(it, nm) : callText(it, nm);
      if (text) stateRun.push(it);
      prev = it;
      return;
    }
    flushState();
    const first = !prev || prev.utd || prev.sender !== it.sender || it.ts - prev.ts > GROUP_MS || (prev.sk !== undefined && prev.sk !== null) ||
      !sameDay(prev.ts, it.ts) || prev.type.startsWith('m.call.');
    rows.push(html`<${EventTile} key=${it.txn || it.id} item=${it} a=${a} first=${first} me=${me}
      name=${nm(it.sender)} avatar=${profiles[it.sender] && profiles[it.sender].avatar}
      tf=${ui.time24 !== false} hl=${false} readers=${showReceipts ? receiptMap[it.id] : null} profiles=${showReceipts && receiptMap[it.id] ? profiles : null} />`);
    prev = it;
  });
  flushUTD();
  flushState();

  const typers = ui.showTyping === false ? [] : (typing || []).filter((u) => u !== me);

  return html`<div class="timeline" ref=${scRef} onScroll=${onScroll}
    onDragOver=${(e) => { e.preventDefault(); }} onDrop=${(e) => { e.preventDefault(); if (e.dataTransfer.files.length) openModal('upload', { files: [...e.dataTransfer.files], room }); }}>
    <div class="tl-inner" ref=${innerRef}>
      <div data-k="top">
        ${tl && tl.loading ? html`<div class="tl-loader"><div class="spinner"></div></div>`
          : tl && !tl.hasMore ? html`<div class="tl-start">${summary && summary.encrypted ? html`<div><${Icon} name="lock" size=16 /> ${t('Los mensajes de esta conversación están cifrados de extremo a extremo.')}</div>` : null}
            <div>${t('Este es el comienzo de la conversación.')}</div></div>`
          : tl && tl.err ? html`<div class="tl-loader error-text">${tl.err} <button class="btn ghost" onClick=${() => loadTimeline(a, room, true)}>${t('Reintentar')}</button></div>`
          : html`<div class="tl-loader"></div>`}
      </div>
      ${rows}
      <div data-k="bottom" style="height:4px"></div>
    </div>
    ${showJump ? html`<button class="jump-bottom" onClick=${() => { stick.current = true; scRef.current.scrollTop = scRef.current.scrollHeight; setShowJump(false); markRead(); }}>
      <${Icon} name="arrowDown" size=16 /> ${t('Ir al final')}</button>` : null}
  </div>
  <div class="typing">${typers.length ? html`<span class="dots"><span></span><span></span><span></span></span>
    ${typers.length === 1 ? t('{u} está escribiendo…', { u: nm(typers[0]) }) : typers.length === 2 ? t('{a} y {b} están escribiendo…', { a: nm(typers[0]), b: nm(typers[1]) }) : t('Varias personas están escribiendo…')}` : null}</div>`;
}

function UTDBody({ a, utd }) {
  const crypto = useStore((s) => (s.acct[a] ? s.acct[a].crypto : null));
  const needsVerify = crypto && (crypto.action === 'verify' || crypto.action === 'setup');
  let text = t('No se pudo descifrar este mensaje');
  if (utd === 'missing_keys') text = needsVerify ? t('Mensaje cifrado: verificá este dispositivo para leerlo') : t('Esperando las claves para descifrar este mensaje…');
  else if (utd === 'withheld' || utd === 'unverified_device') text = needsVerify ? t('El remitente solo comparte claves con dispositivos verificados. Verificá este dispositivo para leer los próximos mensajes.') : t('El remitente no compartió las claves con este dispositivo');
  return html`<div class="utd"><${Icon} name="lock" size=15 /> <span>${text}</span>
    ${needsVerify ? html`<button class="btn ghost" style="height:24px;padding:0 8px" onClick=${() => openModal('security', { step: crypto.action === 'setup' ? 'setup' : 'verify' })}>${t('Verificar')}</button>` : null}</div>`;
}

function UTDGroup({ items, a }) {
  const crypto = useStore((s) => (s.acct[a] ? s.acct[a].crypto : null));
  const unverified = crypto && crypto.action;
  return html`<div class="state-ev" data-k=${`u${items[0].id}`} style="padding-top:8px;padding-bottom:8px">
    <${Icon} name="lock" size=14 />
    <span>${t('{n} mensajes cifrados no se pueden leer en este dispositivo.', { n: items.length })}
      ${unverified ? t(' Verificá este dispositivo o ingresá tu clave de recuperación para recuperarlos.') : t(' Las claves no se compartieron con este dispositivo.')}</span>
    ${unverified ? html`<button onClick=${() => openModal('security', { step: crypto.action === 'setup' ? 'setup' : 'verify' })}>${t('Resolver')}</button>` : null}
  </div>`;
}

function StateLine({ item, nm }) {
  const text = item.sk !== undefined && item.sk !== null ? stateText(item, nm) : callText(item, nm);
  if (!text) return null;
  return html`<div class="state-ev" data-k=${item.id}><${Icon} name=${item.type.startsWith('m.call') ? 'phone' : item.type === 'm.room.member' ? 'users' : 'info'} size=14 />
    <span>${text}</span><span class="ts">${formatTime(item.ts)}</span>
    ${item.type === 'm.room.tombstone' && item.content.replacement_room ? html`<button onClick=${() => openRoomLink(item.content.replacement_room)}>${t('Ir a la sala nueva')}</button>` : null}
  </div>`;
}

function StateGroup({ items, nm }) {
  const [open, setOpen] = useState(false);
  if (open) return html`<div data-k=${`g${items[0].id}`}>${items.map((it) => html`<${StateLine} key=${it.id} item=${it} nm=${nm} />`)}</div>`;
  const joins = items.filter((i) => i.type === 'm.room.member' && i.content.membership === 'join' && (!i.prev || i.prev.membership !== 'join')).length;
  return html`<div class="state-ev" data-k=${`g${items[0].id}`}><${Icon} name="users" size=14 />
    <span>${joins === items.length ? t('{n} personas se unieron', { n: joins }) : t('{n} cambios en la sala', { n: items.length })}</span>
    <button onClick=${() => setOpen(true)}>${t('Mostrar')}</button></div>`;
}

function TrustShield({ item }) {
  if (!item.enc || item.status) return null;
  const tr = item.trust;
  if (!tr || tr === 'verified' || tr === 'tofu') return null;
  const map = {
    unsigned: ['shieldWarn', 'shield-warn', t('Enviado desde un dispositivo que su dueño no verificó')],
    unknown_device: ['shieldWarn', 'shield-warn', t('Dispositivo desconocido o eliminado')],
    changed: ['shieldWarn', 'shield-bad', t('La identidad de esta persona cambió')],
    mismatch: ['shieldWarn', 'shield-bad', t('No se puede garantizar la autenticidad de este mensaje')],
    blacklisted: ['shieldWarn', 'shield-bad', t('Dispositivo bloqueado')],
    backup: ['shield', 'muted', t('Clave recuperada del respaldo: la autenticidad no se puede garantizar')],
    forwarded: ['shield', 'muted', t('Clave reenviada por otro dispositivo: la autenticidad no se puede garantizar')],
  };
  const m = map[tr];
  if (!m) return null;
  return html`<span class=${`shield ${m[1]}`} title=${m[2]}><${Icon} name=${m[0]} size=14 /></span>`;
}

const EventTile = memo(function EventTile({ item, a, first, me, name, avatar, readers, profiles }) {
  const c = item.content || {};
  const [actionsOpen, setActionsOpen] = useState(false);
  const mine = item.sender === me;
  const mentioned = !mine && c['m.mentions'] && Array.isArray(c['m.mentions'].user_ids) && c['m.mentions'].user_ids.includes(me);
  let body;
  if (item.redacted) body = html`<div class="utd"><${Icon} name="trash" size=15 /> ${t('Mensaje eliminado')}</div>`;
  else if (item.utd) body = html`<${UTDBody} a=${a} utd=${item.utd} />`;
  else if (item.type === 'm.sticker') body = html`<${ImageContent} a=${a} item=${item} sticker />`;
  else if (item.type.endsWith('poll.start')) body = html`<${PollContent} a=${a} item=${item} me=${me} />`;
  else if (item.type === 'm.room.message') {
    switch (c.msgtype) {
      case 'm.image': body = html`<${ImageContent} a=${a} item=${item} />${c.body && c.filename && c.body !== c.filename ? html`<${TextBody} content=${c} ctx=${{ a, me }} />` : null}`; break;
      case 'm.video': body = html`<${VideoContent} a=${a} item=${item} />${c.body && c.filename && c.body !== c.filename ? html`<${TextBody} content=${c} ctx=${{ a, me }} />` : null}`; break;
      case 'm.audio': body = html`<${AudioContent} a=${a} item=${item} />`; break;
      case 'm.file': case 'io.hilvan.multipart': body = html`<${FileContent} a=${a} item=${item} />`; break;
      case 'm.location': body = html`<${LocationContent} item=${item} />`; break;
      case 'm.emote': body = html`<div class="emote-msg">* ${name} <${TextBody} content=${c} ctx=${{ a, me }} edited=${item.edited} /></div>`; break;
      case 'm.key.verification.request': body = html`<div class="utd"><${Icon} name="shieldOk" size=15 /> ${mine ? t('Solicitaste una verificación') : t('{u} quiere verificarte', { u: name })}</div>`; break;
      default: body = html`<${TextBody} content=${c} ctx=${{ a, me }} edited=${item.edited} />`;
    }
  } else if (item.type === 'm.room.encrypted') body = html`<div class="utd"><${Icon} name="lock" size=15 /> ${t('Descifrando…')}</div>`;
  else body = html`<div class="utd">${item.type}</div>`;

  const react = (key) => rpc.call('room.react', { a, room: item.room, id: item.id, key }).catch((e) => toast(e.message, 'err'));
  const handlers = { user: (u, el) => openUser(u, el, item.room), room: (r, ev, via) => openRoomLink(r, ev, via) };
  const canAct = !item.id.startsWith('~') && !item.redacted;

  return html`<div class=${`ev${first ? ' first' : ''}${mine ? ' mine' : ''}${item.status ? ' pending' : ''}${mentioned ? ' mention-me' : ''}`} data-k=${item.txn || item.id}
    onDblClick=${(e) => { if (canAct && !e.target.closest('.msg-body')) setComposer(item.room, { reply: item }); }}
    onContextMenu=${(e) => {
      // Clic derecho: el menú del mensaje donde está el puntero (Mayús + clic derecho: el del sistema).
      if (!canAct || e.shiftKey) return;
      e.preventDefault();
      const link = e.target.closest('a[href]');
      setActionsOpen(true);
      moreMenu(e, item, a, mine, () => setActionsOpen(false), { link: link ? link.href : '', selection: String(window.getSelection() || '') });
    }}>
    <div class="gutter">${first ? html`<div onClick=${(e) => openUser(item.sender, e.currentTarget, item.room)} style="cursor:pointer">
      <${Avatar} a=${a} mxc=${avatar} name=${name} id=${item.sender} size=${36} /></div>` : html`<span class="ts">${formatTime(item.ts)}</span>`}</div>
    <div style="min-width:0">
      ${first ? html`<div class="who"><span class="nm" style=${`color:${colorFor(item.sender)}`} onClick=${(e) => openUser(item.sender, e.currentTarget, item.room)}>${name}</span>
        <span class="ts">${formatTime(item.ts)}</span><${TrustShield} item=${item} /></div>` : null}
      <div class="msg-content">
        ${item.reply ? html`<${ReplyQuote} reply=${item.reply} room=${item.room} />` : null}
        <div onClick=${(e) => onBodyClick(e, handlers)}>${body}</div>
      </div>
      ${!first ? html`<${TrustShield} item=${item} />` : null}
      ${item.reactions && item.reactions.length ? html`<div class="reactions">${item.reactions.map((r) => html`
        <button class=${`reaction${r.mine ? ' mine' : ''}`} title=${(r.senders || []).map(localpart).join(', ')} onClick=${() => react(r.key)}>
          ${r.url ? html`<img src=${`/media/${encodeURIComponent(a)}/${r.url.slice(6)}?w=48&h=48`} alt=${r.key} />` : html`<span class="rk">${r.key}</span>`}${r.count > 1 ? r.count : ''}
        </button>`)}</div>` : null}
      ${item.thread && item.thread.count ? html`<div class="thread-sum" onClick=${() => setView({ panel: 'thread', thread: item.id })}>
        <${Icon} name="thread" size=15 /> ${t('{n} respuesta|{n} respuestas', { n: item.thread.count })}
        ${item.thread.latest_body ? html`<span class="tlast">${item.thread.latest_body}</span>` : null}</div>` : null}
      ${item.status === 'sending' ? html`<div class="ev-status"><div class="spinner sm"></div>${t('Enviando…')}</div>` : null}
      ${item.status === 'failed' ? html`<div class="ev-status failed">${t('No se envió.')}
        <button onClick=${() => rpc.call('room.retry', { a, id: item.id }).catch((e) => toast(e.message, 'err'))}>${t('Reintentar')}</button>
        <button onClick=${() => rpc.call('room.discard', { a, id: item.id })}>${t('Descartar')}</button></div>` : null}
      ${readers && readers.length ? html`<div class="receipts" title=${readers.map((u) => (profiles && profiles[u] && profiles[u].name) || localpart(u)).join(', ')}>
        ${readers.slice(0, 5).map((u) => html`<${Avatar} key=${u} a=${a} mxc=${profiles && profiles[u] && profiles[u].avatar} name=${localpart(u)} id=${u} size=${16} />`)}
        ${readers.length > 5 ? html`<span class="small muted">+${readers.length - 5}</span>` : null}</div>` : null}
    </div>
    ${canAct ? html`<div class=${`ev-actions${actionsOpen ? ' open' : ''}`}>
      ${QUICK.slice(0, 3).map((k) => html`<button title=${k} onClick=${() => react(k)}>${k}</button>`)}
      <button title=${t('Reaccionar')} onClick=${(e) => showEmojiPicker(e.currentTarget, { a, room: item.room, emoteAs: 'url', onPick: (k) => react(k) })}><${Icon} name="smile" size=18 /></button>
      <button title=${t('Responder')} onClick=${() => setComposer(item.room, { reply: item })}><${Icon} name="reply" size=18 /></button>
      ${!item.in_thread ? html`<button title=${t('Responder en hilo')} onClick=${() => setView({ panel: 'thread', thread: item.id })}><${Icon} name="thread" size=18 /></button>` : null}
      <button title=${t('Más')} onClick=${(e) => { setActionsOpen(true); moreMenu(e.currentTarget, item, a, mine, () => setActionsOpen(false)); }}><${Icon} name="more" size=18 /></button>
    </div>` : null}
  </div>`;
});

function ReplyQuote({ reply, room }) {
  const tl = state.tl[tlKey(state.cur, room)];
  const nm = (tl && tl.profiles[reply.sender] && tl.profiles[reply.sender].name) || reply.name || localpart(reply.sender);
  let body = reply.body;
  if (reply.missing) body = t('Mensaje no disponible');
  else if (reply.redacted) body = t('Mensaje eliminado');
  else if (reply.utd) body = `🔒 ${t('Mensaje cifrado')}`;
  else if (reply.msgtype === 'm.image') body = `📷 ${t('Imagen')}`;
  else if (reply.msgtype === 'm.video') body = `🎬 ${t('Video')}`;
  else if (reply.msgtype === 'm.audio') body = `🎤 ${t('Audio')}`;
  else if (reply.msgtype === 'm.file' || reply.msgtype === 'io.hilvan.multipart') body = `📎 ${reply.filename || reply.body}`;
  return html`<div class="reply-quote" onClick=${() => !reply.missing && setView({ highlight: { room, id: reply.id } })}>
    ${reply.missing ? null : html`<span class="rq-name">${nm}</span>`}<span class="rq-body">${body}</span></div>`;
}

async function moreMenu(anchor, item, a, mine, onClose, extra = {}) {
  const c = item.content || {};
  const isFile = !!(c.url || c.file || c['io.hilvan.parts'] || c.msgtype === 'io.hilvan.multipart');
  const summary = state.acct[a] && state.acct[a].rooms[item.room];
  const power = summary ? summary.power : 0;
  const isText = item.type === 'm.room.message' && ['m.text', 'm.notice', 'm.emote'].includes(c.msgtype);
  const canSave = !item.utd && (isFile || !!c.body);
  openMenu(anchor, [
    extra.selection ? { icon: 'copy', label: t('Copiar selección'), onClick: () => copyText(extra.selection).then(() => toast(t('Copiado'))) } : null,
    extra.link ? { icon: 'link', label: t('Copiar dirección del enlace'), onClick: () => copyText(extra.link).then(() => toast(t('Enlace copiado'))) } : null,
    canSave ? { icon: 'download', label: t('Guardar como…'), onClick: () => saveAs(a, item) } : null,
    isFile && !item.utd ? { icon: 'folder', label: t('Guardar en Descargas'), onClick: () => rpc.call('file.save', { a, room: item.room, event: item.id }).catch((e) => toast(e.message, 'err')) } : null,
    extra.selection || extra.link || canSave ? '-' : null,
    mine && isText && !item.utd ? { icon: 'edit', label: t('Editar'), onClick: () => setComposer(item.room, { edit: item }) } : null,
    { icon: 'reply', label: t('Responder'), onClick: () => setComposer(item.room, { reply: item }) },
    { icon: 'forward', label: t('Reenviar'), onClick: () => openModal('forward', { item }) },
    isText ? { icon: 'copy', label: t('Copiar texto'), onClick: () => copyText(c.body || '').then(() => toast(t('Copiado'))) } : null,
    { icon: 'link', label: t('Copiar enlace'), onClick: () => copyText(`https://matrix.to/#/${encodeURIComponent(item.room)}/${encodeURIComponent(item.id)}`).then(() => toast(t('Enlace copiado'))) },
    power >= 50 ? { icon: 'pin', label: t('Fijar / desfijar'), onClick: () => togglePin(a, item) } : null,
    { icon: 'info', label: t('Ver código fuente'), onClick: () => openModal('source', { item }) },
    '-',
    mine || power >= 50 ? { icon: 'trash', label: t('Eliminar'), danger: true, onClick: async () => {
      if (await confirmDialog({ title: t('Eliminar mensaje'), text: t('¿Eliminar este mensaje para todos?'), ok: t('Eliminar'), danger: true })) {
        rpc.call('room.redact', { a, room: item.room, id: item.id }).catch((e) => toast(e.message, 'err'));
      }
    } } : null,
    !mine ? { icon: 'flag', label: t('Denunciar'), danger: true, onClick: () => openModal('report', { item }) } : null,
  ]);
  setTimeout(onClose, 0);
}

async function togglePin(a, item) {
  try {
    const st = await rpc.call('room.state', { a, room: item.room, types: ['m.room.pinned_events'] });
    const cur = (st['m.room.pinned_events'] && st['m.room.pinned_events'][0] && st['m.room.pinned_events'][0].content.pinned) || [];
    const next = cur.includes(item.id) ? cur.filter((x) => x !== item.id) : [...cur, item.id];
    await rpc.call('room.setState', { a, room: item.room, type: 'm.room.pinned_events', state_key: '', content: { pinned: next } });
    toast(cur.includes(item.id) ? t('Mensaje desfijado') : t('Mensaje fijado'));
  } catch (e) { toast(e.message, 'err'); }
}

// Visor de imágenes/videos a pantalla completa.
