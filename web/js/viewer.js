// Visor de fotos y videos al estilo de Telegram: se abre sobre la app (no en pantalla completa),
// con controles propios, navegación por los medios del chat y reproductor flotante.
// La pantalla completa es opcional: botón, doble clic o la tecla F.
import { html, useEffect, useLayoutEffect, useRef, useState } from '../vendor/preact-htm.js';
import { useStore, state, update, tlKey, toast } from './store.js';
import { t } from './i18n.js';
import { Icon } from './icons.js';
import { Avatar } from './ui.js';
import { rpc } from './rpc.js';
import { srcFor, saveAs } from './message.js';
import { openMenu } from './ui.js';
import { formatShort } from './util.js';

const load = (k, d) => { try { const v = JSON.parse(localStorage.getItem(k)); return v === null ? d : v; } catch { return d; } };
const save = (k, v) => { try { localStorage.setItem(k, JSON.stringify(v)); } catch { /* sin almacenamiento */ } };

const isMedia = (it) => {
  const c = it.content || {};
  return !it.redacted && (c.msgtype === 'm.image' || c.msgtype === 'm.video') && (c.url || c.file);
};

export function openViewer(a, item, time = 0) { update((s) => { s.viewer = { a, item, time }; s.pip = null; }); }
const closeViewer = () => update((s) => { s.viewer = null; });

function fmt(sec) {
  if (!isFinite(sec) || sec < 0) sec = 0;
  const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60), s = Math.floor(sec % 60);
  return `${h ? `${h}:${String(m).padStart(2, '0')}` : m}:${String(s).padStart(2, '0')}`;
}

