// Renderizado del contenido de los eventos.
import { html, useState, useRef, useEffect, useMemo } from '../vendor/preact-htm.js';
import { t } from './i18n.js';
import { Icon } from './icons.js';
import { state, update, toast, useStore } from './store.js';
import { rpc } from './rpc.js';
import { mediaURL, encMediaURL, sanitizeHTML, linkify, isEmojiOnly, blurhashURL, formatSize, formatDuration, localpart, escapeHTML } from './util.js';

// --- texto ---
export function bodyHTML(content, ctx) {
  if (!content) return '';
  if (content.format === 'org.matrix.custom.html' && typeof content.formatted_body === 'string') {
    // Quitamos el respaldo de respuesta (<mx-reply>) que el saneador también descarta.
    return sanitizeHTML(content.formatted_body, ctx);
  }
  let body = typeof content.body === 'string' ? content.body : '';
  // Respaldo de respuesta antiguo en texto plano: líneas "> ..." al inicio.
  if (ctx.isReply && body.startsWith('> ')) {
    const idx = body.indexOf('\n\n');
    if (idx > 0) body = body.slice(idx + 2);
  }
  return linkify(body, ctx);
}

export function TextBody({ content, ctx, edited }) {
  const raw = typeof content.body === 'string' ? content.body : '';
  const bigEmoji = !(state.settings && state.settings.ui && state.settings.ui.bigEmoji === false);
  const emojiOnly = bigEmoji && !content.formatted_body && isEmojiOnly(raw);
  const htmlStr = useMemo(() => bodyHTML(content, ctx), [content, ctx.a]);
  const emoteOnly = content.formatted_body && /^(\s*<img[^>]*data-mx-emoticon[^>]*>\s*){1,5}$/.test(content.formatted_body);
  const cls = `msg-body${emojiOnly || emoteOnly ? ' emoji-only' : ''}${content.msgtype === 'm.notice' ? ' notice' : ''}`;
  return html`<div class=${cls} dangerouslySetInnerHTML=${{ __html: htmlStr + (edited ? `<span class="edited">(${escapeHTML(t('editado'))})</span>` : '') }}></div>`;
}

// --- multimedia ---
function boxSize(info = {}, maxW = 400, maxH = 320) {
  let w = info.w || info.thumbnail_info?.w, h = info.h || info.thumbnail_info?.h;
  if (!w || !h) return { w: Math.min(maxW, 320), h: 220, unknown: true };
  const s = Math.min(maxW / w, maxH / h, 1);
  return { w: Math.max(Math.round(w * s), 48), h: Math.max(Math.round(h * s), 48) };
}

export function srcFor(a, item, thumb) {
  const c = item.content || {};
  if (c.file) return encMediaURL(a, item.room, item.id, thumb && !!(c.info && c.info.thumbnail_file));
  if (thumb) {
    if (c.info && c.info.thumbnail_url) return mediaURL(a, c.info.thumbnail_url);
    return mediaURL(a, c.url, { w: 800, h: 600 });
  }
  return mediaURL(a, c.url);
}

export function ImageContent({ a, item, sticker }) {
  const c = item.content || {};
  const info = c.info || {};
  const size = boxSize(info, sticker ? 180 : 400, sticker ? 180 : 320);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState(false);
  const local = item.id.startsWith('~') && !c.url && !c.file;
  const bh = info['xyz.amorgan.blurhash'];
  const isGif = info.mimetype === 'image/gif';
  const src = local ? '' : (isGif ? srcFor(a, item, false) : srcFor(a, item, true));
  const open = () => !sticker && !local && update((s) => { s.viewer = { a, item }; });
  return html`<div class=${`media${sticker ? ' sticker' : ''}`} style=${`width:${size.w}px;aspect-ratio:${size.w}/${size.h}`} onClick=${open}>
    ${!loaded && bh ? html`<img class="ph" src=${blurhashURL(bh)} alt="" />` : null}
    ${src && !error ? html`<img src=${src} alt=${c.body || ''} loading="lazy" decoding="async" onLoad=${() => setLoaded(true)} onError=${() => setError(true)}
      style=${loaded ? '' : 'opacity:0'} />` : null}
    ${local ? html`<div class="ph-label"><${UploadRing} item=${item} /></div>` : null}
    ${error ? html`<div class="ph-label">${t('No se pudo cargar la imagen')}</div>` : null}
  </div>`;
}

