// Compositor de mensajes.
import { html, useState, useRef, useEffect, useMemo, useLayoutEffect } from '../vendor/preact-htm.js';
import { off } from './policy.js';
import { useStore, state, update, tlKey, toast, openModal, acct, mutAcct } from './store.js';
import { t } from './i18n.js';
import { Icon } from './icons.js';
import { Avatar, openMenu } from './ui.js';
import { rpc, upload } from './rpc.js';
import { localpart, debounce, throttle, formatDuration } from './util.js';
import { searchEmoji, showEmojiPicker, emotesFor } from './emoji.js';

// Estado del compositor por sala: { reply, edit } — fuera del árbol para que las acciones lo modifiquen.
export function setComposer(room, patch) {
  update((s) => {
    s.compose = { ...(s.compose || {}), [room]: { ...((s.compose || {})[room] || {}), ...patch, ...(patch.edit ? { reply: null } : {}), ...(patch.reply ? { edit: null } : {}) } };
  });
  setTimeout(() => {
    const ta = document.querySelector(`textarea[data-room="${CSS.escape(room)}"]`);
    if (ta) ta.focus();
  }, 20);
}

const COMMANDS = [
  ['me', 'acción', '/me saluda'],
  ['shrug', 'texto', '¯\\_(ツ)_/¯'],
  ['tableflip', 'texto', '(╯°□°）╯︵ ┻━┻'],
  ['lenny', 'texto', '( ͡° ͜ʖ ͡°)'],
  ['spoiler', 'texto', 'texto oculto'],
  ['plain', 'texto', 'sin formato'],
  ['html', 'html', '<b>html</b>'],
  ['join', '#sala:servidor', ''],
  ['invite', '@usuario:servidor', ''],
  ['nick', 'nombre', ''],
  ['topic', 'tema', ''],
  ['poll', 'pregunta', ''],
  ['dm', '@usuario:servidor', ''],
  ['rainbow', 'texto', ''],
];

function rainbowHTML(text) {
  const chars = [...text];
  return chars.map((ch, i) => {
    const hue = Math.round((i / Math.max(chars.length, 1)) * 360);
    const c = hslToHex(hue, 85, 55);
    return ch === ' ' ? ' ' : `<font data-mx-color="${c}" color="${c}">${ch.replace(/[&<>]/g, (x) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' }[x]))}</font>`;
  }).join('');
}
function hslToHex(h, s, l) {
  s /= 100; l /= 100;
  const k = (n) => (n + h / 30) % 12;
  const a = s * Math.min(l, 1 - l);
  const f = (n) => Math.round(255 * (l - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1))))).toString(16).padStart(2, '0');
  return `#${f(0)}${f(8)}${f(4)}`;
}