export function Viewer() {
  const v = useStore((s) => s.viewer);
  const items = useStore((s) => (s.viewer ? ((s.tl[tlKey(s.viewer.a, s.viewer.item.room)] || {}).items || []) : []));
  // Zoom y desplazamiento de la imagen: s = escala, x/y = corrimiento en píxeles.
  const [tf, setTf] = useState({ s: 1, x: 0, y: 0 });
  const [dragging, setDragging] = useState(false);
  const setZoom = (f) => setTf((t0) => { const s2 = typeof f === 'function' ? f(t0.s) : f; return s2 <= 1 ? { s: Math.max(s2, 0.3), x: 0, y: 0 } : { ...t0, s: s2 }; });
  const gest = useRef({ pts: new Map(), start: null, lastTap: 0 });
  const stageRef = useRef();
  const media = items.filter(isMedia);
  const idx = v ? media.findIndex((x) => x.id === v.item.id) : -1;
  const go = (d) => {
    const n = media[idx + d];
    if (n) update((s) => { s.viewer = { a: v.a, item: n }; });
  };
  useEffect(() => { setTf({ s: 1, x: 0, y: 0 }); }, [v && v.item.id]);
  useEffect(() => {
    if (!v) return undefined;
    const onKey = (e) => {
      if (e.target && /^(INPUT|TEXTAREA)$/.test(e.target.tagName)) return;
      if (e.key === 'Escape' && !document.fullscreenElement) { e.stopPropagation(); closeViewer(); } else if (e.key === 'ArrowLeft' && !e.shiftKey) go(-1);
      else if (e.key === 'ArrowRight' && !e.shiftKey) go(1);
    };
    window.addEventListener('keydown', onKey, true);
    return () => window.removeEventListener('keydown', onKey, true);
  }, [v && v.item.id, idx, media.length]);
  if (!v) return null;
  const { a, item } = v;

  // Gestos táctiles (como en Telegram): pellizcar para acercar, arrastrar para mover, doble toque,
  // deslizar a los costados para pasar de foto y hacia abajo para cerrar.
  const center = () => { const r = stageRef.current.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; };
  const snapshot = () => {
    const g = gest.current;
    const p = [...g.pts.values()];
    const mid = p.length > 1 ? { x: (p[0].x + p[1].x) / 2, y: (p[0].y + p[1].y) / 2 } : p[0];
    const dist = p.length > 1 ? Math.hypot(p[0].x - p[1].x, p[0].y - p[1].y) : 0;
    g.start = { tf: { ...tfRef.current }, mid, dist, n: p.length, t: Date.now() };
  };
  const tfRef = useRef(tf);
  tfRef.current = tf;
  const onDown = (e) => {
    if (e.pointerType === 'mouse' || e.target.closest('button')) return;
    const g = gest.current;
    g.pts.set(e.pointerId, { x: e.clientX, y: e.clientY });
    try { e.currentTarget.setPointerCapture(e.pointerId); } catch { /* sin captura: igual seguimos el gesto */ }
    snapshot();
    setDragging(true);
  };
  const onMove = (e) => {
    const g = gest.current;
    if (!g.pts.has(e.pointerId) || !g.start) return;
    g.pts.set(e.pointerId, { x: e.clientX, y: e.clientY });
    const p = [...g.pts.values()];
    const st = g.start;
    if (p.length >= 2 && st.n >= 2 && !video) {
      const mid = { x: (p[0].x + p[1].x) / 2, y: (p[0].y + p[1].y) / 2 };
      const dist = Math.hypot(p[0].x - p[1].x, p[0].y - p[1].y);
      const s2 = Math.max(0.8, Math.min(6, st.tf.s * dist / (st.dist || 1)));
      const c0 = center();
      const k = s2 / st.tf.s;
      // El punto bajo los dedos queda fijo mientras se acerca.
      setTf({ s: s2, x: mid.x - c0.x - (st.mid.x - c0.x - st.tf.x) * k, y: mid.y - c0.y - (st.mid.y - c0.y - st.tf.y) * k });
    } else if (p.length === 1) {
      const dx = p[0].x - st.mid.x, dy = p[0].y - st.mid.y;
      if (st.tf.s > 1.02) setTf({ ...st.tf, x: st.tf.x + dx, y: st.tf.y + dy });
      else if (!video) setTf({ s: 1, x: dx, y: Math.max(0, dy) });
    }
  };
  const onUp = (e) => {
    const g = gest.current;
    if (!g.pts.has(e.pointerId)) return;
    const st = g.start;
    const last = g.pts.get(e.pointerId);
    g.pts.delete(e.pointerId);
    if (g.pts.size) { snapshot(); return; }
    setDragging(false);
    g.start = null;
    if (!st) return;
    const dx = last.x - st.mid.x, dy = last.y - st.mid.y;
    const moved = Math.hypot(dx, dy) > 12 || st.n > 1;
    if (moved) g.movedAt = Date.now();
    if (st.tf.s <= 1.02 && st.n === 1 && !video) {
      if (Math.abs(dx) > 70 && Math.abs(dx) > Math.abs(dy)) { setTf({ s: 1, x: 0, y: 0 }); go(dx < 0 ? 1 : -1); return; }
      if (dy > 110 && Math.abs(dy) > Math.abs(dx)) { closeViewer(); return; }
    }
    if (!moved && st.n === 1 && !video) {
      const now = Date.now();
      if (now - g.lastTap < 320) {
        g.lastTap = 0;
        if (tfRef.current.s > 1.02) setTf({ s: 1, x: 0, y: 0 });
        else {
          const c0 = center();
          const s2 = 2.5;
          setTf({ s: s2, x: -(last.x - c0.x) * (s2 - 1), y: -(last.y - c0.y) * (s2 - 1) });
        }
        return;
      }
      g.lastTap = now;
    }
    const cur = tfRef.current;
    if (cur.s <= 1.02) setTf({ s: 1, x: 0, y: 0 });
  };
  const c = item.content || {};
  const video = c.msgtype === 'm.video';
  const prof = ((state.tl[tlKey(a, item.room)] || {}).profiles || {})[item.sender] || {};
  const saveFile = () => rpc.call('file.save', { a, room: item.room, event: item.id }).catch((e) => toast(e.message, 'err'));
  const menu = (e) => {
    if (e.shiftKey) return;
    e.preventDefault();
    openMenu(e, [
      { icon: 'download', label: t('Guardar como…'), onClick: () => saveAs(a, item) },
      { icon: 'folder', label: t('Guardar en Descargas'), onClick: saveFile },
    ]);
  };
  return html`<div class="viewer" onContextMenu=${menu} onClick=${(e) => { if (e.target.classList.contains('viewer-stage') && !gest.current.start && Date.now() - (gest.current.movedAt || 0) > 400) closeViewer(); }}>
    <div class="viewer-bar">
      <${Avatar} a=${a} mxc=${prof.avatar} name=${prof.name || item.sender} id=${item.sender} size=${30} />
      <div class="grow" style="min-width:0"><div class="vb-name">${prof.name || item.sender}</div>
        <div class="vb-sub">${formatShort(item.ts)}${media.length > 1 && idx >= 0 ? ` · ${t('{n} de {m}', { n: idx + 1, m: media.length })}` : ''}</div></div>
      ${video ? null : html`<button class="icon-btn" title=${t('Acercar')} onClick=${() => setZoom((z) => Math.min(z * 1.4, 6))}><${Icon} name="plus" /></button>
        <button class="icon-btn" title=${t('Alejar')} onClick=${() => setZoom((z) => Math.max(z / 1.4, 0.3))}><${Icon} name="minimize" /></button>`}
      <button class="icon-btn" title=${t('Guardar como…')} onClick=${() => saveAs(a, item)}><${Icon} name="download" /></button>
      <button class="icon-btn" title=${t('Cerrar (Esc)')} onClick=${closeViewer}><${Icon} name="close" /></button>
    </div>
    <div class=${`viewer-stage${dragging ? ' gesturing' : ''}`} ref=${stageRef} onWheel=${(e) => { if (!video) setZoom((z) => Math.max(0.3, Math.min(6, z * (e.deltaY < 0 ? 1.1 : 0.9)))); }}
      onPointerDown=${onDown} onPointerMove=${onMove} onPointerUp=${onUp} onPointerCancel=${onUp}>
      ${idx > 0 ? html`<button class="v-nav prev" title=${t('Anterior')} onClick=${() => go(-1)}><${Icon} name="chevronLeft" size=28 /></button>` : null}
      ${video ? html`<${VideoPlayer} key=${item.id} a=${a} item=${item} startAt=${v.time || 0} />`
        : html`<img src=${srcFor(a, item, false)} alt=${c.body || ''} draggable="false"
            style=${`transform:translate(${tf.x}px,${tf.y}px) scale(${tf.s});${tf.s === 1 && tf.y > 0 ? `opacity:${Math.max(0.35, 1 - tf.y / 400)}` : ''}`}
            onDblClick=${() => setZoom((z) => (z > 1 ? 1 : 2))} />`}
      ${idx >= 0 && idx < media.length - 1 ? html`<button class="v-nav next" title=${t('Siguiente')} onClick=${() => go(1)}><${Icon} name="chevronRight" size=28 /></button>` : null}
    </div>
    ${c.body && !/\.(mp4|webm|mov|mkv|jpe?g|png|gif|webp)$/i.test(c.body) ? html`<div class="viewer-caption">${c.body}</div>` : null}
  </div>`;
}