// Anillo de progreso para fotos y videos que se están subiendo.
function UploadRing({ item }) {
  const txn = item.txn || item.id.slice(1);
  const tr = useStore((s) => (s.transfers || {})[txn]);
  if (!tr || !tr.total || tr.state !== 'running') return html`<div class="spinner"></div>`;
  const pct = Math.min(100, Math.floor((tr.done / tr.total) * 100));
  return html`<div class="up-ring" style=${`--p:${pct}`}><span>${pct}%</span></div>`;
}

export function VideoContent({ a, item }) {
  const c = item.content || {};
  const info = c.info || {};
  const size = boxSize(info, 420, 320);
  const local = item.id.startsWith('~') && !c.url && !c.file;
  // Como en Telegram: un clic abre el visor (sobre la app, no en pantalla completa).
  const open = () => !local && update((s) => { s.viewer = { a, item }; s.pip = null; });
  // Los "GIF" (videos cortos en bucle) se reproducen solos, sin sonido.
  const gif = !!(info['fi.mau.gif'] || info['fi.mau.loop'] || info['fi.mau.autoplay']);
  const poster = local ? '' : (info.thumbnail_url || info.thumbnail_file ? srcFor(a, item, true) : '');
  const bh = info['xyz.amorgan.blurhash'];
  const [loaded, setLoaded] = useState(false);
  return html`<div class="media video" style=${`width:${size.w}px;aspect-ratio:${size.w}/${size.h}`} onClick=${open}>
    ${gif && !local ? html`<video src=${srcFor(a, item, false)} autoplay muted loop playsinline style="width:100%;height:100%;object-fit:cover" />` : html`
      ${bh && !loaded ? html`<img class="ph" src=${blurhashURL(bh)} alt="" />` : null}
      ${poster ? html`<img src=${poster} alt="" loading="lazy" onLoad=${() => setLoaded(true)} />` : null}
      <button class="ph-label" style="border:0;background:transparent;cursor:pointer" aria-label=${t('Reproducir')}>
        ${local ? html`<${UploadRing} item=${item} />` : html`<span style="width:54px;height:54px;border-radius:50%;background:rgba(0,0,0,.55);color:#fff;display:grid;place-items:center"><${Icon} name="play" size=24 /></span>`}
      </button>`}
    ${info.duration ? html`<span style="position:absolute;right:8px;bottom:8px;background:rgba(0,0,0,.6);color:#fff;font-size:12px;padding:1px 6px;border-radius:6px">${formatDuration(info.duration)}</span>` : null}
  </div>`;
}