export function Composer({ a, room, summary, thread }) {
  const ckey = thread ? `${room}|${thread}` : room;
  const ctx = useStore((s) => (s.compose || {})[ckey]) || {};
  const drafts = useStore((s) => (s.acct[a] ? s.acct[a].drafts : null)) || {};
  const settings = useStore((s) => s.settings);
  const [text, setText] = useState('');
  const [ac, setAc] = useState(null); // autocompletado
  const [sel, setSel] = useState(0);
  const [rec, setRec] = useState(null);
  const taRef = useRef();
  const loadedFor = useRef(null);

  // Cargar borrador al cambiar de sala.
  useEffect(() => {
    if (loadedFor.current === ckey) return;
    loadedFor.current = ckey;
    const d = !thread && drafts[room];
    setText(d && typeof d === 'object' ? (d.text || '') : (typeof d === 'string' ? d : ''));
    setAc(null);
    // En el teléfono no se abre el teclado solo al entrar a una sala.
    if (!matchMedia('(hover: none)').matches) setTimeout(() => taRef.current && taRef.current.focus(), 30);
  }, [ckey]);

  // Al editar, precargar el texto original.
  useEffect(() => {
    if (ctx.edit) {
      const c = ctx.edit.content || {};
      setText(c.body || '');
      setTimeout(() => { const ta = taRef.current; if (ta) { ta.focus(); ta.setSelectionRange(ta.value.length, ta.value.length); } }, 20);
    }
  }, [ctx.edit && ctx.edit.id]);

  const saveDraft = useMemo(() => debounce((r, v) => {
    rpc.call('room.draft', { a, room: r, data: v.trim() ? { text: v } : null }).catch(() => {});
    mutAcct(a, (ac2) => { ac2.drafts = { ...ac2.drafts, [r]: v.trim() ? { text: v } : undefined }; });
  }, 600), [a]);
  const typingState = useRef(false);
  const lastTyping = useRef(0);
  // Indicador de escritura: "sí" como máximo cada 4 s (sin envíos diferidos que lo reactiven tarde).
  const sendTyping = (r, on) => {
    if (on) {
      const now = Date.now();
      if (!typingState.current || now - lastTyping.current > 4000) {
        typingState.current = true;
        lastTyping.current = now;
        rpc.call('room.typing', { a, room: r, typing: true }).catch(() => {});
      }
      return;
    }
    if (!typingState.current) return;
    typingState.current = false;
    rpc.call('room.typing', { a, room: r, typing: false }).catch(() => {});
  };

  // Autoajuste de altura.
  useLayoutEffect(() => {
    const ta = taRef.current;
    if (!ta) return;
    ta.style.height = 'auto';
    ta.style.height = `${Math.min(ta.scrollHeight, window.innerHeight * 0.4)}px`;
  }, [text]);

  const members = () => {
    const tl = state.tl[tlKey(a, room)];
    return tl ? Object.entries(tl.profiles || {}) : [];
  };

  const updateAutocomplete = (val, caret) => {
    const before = val.slice(0, caret);
    let m = /(?:^|\s)@([^\s@]{0,40})$/.exec(before);
    if (m) {
      const q = m[1].toLowerCase();
      const list = members().filter(([u, p]) => u !== a && (u.toLowerCase().includes(q) || (p.name || '').toLowerCase().includes(q))).slice(0, 8)
        .map(([u, p]) => ({ kind: 'user', id: u, name: p.name || localpart(u), avatar: p.avatar, start: caret - m[1].length - 1 }));
      if (summary && !summary.dm && 'room'.startsWith(q)) list.push({ kind: 'room', id: '@room', name: t('Notificar a todos'), start: caret - m[1].length - 1 });
      setAc(list.length ? list : null); setSel(0); return;
    }
    m = /(?:^|\s):([a-z0-9_+\-]{2,30})$/i.exec(before);
    if (m) {
      const q = m[1].toLowerCase();
      const custom = emotesFor(a, room).filter((e) => e.code.toLowerCase().includes(q)).slice(0, 6)
        .map((e) => ({ kind: 'emote', id: e.code, url: e.url, start: caret - m[1].length - 1 }));
      const std = searchEmoji(q, 8).map((e) => ({ kind: 'emoji', id: e[1], emoji: e[0], start: caret - m[1].length - 1 }));
      const list = [...custom, ...std].slice(0, 10);
      setAc(list.length ? list : null); setSel(0); return;
    }
    m = /^\/([a-z]*)$/.exec(before);
    if (m) {
      const list = COMMANDS.filter(([c]) => c.startsWith(m[1])).map(([c, arg, ex]) => ({ kind: 'cmd', id: c, arg, ex, start: 0 }));
      setAc(list.length ? list : null); setSel(0); return;
    }
    setAc(null);
  };

  const applyAc = (item) => {
    const ta = taRef.current;
    const caret = ta.selectionStart;
    let ins;
    if (item.kind === 'user') ins = `@${item.name} `;
    else if (item.kind === 'room') ins = '@room ';
    else if (item.kind === 'emoji') ins = item.emoji;
    else if (item.kind === 'emote') ins = `:${item.id}: `;
    else if (item.kind === 'cmd') ins = `/${item.id} `;
    const next = text.slice(0, item.start) + ins + text.slice(caret);
    // Guardamos qué nombres corresponden a qué usuarios para generar menciones reales.
    if (item.kind === 'user') mentionMap.current[`@${item.name}`] = item.id;
    setText(next);
    setAc(null);
    setTimeout(() => { ta.focus(); const p = item.start + ins.length; ta.setSelectionRange(p, p); }, 0);
  };
  const mentionMap = useRef({});

  const send = async () => {
    let body = text.trim();
    if (!body) return;
    const base = { a, room, reply_to: ctx.reply ? ctx.reply.id : undefined, thread: thread || undefined, edit: ctx.edit ? ctx.edit.id : undefined };
    // Comandos.
    if (body.startsWith('/') && !body.startsWith('//')) {
      const [cmd, ...rest] = body.slice(1).split(' ');
      const arg = rest.join(' ').trim();
      try {
        switch (cmd) {
          case 'me': await rpc.call('room.send', { ...base, text: arg, markdown: true, msgtype: 'm.emote' }); break;
          case 'shrug': await rpc.call('room.send', { ...base, text: `${arg} ¯\\\\_(ツ)_/¯`.trim(), markdown: true }); break;
          case 'tableflip': await rpc.call('room.send', { ...base, text: `${arg} (╯°□°）╯︵ ┻━┻`.trim(), markdown: false }); break;
          case 'lenny': await rpc.call('room.send', { ...base, text: `${arg} ( ͡° ͜ʖ ͡°)`.trim(), markdown: false }); break;
          case 'plain': await rpc.call('room.send', { ...base, text: arg, markdown: false }); break;
          case 'html': await rpc.call('room.send', { ...base, text: arg.replace(/<[^>]+>/g, ''), html: arg }); break;
          case 'spoiler': await rpc.call('room.send', { ...base, text: `||${arg}||`, html: `<span data-mx-spoiler>${arg.replace(/[&<>]/g, (x) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' }[x]))}</span>` }); break;
          case 'rainbow': await rpc.call('room.send', { ...base, text: arg, html: rainbowHTML(arg) }); break;
          case 'join': { const r = await rpc.call('room.join', { a, target: arg }); update((s) => { s.view = { ...s.view, room: r.room }; }); break; }
          case 'dm': { const r = await rpc.call('dm.open', { a, user: arg }); update((s) => { s.view = { ...s.view, room: r.room }; }); break; }
          case 'invite': await rpc.call('room.invite', { a, room, user: arg }); toast(t('Invitación enviada')); break;
          case 'nick': await rpc.call('room.setState', { a, room, type: 'm.room.member', state_key: a, content: { membership: 'join', displayname: arg } }); break;
          case 'topic': await rpc.call('room.setState', { a, room, type: 'm.room.topic', state_key: '', content: { topic: arg } }); break;
          case 'poll': openModal('poll', { room, question: arg }); break;
          default: toast(t('Comando desconocido: /{c}', { c: cmd }), 'err'); return;
        }
      } catch (e) { toast(e.message, 'err'); return; }
      reset();
      return;
    }
    if (body.startsWith('//')) body = body.slice(1);
    // Menciones: reemplazamos @Nombre por enlaces reales.
    const mentions = [];
    let md = body;
    for (const [label, uid] of Object.entries(mentionMap.current)) {
      if (md.includes(label)) {
        mentions.push(uid);
        md = md.split(label).join(`[${label.slice(1)}](https://matrix.to/#/${uid})`);
      }
    }
    const roomMention = /(^|\s)@room\b/.test(body);
    const emotes = {};
    for (const e of emotesFor(a, room)) if (md.includes(`:${e.code}:`)) emotes[e.code] = e.url;
    try {
      reset();
      const useMd = !(settings.ui && settings.ui.markdown === false);
      await rpc.call('room.send', { ...base, text: md, markdown: useMd || mentions.length > 0, mentions, room_mention: roomMention, emotes });
    } catch (e) {
      toast(e.message, 'err');
      setText(body);
    }
  };

  const reset = () => {
    setText('');
    setAc(null);
    mentionMap.current = {};
    setComposer(ckey, { reply: null, edit: null });
    if (!thread) saveDraft(room, '');
    sendTyping(room, false);
  };

  const editLast = () => {
    const tl = state.tl[tlKey(a, room)];
    if (!tl) return false;
    const last = [...tl.items].reverse().find((i) => i.sender === a && i.type === 'm.room.message' && !i.redacted && !i.id.startsWith('~') && ['m.text', 'm.emote', 'm.notice'].includes(i.content.msgtype));
    if (!last) return false;
    setComposer(ckey, { edit: last });
    return true;
  };

  const onKeyDown = (e) => {
    if (ac) {
      if (e.key === 'ArrowDown') { e.preventDefault(); setSel((x) => (x + 1) % ac.length); return; }
      if (e.key === 'ArrowUp') { e.preventDefault(); setSel((x) => (x - 1 + ac.length) % ac.length); return; }
      if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) { e.preventDefault(); applyAc(ac[sel]); return; }
      if (e.key === 'Escape') { e.preventDefault(); setAc(null); return; }
    }
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      const enterSends = !(settings.ui && settings.ui.ctrlEnter);
      if (enterSends || e.ctrlKey) { e.preventDefault(); send(); return; }
    }
    if (e.key === 'ArrowUp' && !text && !ctx.edit) { if (editLast()) e.preventDefault(); return; }
    if (e.key === 'Escape' && (ctx.reply || ctx.edit)) { e.preventDefault(); if (ctx.edit) setText(''); setComposer(ckey, { reply: null, edit: null }); }
  };

  const onInput = (e) => {
    const v = e.currentTarget.value;
    setText(v);
    updateAutocomplete(v, e.currentTarget.selectionStart);
    if (!thread && !ctx.edit) saveDraft(room, v);
    if (v) sendTyping(room, true);
  };

  const onPaste = (e) => {
    const files = [...(e.clipboardData && e.clipboardData.files || [])];
    if (files.length) { e.preventDefault(); openModal('upload', { files, room, thread, reply: ctx.reply && ctx.reply.id }); }
  };

  const pickFiles = (accept) => {
    const inp = document.createElement('input');
    inp.type = 'file';
    inp.multiple = true;
    if (accept) inp.accept = accept;
    inp.onchange = () => inp.files.length && openModal('upload', { files: [...inp.files], room, thread, reply: ctx.reply && ctx.reply.id });
    inp.click();
  };

  // --- notas de voz ---
  const startRec = async () => {
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true } });
      const mime = ['audio/ogg;codecs=opus', 'audio/webm;codecs=opus', 'audio/mp4'].find((m) => window.MediaRecorder && MediaRecorder.isTypeSupported(m)) || '';
      const mr = new MediaRecorder(stream, mime ? { mimeType: mime, audioBitsPerSecond: 32000 } : undefined);
      const chunks = [];
      const levels = [];
      const actx = new AudioContext();
      const an = actx.createAnalyser();
      an.fftSize = 512;
      actx.createMediaStreamSource(stream).connect(an);
      const buf = new Uint8Array(an.fftSize);
      const iv = setInterval(() => {
        an.getByteTimeDomainData(buf);
        let peak = 0;
        for (const v of buf) peak = Math.max(peak, Math.abs(v - 128));
        levels.push(Math.min(1024, Math.round((peak / 128) * 1024)));
      }, 100);
      mr.ondataavailable = (ev) => ev.data.size && chunks.push(ev.data);
      const started = Date.now();
      mr.start(250);
      const tick = setInterval(() => setRec((r) => (r ? { ...r, ms: Date.now() - started } : r)), 250);
      setRec({ mr, stream, chunks, levels, started, ms: 0, stop: () => { clearInterval(iv); clearInterval(tick); actx.close(); } });
    } catch (e) {
      toast(t('No se pudo acceder al micrófono'), 'err');
    }
  };
  const stopRec = (sendIt) => {
    const r = rec;
    if (!r) return;
    r.stop();
    r.mr.onstop = async () => {
      r.stream.getTracks().forEach((tr) => tr.stop());
      setRec(null);
      if (!sendIt) return;
      const dur = Date.now() - r.started;
      if (dur < 600) return;
      const type = (r.mr.mimeType || 'audio/ogg').split(';')[0];
      const blob = new Blob(r.chunks, { type });
      const ext = type.includes('ogg') ? 'ogg' : type.includes('webm') ? 'webm' : 'm4a';
      const n = 60;
      const wf = [];
      for (let i = 0; i < n; i++) wf.push(r.levels[Math.floor((i * r.levels.length) / n)] || 0);
      const extra = { info: { duration: dur }, 'org.matrix.msc1767.audio': { duration: dur, waveform: wf }, 'org.matrix.msc3245.voice': {} };
      try {
        await upload({ a, room, mode: 'send', name: `nota-de-voz.${ext}`, mime: type, extra: JSON.stringify(extra), thread: thread || '', reply: ctx.reply ? ctx.reply.id : '' }, blob);
      } catch (e) { toast(e.message, 'err'); }
    };
    r.mr.stop();
  };

  const canSend = summary && summary.membership === 'join';
  if (summary && summary.tombstone) {
    return html`<div class="composer"><div class="composer-box"><div class="readonly">${t('Esta sala fue reemplazada.')} </div></div></div>`;
  }
  if (!canSend) return null;

  const tl = state.tl[tlKey(a, room)];
  const nmOf = (u) => (tl && tl.profiles[u] && tl.profiles[u].name) || localpart(u);

  return html`<div class="composer">
    ${ac ? html`<div class="autocomplete">${ac.map((it, i) => html`<div class=${`ac-item${i === sel ? ' sel' : ''}`} onMouseDown=${(e) => { e.preventDefault(); applyAc(it); }}>
      ${it.kind === 'user' ? html`<${Avatar} a=${a} mxc=${it.avatar} name=${it.name} id=${it.id} size=${24} /><span>${it.name}</span><span class="ac-sub">${it.id}</span>`
        : it.kind === 'room' ? html`<${Icon} name="at" /><span>@room</span><span class="ac-sub">${it.name}</span>`
        : it.kind === 'emoji' ? html`<span class="ac-emoji">${it.emoji}</span><span>:${it.id}:</span>`
        : it.kind === 'emote' ? html`<img src=${`/media/${encodeURIComponent(a)}/${it.url.slice(6)}?w=48&h=48`} style="width:24px;height:24px;object-fit:contain" /><span>:${it.id}:</span>`
        : html`<span class="mono">/${it.id}</span><span class="ac-sub">${it.arg}</span>`}
    </div>`)}</div>` : null}
    <div class="composer-box">
      ${ctx.reply || ctx.edit ? html`<div class="composer-ctx"><${Icon} name=${ctx.edit ? 'edit' : 'reply'} size=16 />
        <span class="what">${ctx.edit ? t('Editando mensaje') : html`${t('Respondiendo a')} <b>${nmOf(ctx.reply.sender)}</b>: ${(ctx.reply.content && ctx.reply.content.body) || ''}`}</span>
        <button class="icon-btn sm" onClick=${() => { if (ctx.edit) setText(''); setComposer(ckey, { reply: null, edit: null }); }}><${Icon} name="close" size=16 /></button></div>` : null}
      ${rec ? html`<div class="composer-row"><div class="rec-bar grow"><span class="rec-dot"></span>${formatDuration(rec.ms)} · ${t('Grabando nota de voz')}</div>
        <button class="icon-btn" title=${t('Descartar')} onClick=${() => stopRec(false)}><${Icon} name="trash" /></button>
        <button class="send-btn" title=${t('Enviar')} onClick=${() => stopRec(true)}><${Icon} name="send" size=18 /></button></div>` : html`
      <div class="composer-row">
        <button class="icon-btn" title=${t('Adjuntar')} onClick=${(e) => openMenu(e.currentTarget, [
          off('file_upload') ? null : { icon: 'image', label: t('Foto o video'), onClick: () => pickFiles('image/*,video/*') },
          off('file_upload') ? null : { icon: 'file', label: t('Archivo'), onClick: () => pickFiles('') },
          { icon: 'poll', label: t('Encuesta'), onClick: () => openModal('poll', { room }) },
          { icon: 'sticker', label: t('Sticker'), onClick: () => showEmojiPicker(e.currentTarget, { a, room, stickers: true, onSticker: (st) => sendSticker(a, room, st) }) },
          { icon: 'location', label: t('Ubicación'), onClick: () => shareLocation(a, room) },
          { icon: 'clock', label: t('Programar mensaje'), onClick: () => scheduleCurrent() },
        ])}><${Icon} name="plus" /></button>
        <textarea ref=${taRef} data-room=${ckey} rows="1" value=${text} placeholder=${thread ? t('Responder en el hilo…') : summary && summary.encrypted ? t('Mensaje cifrado…') : t('Mensaje…')}
          spellcheck=${settings.spellcheck !== false} onInput=${onInput} onKeyDown=${onKeyDown} onPaste=${onPaste}
          onBlur=${() => setTimeout(() => setAc(null), 150)} onClick=${(e) => updateAutocomplete(text, e.currentTarget.selectionStart)}></textarea>
        <button class="icon-btn" title=${t('Emoji')} onClick=${(e) => showEmojiPicker(e.currentTarget, { a, room, onPick: (k) => insertAtCaret(k), onSticker: (st) => sendSticker(a, room, st) })}><${Icon} name="smile" /></button>
        ${text.trim() || ctx.edit ? html`<button class="send-btn" title=${t('Enviar')} onClick=${send}><${Icon} name=${ctx.edit ? 'check' : 'send'} size=18 /></button>`
          : html`<button class="icon-btn" title=${t('Nota de voz')} onClick=${startRec}><${Icon} name="mic" /></button>`}
      </div>`}
    </div>
  </div>`;

  function insertAtCaret(s) {
    const ta = taRef.current;
    const st = ta ? ta.selectionStart : text.length;
    const en = ta ? ta.selectionEnd : text.length;
    const next = text.slice(0, st) + s + text.slice(en);
    setText(next);
    setTimeout(() => { if (ta) { ta.focus(); ta.setSelectionRange(st + s.length, st + s.length); } }, 0);
  }

  function scheduleCurrent() {
    if (!text.trim()) { toast(t('Escribí el mensaje primero')); return; }
    openModal('schedule', { room, params: { room, text: text.trim(), markdown: true }, onDone: () => reset() });
  }
}

export async function sendSticker(a, room, st) {
  try {
    await rpc.call('room.send', { a, room, type: 'm.sticker', content: { body: st.body || st.code, url: st.url, info: st.info || { w: 256, h: 256, mimetype: 'image/png' } } });
  } catch (e) { toast(e.message, 'err'); }
}

function shareLocation(a, room) {
  if (!navigator.geolocation) { toast(t('Ubicación no disponible'), 'err'); return; }
  navigator.geolocation.getCurrentPosition(async (pos) => {
    const { latitude: la, longitude: lo } = pos.coords;
    const uri = `geo:${la.toFixed(6)},${lo.toFixed(6)}`;
    await rpc.call('room.send', { a, room, type: 'm.room.message', content: {
      msgtype: 'm.location', body: `${t('Ubicación')}: ${uri}`, geo_uri: uri,
      'org.matrix.msc3488.location': { uri }, 'org.matrix.msc3488.asset': { type: 'm.self' }, 'org.matrix.msc3488.ts': Date.now(),
    } }).catch((e) => toast(e.message, 'err'));
  }, () => toast(t('No se pudo obtener la ubicación'), 'err'), { enableHighAccuracy: false, timeout: 10000 });
}