// Reproductor con controles propios (los de WebKit abrían la pantalla completa).
function VideoPlayer({ a, item, mini, startAt = 0, onExpand, onClose }) {
  const box = useRef();
  const vid = useRef();
  const [playing, setPlaying] = useState(false);
  const [time, setTime] = useState(0);
  const [dur, setDur] = useState(0);
  const [buf, setBuf] = useState(0);
  const [vol, setVol] = useState(() => load('hv.vol', 1));
  const [muted, setMuted] = useState(() => load('hv.muted', false));
  const [rate, setRate] = useState(1);
  const [idle, setIdle] = useState(false);
  const [fs, setFs] = useState(false);
  const [err, setErr] = useState(false);
  const idleT = useRef(0);
  const src = srcFor(a, item, false);
  const poster = (item.content.info || {}).thumbnail_url || (item.content.info || {}).thumbnail_file ? srcFor(a, item, true) : '';
  useLayoutEffect(() => {
    const el = vid.current;
    if (!el) return;
    el.volume = vol;
    el.muted = muted;
  }, [vol, muted]);
  useEffect(() => { if (vid.current) vid.current.playbackRate = rate; }, [rate]);
  useEffect(() => {
    const onFs = () => setFs(document.fullscreenElement === box.current);
    document.addEventListener('fullscreenchange', onFs);
    return () => document.removeEventListener('fullscreenchange', onFs);
  }, []);
  const wake = () => {
    setIdle(false);
    clearTimeout(idleT.current);
    idleT.current = setTimeout(() => setIdle(true), 2500);
  };
  const toggle = () => { const el = vid.current; if (!el) return; if (el.paused) el.play().catch(() => {}); else el.pause(); };
  const seek = (s) => { const el = vid.current; if (el && isFinite(el.duration)) el.currentTime = Math.max(0, Math.min(el.duration, s)); };
  const full = () => {
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    else if (box.current && box.current.requestFullscreen) box.current.requestFullscreen().catch(() => {});
  };
  const toPip = () => {
    const el = vid.current;
    update((s) => { s.pip = { a, item, time: el ? el.currentTime : 0 }; s.viewer = null; });
  };
  useEffect(() => {
    if (mini) return undefined;
    const onKey = (e) => {
      if (e.target && /^(INPUT|TEXTAREA)$/.test(e.target.tagName)) return;
      const el = vid.current;
      if (!el) return;
      if (e.key === ' ' || e.key === 'k') { e.preventDefault(); toggle(); } else if (e.key === 'f' || e.key === 'F') full();
      else if (e.key === 'm' || e.key === 'M') { setMuted((x) => { save('hv.muted', !x); return !x; }); } else if (e.key === 'j' || (e.key === 'ArrowLeft' && e.shiftKey)) seek(el.currentTime - 10);
      else if (e.key === 'l' || (e.key === 'ArrowRight' && e.shiftKey)) seek(el.currentTime + 10);
      else if (e.key === 'ArrowUp') { e.preventDefault(); setVol((x) => { const n = Math.min(1, x + 0.1); save('hv.vol', n); return n; }); } else if (e.key === 'ArrowDown') { e.preventDefault(); setVol((x) => { const n = Math.max(0, x - 0.1); save('hv.vol', n); return n; }); }
      wake();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [mini]);
  const pct = dur ? (time / dur) * 100 : 0;
  return html`<div class=${`vplayer${mini ? ' mini' : ''}${idle && playing ? ' idle' : ''}${fs ? ' fs' : ''}`} ref=${box} onMouseMove=${wake} onMouseLeave=${() => setIdle(true)}>
    <video ref=${vid} src=${src} poster=${poster} autoplay playsinline preload="metadata"
      onClick=${toggle} onDblClick=${mini ? () => onExpand(vid.current ? vid.current.currentTime : 0) : full}
      onPlay=${() => { setPlaying(true); wake(); }} onPause=${() => { setPlaying(false); setIdle(false); }}
      onLoadedMetadata=${(e) => { setDur(e.currentTarget.duration); if (startAt) e.currentTarget.currentTime = startAt; }}
      onTimeUpdate=${(e) => setTime(e.currentTarget.currentTime)}
      onProgress=${(e) => { const b = e.currentTarget.buffered; if (b.length && e.currentTarget.duration) setBuf((b.end(b.length - 1) / e.currentTarget.duration) * 100); }}
      onError=${() => setErr(true)} />
    ${err ? html`<div class="v-err">${t('No se pudo reproducir el video. Probá guardarlo y abrirlo con otro reproductor.')}</div>` : null}
    ${!playing && !err && !mini ? html`<button class="v-bigplay" onClick=${toggle} aria-label=${t('Reproducir')}><${Icon} name="play" size=34 /></button>` : null}
    <div class="v-controls" onClick=${(e) => e.stopPropagation()}>
      ${mini ? null : html`<div class="v-seek" style=${`--p:${pct}%;--b:${buf}%`}>
        <input type="range" min="0" max=${dur || 0} step="0.05" value=${time} onInput=${(e) => seek(Number(e.currentTarget.value))} aria-label=${t('Posición')} /></div>`}
      <div class="v-row">
        <button class="v-btn" onClick=${toggle} title=${playing ? t('Pausa (espacio)') : t('Reproducir (espacio)')}><${Icon} name=${playing ? 'pause' : 'play'} size=18 /></button>
        ${mini ? null : html`<span class="v-time">${fmt(time)} / ${fmt(dur)}</span>`}
        <span class="grow"></span>
        ${mini ? null : html`<div class="v-vol">
          <button class="v-btn" onClick=${() => setMuted((x) => { save('hv.muted', !x); return !x; })} title=${t('Silenciar (M)')}><${Icon} name=${muted || vol === 0 ? 'micOff' : 'volume'} size=18 /></button>
          <input type="range" min="0" max="1" step="0.02" value=${muted ? 0 : vol} style=${`--pct:${(muted ? 0 : vol) * 100}%`}
            onInput=${(e) => { const n = Number(e.currentTarget.value); setVol(n); setMuted(n === 0); save('hv.vol', n); save('hv.muted', n === 0); }} aria-label=${t('Volumen')} /></div>
        <button class="v-btn v-rate" onClick=${() => setRate((r) => ({ 1: 1.25, 1.25: 1.5, 1.5: 2, 2: 0.5, 0.5: 1 }[r] || 1))} title=${t('Velocidad')}>${rate}×</button>
        <button class="v-btn" onClick=${toPip} title=${t('Reproductor flotante')}><${Icon} name="layers" size=18 /></button>
        <button class="v-btn" onClick=${full} title=${fs ? t('Salir de pantalla completa (F)') : t('Pantalla completa (F)')}><${Icon} name=${fs ? 'minimize' : 'maximize'} size=18 /></button>`}
        ${mini ? html`<button class="v-btn" onClick=${() => onExpand(vid.current ? vid.current.currentTime : 0)} title=${t('Abrir en el visor')}><${Icon} name="maximize" size=16 /></button>
          <button class="v-btn" onClick=${onClose} title=${t('Cerrar')}><${Icon} name="close" size=16 /></button>` : null}
      </div>
    </div>
  </div>`;
}

// Reproductor flotante: sigue reproduciendo mientras navegás por los chats; se arrastra.
export function PipLayer() {
  const p = useStore((s) => s.pip);
  const [pos, setPos] = useState(() => load('hv.pipPos', null));
  const drag = useRef(null);
  if (!p) return null;
  const style = pos ? `left:${pos.x}px;top:${pos.y}px;right:auto;bottom:auto` : '';
  const down = (e) => {
    if (drag.current) drag.current.moved = false;
    if (e.target.closest('button,input')) return;
    const r = e.currentTarget.getBoundingClientRect();
    drag.current = { dx: e.clientX - r.left, dy: e.clientY - r.top, w: r.width, h: r.height, x0: e.clientX, y0: e.clientY, moved: false };
    const move = (ev) => {
      const d = drag.current;
      // Con el dedo hay temblor: recién es arrastre si se movió de verdad.
      if (!d.moved && Math.hypot(ev.clientX - d.x0, ev.clientY - d.y0) < (e.pointerType === 'mouse' ? 4 : 12)) return;
      d.moved = true;
      setPos({ x: Math.max(8, Math.min(window.innerWidth - d.w - 8, ev.clientX - d.dx)), y: Math.max(8, Math.min(window.innerHeight - d.h - 8, ev.clientY - d.dy)) });
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      // Con el dedo, al soltar un arrastre no llega ningún clic: la marca se limpia sola para que el
      // próximo toque (por ejemplo en la X) funcione al primer intento.
      setTimeout(() => { if (drag.current) drag.current.moved = false; }, 350);
      setPos((x) => { save('hv.pipPos', x); return x; });
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };
  // Si se arrastró, el clic al soltar no pausa ni reproduce.
  const click = (e) => { if (drag.current && drag.current.moved) { e.stopPropagation(); drag.current.moved = false; } };
  return html`<div class="pip" style=${style} onPointerDown=${down} onClickCapture=${click}>
    <${VideoPlayer} key=${p.item.id} a=${p.a} item=${p.item} mini startAt=${p.time}
      onExpand=${(time) => openViewer(p.a, p.item, time)} onClose=${() => update((s) => { s.pip = null; })} />
  </div>`;
}