export function AudioContent({ a, item }) {
  const c = item.content || {};
  const info = c.info || {};
  const voice = !!c['org.matrix.msc3245.voice'];
  const wave = (c['org.matrix.msc1767.audio'] && c['org.matrix.msc1767.audio'].waveform) || [];
  const dur = (c['org.matrix.msc1767.audio'] && c['org.matrix.msc1767.audio'].duration) || info.duration || 0;
  const audioRef = useRef();
  const [playing, setPlaying] = useState(false);
  const [pos, setPos] = useState(0);
  const local = item.id.startsWith('~') && !c.url && !c.file;
  const bars = useMemo(() => {
    const n = 36;
    if (!wave.length) return Array.from({ length: n }, (_, i) => 0.25 + 0.5 * Math.abs(Math.sin(i * 1.7)));
    const out = [];
    for (let i = 0; i < n; i++) out.push(Math.max(0.08, (wave[Math.floor((i * wave.length) / n)] || 0) / 1024));
    return out;
  }, [wave]);
  const toggle = () => {
    const el = audioRef.current;
    if (!el) return;
    if (el.paused) el.play().catch(() => toast(t('No se pudo reproducir el audio'), 'err')); else el.pause();
  };
  const total = dur || (audioRef.current && audioRef.current.duration * 1000) || 0;
  const frac = total ? pos / total : 0;
  return html`<div class="audio-card">
    <button class="icon-btn" onClick=${toggle} disabled=${local}>${local ? html`<div class="spinner sm"></div>` : html`<${Icon} name=${playing ? 'pause' : 'play'} size=18 />`}</button>
    <div class="wave" onClick=${(e) => {
      const el = audioRef.current;
      if (!el || !el.duration) return;
      const r = e.currentTarget.getBoundingClientRect();
      el.currentTime = ((e.clientX - r.left) / r.width) * el.duration;
    }}>${bars.map((b, i) => html`<span class=${i / bars.length < frac ? 'on' : ''} style=${`height:${Math.round(b * 100)}%`}></span>`)}</div>
    <span class="dur">${formatDuration(playing || pos ? pos : total)}</span>
    ${!local ? html`<audio ref=${audioRef} preload="none" src=${srcFor(a, item, false)}
      onPlay=${() => setPlaying(true)} onPause=${() => setPlaying(false)} onEnded=${() => { setPlaying(false); setPos(0); }}
      onTimeUpdate=${(e) => setPos(e.currentTarget.currentTime * 1000)} />` : null}
    ${voice ? null : html`<span class="small muted" style="max-width:120px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${c.body}</span>`}
  </div>`;
}

// Progreso de subidas/descargas (por txn o id de evento), compartido por las tarjetas de archivo.
function TransferBar({ tr }) {
  if (!tr || tr.state === 'done') return null;
  const pct = tr.total ? Math.min(100, Math.floor((tr.done / tr.total) * 100)) : 0;
  const label = tr.state === 'error' ? tr.error
    : tr.state === 'verifying' ? t('Verificando integridad…')
    : `${tr.dir === 'up' ? t('Subiendo') : t('Descargando')} ${pct}% · ${formatSize(tr.done)} / ${formatSize(tr.total)}${tr.parts > 1 ? ` · ${t('parte {n} de {m}', { n: tr.part, m: tr.parts })}` : ''}`;
  return html`<div class=${`transfer${tr.state === 'error' ? ' err' : ''}`}>
    <div class="tbar"><span style=${`width:${tr.state === 'verifying' ? 100 : pct}%`}></span></div>
    <div class="tlabel">${label}</div>
  </div>`;
}

export function FileContent({ a, item }) {
  const c = item.content || {};
  const info = c.info || {};
  const multipart = c.msgtype === 'io.hilvan.multipart';
  const parts = multipart ? (c['io.hilvan.parts'] || []).length : 0;
  const local = item.id.startsWith('~') && !c.url && !c.file && !parts;
  const txn = item.txn || (item.id.startsWith('~') ? item.id.slice(1) : '');
  const tr = useStore((s) => (s.transfers || {})[local ? txn : item.id]);
  const name = c.filename || c.body || t('Archivo');
  const save = () => rpc.call('file.save', { a, room: item.room, event: item.id }).catch((e) => toast(e.message, 'err'));
  return html`<div class="file-card col-card">
    <div class="row-in">
      <${Icon} name="file" size=28 />
      <div style="flex:1;min-width:0"><div class="fname">${name}</div>
        <div class="fsize">${formatSize(info.size)}${info.mimetype ? ` · ${info.mimetype}` : ''}${parts > 1 ? ` · ${t('{n} partes', { n: parts })}` : ''}</div></div>
      ${local ? html`<div class="spinner sm"></div>` : html`<button class="icon-btn" disabled=${tr && tr.state === 'running'} onClick=${save} title=${t('Guardar en Descargas (verificado)')}><${Icon} name="download" /></button>`}
    </div>
    ${multipart && c['io.hilvan.caption'] ? html`<div class="small">${c['io.hilvan.caption']}</div>` : null}
    <${TransferBar} tr=${tr} />
  </div>`;
}

export function LocationContent({ item }) {
  const c = item.content || {};
  const geo = c.geo_uri || (c['m.location'] && c['m.location'].uri) || '';
  const m = /^geo:(-?[\d.]+),(-?[\d.]+)/.exec(geo);
  const link = m ? `https://www.openstreetmap.org/?mlat=${m[1]}&mlon=${m[2]}#map=16/${m[1]}/${m[2]}` : '';
  return html`<div class="file-card"><${Icon} name="location" size=26 />
    <div style="flex:1;min-width:0"><div class="fname">${c.body || t('Ubicación')}</div><div class="fsize">${m ? `${m[1]}, ${m[2]}` : geo}</div></div>
    ${link ? html`<a class="icon-btn" href=${link} target="_blank" rel="noopener noreferrer" data-ext="1"><${Icon} name="globe" /></a>` : null}
  </div>`;
}

// --- encuestas ---
export function PollContent({ a, item, me }) {
  const c = item.content || {};
  const ps = c['org.matrix.msc3381.poll.start'] || {};
  const stable = c['m.poll'] || {};
  const question = (ps.question && (ps.question['org.matrix.msc1767.text'] || ps.question.body)) ||
    (stable.question && stable.question['m.text'] && stable.question['m.text'][0] && stable.question['m.text'][0].body) || c.body || '';
  const answers = (ps.answers || []).map((x) => ({ id: x.id, text: x['org.matrix.msc1767.text'] || x.body || '' }))
    .concat((stable.answers || []).map((x) => ({ id: x['m.id'], text: (x['m.text'] && x['m.text'][0] && x['m.text'][0].body) || '' })));
  const kind = ps.kind || stable.kind || '';
  const disclosed = kind.endsWith('disclosed') && !kind.endsWith('undisclosed');
  const maxSel = ps.max_selections || stable.max_selections || 1;
  const agg = item.poll || { totals: {}, votes: {}, mine: [] };
  const totalVotes = Object.keys(agg.votes || {}).length;
  const mine = agg.mine || [];
  const ended = agg.ended;
  const showResults = ended || disclosed || mine.length > 0;
  const vote = (id) => {
    if (ended) return;
    let sel = mine.includes(id) ? mine.filter((x) => x !== id) : maxSel === 1 ? [id] : [...mine, id].slice(-maxSel);
    rpc.call('poll.vote', { a, room: item.room, poll: item.id, answers: sel }).catch((e) => toast(e.message, 'err'));
  };
  return html`<div class="poll">
    <div class="q">📊 ${question}</div>
    ${answers.map((ans) => {
      const n = (agg.totals || {})[ans.id] || 0;
      const pct = totalVotes ? Math.round((n * 100) / totalVotes) : 0;
      return html`<div class=${`opt${mine.includes(ans.id) ? ' mine' : ''}`} onClick=${() => vote(ans.id)}>
        ${showResults ? html`<div class="bar" style=${`width:${pct}%`}></div>` : null}
        <span>${mine.includes(ans.id) ? '✓ ' : ''}${ans.text}</span>${showResults ? html`<span class="muted">${n}</span>` : null}
      </div>`;
    })}
    <div class="foot"><span>${ended ? t('Encuesta finalizada') : t('{n} voto|{n} votos', { n: totalVotes })}${!disclosed && !ended ? ` · ${t('resultados al finalizar')}` : ''}</span>
      ${!ended && item.sender === me ? html`<button class="btn ghost" style="height:24px;padding:0 8px" onClick=${() => rpc.call('poll.end', { a, room: item.room, poll: item.id })}>${t('Finalizar')}</button>` : null}
    </div>
  </div>`;
}

// --- eventos de estado ---
export function stateText(item, nm) {
  const c = item.content || {};
  const p = item.prev || {};
  const who = nm(item.sender);
  switch (item.type) {
    case 'm.room.member': {
      const target = nm(item.sk);
      const m = c.membership, pm = p.membership;
      if (m === 'join' && pm === 'join') {
        if (c.displayname !== p.displayname) {
          if (!p.displayname) return t('{u} eligió el nombre {n}', { u: item.sk, n: c.displayname });
          if (!c.displayname) return t('{u} quitó su nombre', { u: p.displayname });
          return t('{a} ahora se llama {b}', { a: p.displayname, b: c.displayname });
        }
        if (c.avatar_url !== p.avatar_url) return t('{u} cambió su foto', { u: target });
        return null;
      }
      if (m === 'join') return pm === 'invite' ? t('{u} aceptó la invitación', { u: target }) : t('{u} se unió', { u: target });
      if (m === 'invite') return t('{a} invitó a {b}', { a: who, b: target });
      if (m === 'leave') {
        if (item.sender !== item.sk) return pm === 'ban' ? t('{a} readmitió a {b}', { a: who, b: target }) : pm === 'invite' ? t('{a} retiró la invitación a {b}', { a: who, b: target }) : t('{a} quitó a {b}', { a: who, b: target });
        return pm === 'invite' ? t('{u} rechazó la invitación', { u: target }) : t('{u} salió', { u: target });
      }
      if (m === 'ban') return t('{a} expulsó a {b}', { a: who, b: target }) + (c.reason ? `: ${c.reason}` : '');
      if (m === 'knock') return t('{u} pidió unirse', { u: target });
      return null;
    }
    case 'm.room.name': return c.name ? t('{u} cambió el nombre a «{n}»', { u: who, n: c.name }) : t('{u} quitó el nombre de la sala', { u: who });
    case 'm.room.topic': return c.topic ? t('{u} cambió el tema: {n}', { u: who, n: c.topic }) : t('{u} quitó el tema', { u: who });
    case 'm.room.avatar': return t('{u} cambió la imagen de la sala', { u: who });
    case 'm.room.create': return c.type === 'm.space' ? t('{u} creó el espacio', { u: who }) : t('{u} creó la sala', { u: who });
    case 'm.room.encryption': return t('{u} activó el cifrado de extremo a extremo', { u: who });
    case 'm.room.join_rules': return { public: t('{u} hizo pública la sala', { u: who }), invite: t('{u} hizo la sala solo por invitación', { u: who }), knock: t('{u} permite pedir acceso', { u: who }), restricted: t('{u} limitó el acceso a miembros del espacio', { u: who }) }[c.join_rule] || null;
    case 'm.room.history_visibility': return t('{u} cambió quién puede ver el historial', { u: who });
    case 'm.room.guest_access': return null;
    case 'm.room.power_levels': return t('{u} cambió los permisos', { u: who });
    case 'm.room.pinned_events': return t('{u} cambió los mensajes fijados', { u: who });
    case 'm.room.canonical_alias': return c.alias ? t('{u} estableció la dirección {a}', { u: who, a: c.alias }) : null;
    case 'm.room.tombstone': return t('La sala fue actualizada a una nueva versión');
    case 'm.room.server_acl': return t('{u} cambió los servidores permitidos', { u: who });
    case 'im.vector.modular.widgets': return c.type === 'jitsi' || (c.url || '').includes('jitsi') ? (c.url ? t('{u} inició una videollamada grupal', { u: who }) : t('{u} terminó la videollamada grupal', { u: who })) : (c.url ? t('{u} agregó un widget', { u: who }) : t('{u} quitó un widget', { u: who }));
    case 'm.space.child': return null;
    case 'm.space.parent': return null;
    case 'm.bridge': case 'uk.half-shot.bridge': return t('Esta sala está conectada con {p}', { p: (c.protocol && (c.protocol.displayname || c.protocol.id)) || '?' });
    default: return null;
  }
}

export function callText(item, nm) {
  const c = item.content || {};
  if (item.type === 'm.call.invite') {
    const video = /m=video/.test((c.offer && c.offer.sdp) || '');
    return video ? t('{u} inició una videollamada', { u: nm(item.sender) }) : t('{u} inició una llamada', { u: nm(item.sender) });
  }
  if (item.type === 'm.call.hangup') return t('Llamada finalizada');
  if (item.type.includes('rtc.notification') || item.type.includes('call.notify')) return t('{u} inició una llamada grupal', { u: nm(item.sender) });
  return null;
}

// --- clic delegado en cuerpos de mensaje (spoilers, menciones, enlaces) ---
export function onBodyClick(e, handlers) {
  const sp = e.target.closest('[data-spoiler]');
  if (sp) { sp.classList.toggle('shown'); e.preventDefault(); return; }
  const pill = e.target.closest('.pill');
  if (pill) {
    e.preventDefault();
    if (pill.dataset.user) handlers.user && handlers.user(pill.dataset.user, pill);
    else if (pill.dataset.room) handlers.room && handlers.room(pill.dataset.room, pill.dataset.event, (pill.dataset.via || '').split(',').filter(Boolean));
    return;
  }
  const a = e.target.closest('a[data-ext]');
  if (a) {
    e.preventDefault();
    openExternal(a.getAttribute('href'));
  }
}

export async function openExternal(url) {
  try {
    const r = await rpc.call('app.openExternal', { url });
    if (!r.opened) window.open(url, '_blank', 'noopener,noreferrer');
  } catch { window.open(url, '_blank', 'noopener,noreferrer'); }
}

export function senderNameFn(profiles) {
  return (uid) => (profiles && profiles[uid] && profiles[uid].name) || localpart(uid) || uid;
}

// Guardar como…: el selector del sistema elige carpeta y nombre; el archivo se baja, se verifica
// y avisa al terminar ("file.saved").
export function saveAs(a, item) {
  const c = item.content || {};
  return rpc.call('file.saveAs', { a, room: item.room, event: item.id }).then((r) => {
    if (r && r.path && (c.url || c.file)) toast(t('Guardando…'));
  }).catch((e) => toast(e.message, 'err'));
}

// saveTextFile guarda un texto como archivo: en la computadora con la descarga del navegador; en
// Android (donde el WebView no descarga) con el selector "Guardar como" del sistema.
export async function saveTextFile(name, text) {
  // Solo la ventana de Linux (WebKitGTK) descarga archivos sola; el resto usa el diálogo del sistema.
  if (state.info && state.info.os !== 'linux') {
    const r = await rpc.call('file.saveText', { name, text });
    if (r && r.path) toast(`${t('Guardado en')} ${r.path}`);
    return !!(r && r.path);
  }
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }));
  const el = document.createElement('a');
  el.href = url; el.download = name; el.click();
  setTimeout(() => URL.revokeObjectURL(url), 2000);
  return true;
}
