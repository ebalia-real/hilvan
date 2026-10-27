// Llamadas 1:1: timbre, escenario, barra lateral, video nativo y motor WebRTC del navegador.
import { html, useEffect, useLayoutEffect, useRef, useState } from '../vendor/preact-htm.js';
import { useStore, state, update, setView, toast, switchAccount, save, openModal, closeModal } from './store.js';
import { rpc } from './rpc.js';
import { t } from './i18n.js';
import { Icon } from './icons.js';
import { Avatar, confirmDialog, openMenu, closeMenu, Modal, Seg, Switch, Spinner } from './ui.js';
import { useDevices, Range, setUI, setSetting, STREAM_PRESETS } from './settings.js';
import { off } from './policy.js';
import { playSound, loopSound, stopLoop } from './sounds.js';

const ui = () => (state.settings && state.settings.ui) || {};
// Solo la WebView de la ventana nativa ubica el video nativo (otros navegadores conectados no).
const IN_SHELL = navigator.userAgent.includes('Hilvan');
const live = (c) => c && c.state !== 'ended' && c.state !== 'ringing';

export function activeCall(s = state) {
  return Object.values(s.calls || {}).find(live) || null;
}

function setCall(v) {
  update((s) => { s.calls = { ...(s.calls || {}), [v.id]: { ...((s.calls || {})[v.id] || {}), ...v } }; });
}

// ---------------- eventos del núcleo ----------------
const prevState = {};
const REASONS = () => ({
  user_busy: t('Está en otra llamada'), invite_timeout: t('No contestó'), rejected: t('Rechazó la llamada'),
  user_media_failed: t('No se pudo usar el micrófono o la cámara'), ice_failed: t('No se pudo establecer la conexión'),
  answered_elsewhere: t('Contestaste en otro dispositivo'), send_failed: t('No se pudo enviar la llamada'),
});

rpc.on('call', (m) => {
  const v = m.d;
  const before = prevState[v.id];
  prevState[v.id] = v.state;
  setCall(v);
  if (before === v.state) return;
  if (v.state === 'ringing') loopSound('ring', 2600);
  else stopLoop('ring');
  if (v.state === 'inviting') loopSound('ringback', 3000);
  else stopLoop('ringback');
  if (v.state === 'connected') playSound('connected');
  if (v.state === 'ended') {
    if (before === 'connected' || before === 'connecting' || v.outgoing) playSound('disconnected');
    const why = REASONS()[v.reason];
    if (why && v.outgoing) toast(why);
    else if (why && v.reason === 'user_media_failed') toast(why, 'err');
    stopPTT();
    setTimeout(() => update((s) => {
      const c = { ...(s.calls || {}) };
      if (c[v.id] && c[v.id].state === 'ended') delete c[v.id];
      s.calls = c;
    }), 4000);
  }
});

rpc.on('call.speaking', (m) => update((s) => {
  const c = (s.calls || {})[m.d.id];
  if (!c) return;
  s.calls = { ...s.calls, [c.id]: { ...c, speaking: { ...(c.speaking || {}), [m.d.who]: m.d.speaking } } };
}));

rpc.on('$open', async () => {
  try {
    const caps = await rpc.call('call.caps', { webrtc: typeof RTCPeerConnection !== 'undefined' });
    const list = await rpc.call('call.list');
    update((s) => {
      s.callCaps = caps;
      s.calls = {};
      for (const c of list || []) { s.calls[c.id] = c; prevState[c.id] = c.state; }
    });
  } catch { /* núcleo viejo */ }
});

// ---------------- acciones ----------------
export async function startCall(a, room, video) {
  const sum = state.acct[a] && state.acct[a].rooms[room];
  if (!sum) return;
  if (activeCall()) { toast(t('Ya estás en una llamada')); return; }
  if (!sum.dm && (sum.joined || 0) > 2) return groupCall(a, room);
  const v = state.voice || {};
  try {
    const c = await rpc.call('call.place', { a, room, video, muted: !!v.muted, deafened: !!v.deafened });
    prevState[c.id] = c.state;
    setCall(c);
    loopSound('ringback', 3000);
  } catch (e) {
    if (e.message === 'group') return groupCall(a, room);
    toast(e.message, 'err');
  }
}

// Las llamadas grupales nativas (MatrixRTC) llegan después; mientras, una sala de Jitsi compartida.
async function groupCall(a, room) {
  if (off('jitsi')) { toast(t('Las llamadas grupales todavía no están disponibles en tu organización')); return; }
  const ok = await confirmDialog({
    title: t('Llamada grupal'),
    text: t('Las llamadas grupales nativas (MatrixRTC) están en camino. ¿Crear una reunión de Jitsi y compartir el enlace en esta sala?'),
    ok: t('Crear reunión'),
  });
  if (!ok) return;
  const id = Array.from(crypto.getRandomValues(new Uint8Array(9)), (b) => b.toString(36).padStart(2, '0')).join('');
  const url = `https://meet.jit.si/Hilvan-${id}`;
  await rpc.call('room.send', { a, room, text: `📞 ${t('Reunión grupal')}: ${url}` }).catch(() => {});
  rpc.call('app.openExternal', { url });
}

export function joinGroupCall(a, room) { return groupCall(a, room); }

export async function answerCall(c, video) {
  const v = state.voice || {};
  if (c.account !== state.cur) switchAccount(c.account);
  setView({ room: c.room, space: null });
  stopLoop('ring');
  try { await rpc.call('call.answer', { id: c.id, video, muted: !!v.muted, deafened: !!v.deafened }); } catch (e) { toast(e.message, 'err'); }
}

export function rejectCall(c) { stopLoop('ring'); rpc.call('call.reject', { id: c.id }); }
export function hangup(c) { stopLoop('ringback'); rpc.call('call.hangup', { id: c.id }); }

function setVoice(patch) {
  update((s) => { s.voice = { ...(s.voice || {}), ...patch }; });
  save('hv.voice', state.voice);
}

export function toggleMute() {
  const v = state.voice || {};
  if (v.deafened && v.muted) { setVoice({ deafened: false, muted: false }); playSound('undeafen'); return; }
  setVoice({ muted: !v.muted });
  playSound(v.muted ? 'unmute' : 'mute');
}

export function toggleDeafen() {
  const v = state.voice || {};
  if (v.deafened) { setVoice({ deafened: false, muted: !!v.mutedBeforeDeaf }); playSound('undeafen'); }
  else { setVoice({ deafened: true, mutedBeforeDeaf: !!v.muted, muted: true }); playSound('deafen'); }
}

async function toggleCamera(c) {
  playSound(c.video_off ? 'camera_on' : 'camera_off');
  await rpc.call('call.video', { id: c.id, off: !c.video_off }).catch((e) => toast(e.message, 'err'));
}

// ---------------- pulsar para hablar ----------------
let pttDown = false;
let pttTimer = null;
function pttActive() { return ui().inputMode === 'ptt' && activeCall(); }
function pttMatch(e) { return (e.code || e.key) === (ui().pttKey || 'Space'); }
function typing(e) {
  const el = e.target;
  return el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) && (ui().pttKey || 'Space').length > 1 && !/^F\d+$/.test(ui().pttKey || '');
}
function onPTTDown(e) {
  if (!pttActive() || !pttMatch(e) || e.repeat || typing(e)) return;
  const c = activeCall();
  if ((state.voice || {}).muted) return;
  pttDown = true;
  clearTimeout(pttTimer);
  rpc.call('call.mute', { id: c.id, muted: false });
}
function onPTTUp(e) {
  if (!pttDown || !pttMatch(e)) return;
  pttDown = false;
  const c = activeCall();
  clearTimeout(pttTimer);
  pttTimer = setTimeout(() => c && rpc.call('call.mute', { id: c.id, muted: true }), ui().pttDelay ?? 200);
}
function stopPTT() { pttDown = false; clearTimeout(pttTimer); }
window.addEventListener('keydown', onPTTDown, true);
window.addEventListener('keyup', onPTTUp, true);
window.addEventListener('blur', () => { if (pttDown) onPTTUp({ code: ui().pttKey || 'Space', key: ui().pttKey || 'Space' }); });

// Sincroniza el micrófono/auriculares globales (panel de usuario) con la llamada activa.
function useVoiceSync(c) {
  const voice = useStore((s) => s.voice) || {};
  const ptt = useStore((s) => (s.settings.ui || {}).inputMode === 'ptt');
  useEffect(() => {
    if (!c || c.state === 'ended') return;
    const wantMuted = !!voice.muted || (ptt && !pttDown);
    if (wantMuted !== !!c.muted) rpc.call('call.mute', { id: c.id, muted: wantMuted });
    if (!!voice.deafened !== !!c.deafened) rpc.call('call.deafen', { id: c.id, deaf: !!voice.deafened });
  }, [c && c.id, c && c.state, voice.muted, voice.deafened, ptt]);
}

// ---------------- duración ----------------
function useDuration(c) {
  const [, tick] = useState(0);
  useEffect(() => {
    if (!c || !c.connected_at || c.state === 'ended') return undefined;
    const h = setInterval(() => tick((x) => x + 1), 1000);
    return () => clearInterval(h);
  }, [c && c.connected_at, c && c.state]);
  if (!c || !c.connected_at) return '';
  const s = Math.max(0, Math.floor((Date.now() - c.connected_at) / 1000));
  const hh = Math.floor(s / 3600), mm = Math.floor((s % 3600) / 60), ss = s % 60;
  return `${hh ? `${hh}:` : ''}${hh ? String(mm).padStart(2, '0') : mm}:${String(ss).padStart(2, '0')}`;
}

function statusText(c, dur) {
  switch (c.state) {
    case 'inviting': return t('Llamando…');
    case 'connecting': return t('Conectando…');
    case 'connected': return dur;
    case 'ended': return t('Llamada finalizada');
    default: return '';
  }
}

// ---------------- video nativo: informar posiciones al shell ----------------
const OVERLAYS = '.overlay, .popover, .viewer, .dialog, .toast';
function intersects(a, b) { return a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top; }

function useNativeVideo(stageRef, c, enabled) {
  const last = useRef('');
  useEffect(() => {
    if (!enabled) return undefined;
    let raf = 0;
    const report = () => {
      raf = 0;
      const root = stageRef.current;
      const tiles = [];
      const covers = [...document.querySelectorAll(OVERLAYS)].map((el) => el.getBoundingClientRect()).filter((r) => r.width && r.height);
      if (root) {
        for (const el of root.querySelectorAll('[data-stream]')) {
          const r = el.getBoundingClientRect();
          const show = el.dataset.show === '1' && r.width > 4 && r.height > 4 && !document.hidden && !covers.some((o) => intersects(o, r));
          tiles.push({ stream: el.dataset.stream, x: r.left, y: r.top, w: r.width, h: r.height, visible: show, mirror: el.dataset.mirror === '1', contain: el.dataset.contain === '1' });
        }
      }
      const key = JSON.stringify(tiles);
      if (key !== last.current) {
        last.current = key;
        rpc.call('call.layout', { tiles }).catch(() => {});
      }
    };
    const schedule = () => { if (!raf) raf = requestAnimationFrame(report); };
    const ro = new ResizeObserver(schedule);
    if (stageRef.current) ro.observe(stageRef.current);
    window.addEventListener('resize', schedule);
    const mo = new MutationObserver(schedule);
    mo.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['class', 'data-show'] });
    const iv = setInterval(schedule, 700);
    schedule();
    return () => {
      ro.disconnect(); mo.disconnect(); clearInterval(iv);
      window.removeEventListener('resize', schedule);
      if (raf) cancelAnimationFrame(raf);
      const hidden = JSON.parse(last.current || '[]').map((x) => ({ ...x, visible: false }));
      last.current = '';
      if (hidden.length) rpc.call('call.layout', { tiles: hidden }).catch(() => {});
    };
  }, [enabled, c && c.id]);
}

// ---------------- escenario de la llamada (dentro de la sala) ----------------
export function CallStage({ a, room }) {
  const c = useStore((s) => Object.values(s.calls || {}).find((x) => x.room === room && x.account === a && x.state !== 'ringing')) || null;
  const caps = useStore((s) => s.callCaps) || {};
  const full = useStore((s) => s.callFull);
  const busy = useStore((s) => s.callBusy);
  const voice = useStore((s) => s.voice) || {};
  const mirror = useStore((s) => (s.settings.ui || {}).mirrorCamera !== false);
  const showStats = useStore((s) => !!(s.settings.ui || {}).callStats);
  const me = useStore((s) => (s.accounts || []).find((x) => x.user_id === a));
  const ref = useRef();
  const dur = useDuration(c);
  const native = !!(c && c.engine === 'native' && caps.native_video);
  const elsewhere = native && !IN_SHELL;
  useVoiceSync(c);
  useNativeVideo(ref, c, native && IN_SHELL && c && c.state !== 'ended');
  if (!c) return null;
  const streams = c.streams || [];
  const remoteVideo = streams.includes('remote') && c.remote_video;
  const localVideo = c.video && streams.includes('local') && !c.video_off;
  const hasVideo = c.video || remoteVideo;
  const sp = c.speaking || {};
  const ended = c.state === 'ended';
  return html`<section class=${`call-stage${hasVideo ? ' video' : ' voice'}${full ? ' full' : ''}${ended ? ' ended' : ''}`} ref=${ref}>
    <div class="call-tiles">
      <div class=${`call-tile remote${sp.remote && !ended ? ' speaking' : ''}`}
        onContextMenu=${(e) => { e.preventDefault(); openMenu(e, null, { render: () => html`<${UserVolumeMenu} user=${c.peer} name=${c.peer_name || c.peer} />` }); }}>
        <div class="tile-avatar"><${Avatar} a=${a} mxc=${c.peer_avatar} name=${c.peer_name} id=${c.peer} size=${hasVideo ? 96 : 84} /></div>
        ${native ? html`<div class="tile-video" data-stream="remote" data-show=${remoteVideo && !ended ? '1' : '0'} data-contain="1"></div>`
          : html`<${BrowserVideo} call=${c.id} stream="remote" show=${remoteVideo} />`}
        <div class="tile-name">${c.peer_name || c.peer}</div>
        ${elsewhere && remoteVideo ? html`<div class="tile-note">${t('El video se ve en la ventana de Hilvan')}</div>` : null}
      </div>
      ${c.video && !ended ? html`<div class=${`call-tile local pip${sp.local && !voice.muted ? ' speaking' : ''}`}>
        <div class="tile-avatar"><${Avatar} a=${a} mxc=${me && me.avatar} name=${me && me.displayname} id=${a} size=${44} /></div>
        ${native ? html`<div class="tile-video" data-stream="local" data-show=${localVideo ? '1' : '0'} data-mirror=${mirror ? '1' : '0'}></div>`
          : html`<${BrowserVideo} call=${c.id} stream="local" show=${localVideo} mirror=${mirror} />`}
        ${c.video_off ? html`<div class="tile-off"><${Icon} name="videoOff" size=16 /></div>` : null}
      </div>` : null}
    </div>
    <div class="call-info">
      <span class=${`call-dot ${c.state}`}></span>
      <span>${statusText(c, dur)}</span>
      <span class="call-lock" title=${t('Señalización cifrada de extremo a extremo; medios cifrados con DTLS-SRTP')}><${Icon} name="lock" size=13 /></span>
      ${c.sharing ? html`<span class="call-live">${t('EN VIVO')}</span><span class="call-share-info">${shareLabel(c.share)}</span>` : null}
    </div>
    ${showStats && c.state === 'connected' ? html`<${CallStats} id=${c.id} />` : null}
    ${ended ? null : html`<div class="call-controls">
      <div class=${`ctl-group${voice.muted ? ' off' : ''}`}>
        <button class="round" title=${voice.muted ? t('Activar micrófono') : t('Silenciar micrófono')} onClick=${toggleMute}>
          <${Icon} name=${voice.muted ? 'micOff' : 'mic'} /></button>
        <button class="caret" title=${t('Opciones de voz')} onClick=${(e) => openMenu(e.currentTarget, null, { render: () => html`<${VoiceMenu} />`, align: 'start' })}><${Icon} name="chevronDown" size=14 /></button>
      </div>
      ${c.video ? html`<div class=${`ctl-group${c.video_off ? ' off' : ''}`}>
        <button class="round" title=${c.video_off ? t('Encender cámara') : t('Apagar cámara')} onClick=${() => toggleCamera(c)}>
          <${Icon} name=${c.video_off ? 'videoOff' : 'video'} /></button>
        <button class="caret" title=${t('Opciones de video')} onClick=${(e) => openMenu(e.currentTarget, null, { render: () => html`<${CameraMenu} />` })}><${Icon} name="chevronDown" size=14 /></button>
      </div>` : null}
      ${off('screenshare') ? null : html`<div class=${`ctl-group${c.sharing ? ' on' : ''}`}>
        <button class="round" disabled=${busy} title=${c.sharing ? t('Opciones de la transmisión') : t('Transmitir pantalla')}
          onClick=${(e) => (c.sharing ? openMenu(e.currentTarget, null, { render: () => html`<${StreamMenu} id=${c.id} />` }) : openGoLive(c))}>
          ${busy ? html`<${Spinner} sm />` : html`<${Icon} name="screen" />`}</button>
      </div>`}
      <button class=${`round${voice.deafened ? ' off' : ''}`} title=${voice.deafened ? t('Dejar de ensordecer') : t('Ensordecer')} onClick=${toggleDeafen}>
        <${Icon} name=${voice.deafened ? 'headphonesOff' : 'headphones'} /></button>
      <button class="round" title=${t('Ajustes de voz y video')} onClick=${() => openModal('settings', { tab: 'voice' })}><${Icon} name="settings" /></button>
      <button class="round" title=${full ? t('Salir de pantalla completa') : t('Pantalla completa')} onClick=${() => update((s) => { s.callFull = !s.callFull; })}>
        <${Icon} name=${full ? 'minimize' : 'maximize'} /></button>
      <button class="round hang" title=${t('Colgar')} onClick=${() => hangup(c)}><${Icon} name="hangup" /></button>
    </div>`}
  </section>`;
}

function CallStats({ id }) {
  const [st, setSt] = useState(null);
  useEffect(() => {
    let alive = true;
    const poll = () => rpc.call('call.stats', { id }).then((r) => alive && setSt(r)).catch(() => {});
    poll();
    const h = setInterval(poll, 2000);
    return () => { alive = false; clearInterval(h); };
  }, [id]);
  if (!st) return null;
  const parts = [];
  if (st.rtt_ms > 0) parts.push(`${t('Latencia')} ${st.rtt_ms} ms`);
  if (st.jitter_ms) parts.push(`jitter ${st.jitter_ms} ms`);
  if (st.lost >= 0) parts.push(`${t('perdidos')} ${st.lost}`);
  if (st.engine) parts.push(st.engine);
  return html`<div class="call-stats mono">${parts.join(' · ')}</div>`;
}

// ---------------- barra de llamada en la barra lateral ----------------
function usePing(c) {
  const [rtt, setRtt] = useState(null);
  const id = c && c.state === 'connected' ? c.id : null;
  useEffect(() => {
    if (!id) { setRtt(null); return undefined; }
    let alive = true;
    const poll = () => rpc.call('call.stats', { id }).then((r) => alive && setRtt(r && r.rtt_ms > 0 ? r.rtt_ms : null)).catch(() => {});
    poll();
    const h = setInterval(poll, 3000);
    return () => { alive = false; clearInterval(h); };
  }, [id]);
  return rtt;
}

export function CallBar() {
  const c = useStore((s) => activeCall(s));
  const rtt = usePing(c);
  const voice = useStore((s) => s.voice) || {};
  const viewing = useStore((s) => c && s.view.room === c.room && s.cur === c.account);
  const dur = useDuration(c);
  useVoiceSync(viewing ? null : c);
  if (!c) return null;
  const go = () => { if (c.account !== state.cur) switchAccount(c.account); setView({ room: c.room, space: null }); };
  return html`<div class="call-bar">
    <button class="cb-main" onClick=${go} title=${t('Ir a la llamada')}>
      <span class=${`call-dot ${c.state}`}></span>
      <div style="min-width:0"><div class="cb-state">${c.state === 'connected' ? (c.video ? t('Videollamada') : t('Llamada de voz')) : statusText(c, dur)}
        ${rtt ? html`<span class=${`cb-ping${rtt > 250 ? ' bad' : rtt > 120 ? ' meh' : ''}`} title=${t('Latencia')}>${rtt} ms</span>` : null}
        ${c.sharing ? html`<span class="call-live sm" title=${shareLabel(c.share)}>${t('EN VIVO')}</span>` : null}</div>
        <div class="cb-peer">${c.peer_name || c.peer}${c.state === 'connected' ? ` · ${dur}` : ''}</div></div>
    </button>
    ${off('screenshare') ? null : html`<button class=${`icon-btn${c.sharing ? ' live' : ''}`} title=${c.sharing ? t('Opciones de la transmisión') : t('Transmitir pantalla')}
      onClick=${(e) => (c.sharing ? openMenu(e.currentTarget, null, { render: () => html`<${StreamMenu} id=${c.id} />` }) : openGoLive(c))}><${Icon} name="screen" size=18 /></button>`}
    <button class=${`icon-btn${voice.muted ? ' off' : ''}`} title=${voice.muted ? t('Activar micrófono') : t('Silenciar micrófono')} onClick=${toggleMute}><${Icon} name=${voice.muted ? 'micOff' : 'mic'} size=18 /></button>
    <button class="icon-btn hang" title=${t('Colgar')} onClick=${() => hangup(c)}><${Icon} name="hangup" size=18 /></button>
  </div>`;
}

// ---------------- llamada entrante ----------------
export function CallLayer() {
  const ringing = useStore((s) => Object.values(s.calls || {}).filter((c) => c.state === 'ringing'));
  return html`${ringing.map((c) => html`<${Incoming} key=${c.id} c=${c} />`)}<${GoLiveLayer} />`;
}

function Incoming({ c }) {
  useEffect(() => {
    const onKey = (e) => { if (e.key === 'Escape') rejectCall(c); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [c.id]);
  return html`<div class="incoming-call" role="alertdialog" aria-label=${t('Llamada entrante')}>
    <div class="ic-avatar"><${Avatar} a=${c.account} mxc=${c.peer_avatar} name=${c.peer_name} id=${c.peer} size=${56} /></div>
    <div class="ic-text"><b>${c.peer_name || c.peer}</b><span>${c.video ? t('Videollamada entrante…') : t('Llamada de voz entrante…')}</span></div>
    <div class="ic-actions">
      <button class="round hang" title=${t('Rechazar')} onClick=${() => rejectCall(c)}><${Icon} name="hangup" /></button>
      <button class="round ok" title=${c.video ? t('Contestar solo con audio') : t('Contestar')} onClick=${() => answerCall(c, false)}><${Icon} name="phone" /></button>
      ${c.video ? html`<button class="round ok" title=${t('Contestar con video')} onClick=${() => answerCall(c, true)}><${Icon} name="video" /></button>` : null}
    </div>
  </div>`;
}

// ---------------- motor WebRTC del navegador ----------------
// Se usa cuando la interfaz corre en un navegador con WebRTC (modo --headless).
const engines = new Map(); // call id → { pc, local, remote, screen, analysers }
const streamListeners = new Set();
function notifyStreams() { for (const f of streamListeners) f(); }

function BrowserVideo({ call, stream, show, mirror }) {
  const ref = useRef();
  const [, bump] = useState(0);
  useEffect(() => { const f = () => bump((x) => x + 1); streamListeners.add(f); return () => streamListeners.delete(f); }, []);
  const e = engines.get(call);
  const src = e ? (stream === 'local' ? (e.screen || e.local) : e.remote) : null;
  useLayoutEffect(() => { if (ref.current && ref.current.srcObject !== src) ref.current.srcObject = src || null; });
  return html`<video ref=${ref} class=${`tile-video${mirror && !(e && e.screen) ? ' mirror' : ''}`} style=${show && src ? '' : 'visibility:hidden'} autoplay playsinline muted></video>`;
}

function engineEvent(call, kind, data) { rpc.call('call.engine.event', { call, kind, data }).catch(() => {}); }

async function getMedia(p, video) {
  const audio = { echoCancellation: p.ec !== false, noiseSuppression: p.ns !== false, autoGainControl: true };
  return navigator.mediaDevices.getUserMedia({ audio, video: video ? { width: { ideal: 1280 }, height: { ideal: 720 }, frameRate: { ideal: 30 } } : false });
}

function newPC(id, p) {
  const pc = new RTCPeerConnection({ iceServers: p.ice || [], iceTransportPolicy: p.relay ? 'relay' : 'all', bundlePolicy: 'max-bundle' });
  const e = { pc, local: null, remote: new MediaStream(), screen: null, audioEl: new Audio() };
  e.audioEl.autoplay = true;
  e.audioEl.srcObject = e.remote;
  pc.onicecandidate = (ev) => {
    if (ev.candidate) engineEvent(id, 'candidate', { candidate: ev.candidate.candidate, sdpMid: ev.candidate.sdpMid, sdpMLineIndex: ev.candidate.sdpMLineIndex });
  };
  pc.onconnectionstatechange = () => engineEvent(id, 'state', { Kind: 'connection', State: pc.connectionState });
  pc.ontrack = (ev) => {
    e.remote.addTrack(ev.track);
    engineEvent(id, 'stream', { Stream: ev.track.kind === 'video' ? 'remote' : 'remote-audio', Kind: ev.track.kind });
    notifyStreams();
  };
  pc.onnegotiationneeded = () => { if (e.ready) engineEvent(id, 'negotiation', {}); };
  engines.set(id, e);
  return e;
}

function meter(id, e, stream, who) {
  try {
    const ctx = e.actx || (e.actx = new AudioContext());
    const an = ctx.createAnalyser();
    an.fftSize = 1024;
    ctx.createMediaStreamSource(stream).connect(an);
    const buf = new Float32Array(an.fftSize);
    const h = setInterval(() => {
      an.getFloatTimeDomainData(buf);
      let sum = 0;
      for (const x of buf) sum += x * x;
      const db = 20 * Math.log10(Math.sqrt(sum / buf.length) || 1e-9);
      engineEvent(id, 'level', { Who: who, RMS: db });
    }, 200);
    (e.timers || (e.timers = [])).push(h);
  } catch { /* sin WebAudio */ }
}

const ops = {
  async offer(id, p) {
    const e = newPC(id, p);
    e.local = await getMedia(p, p.video);
    for (const tr of e.local.getTracks()) e.pc.addTrack(tr, e.local);
    if (p.muted) e.local.getAudioTracks().forEach((tr) => { tr.enabled = false; });
    meter(id, e, e.local, 'local');
    if (p.video) engineEvent(id, 'stream', { Stream: 'local', Kind: 'video' });
    notifyStreams();
    const o = await e.pc.createOffer();
    await e.pc.setLocalDescription(o);
    e.ready = true;
    return { sdp: e.pc.localDescription.sdp };
  },
  async answer(id, p) {
    const e = newPC(id, p);
    await e.pc.setRemoteDescription({ type: 'offer', sdp: p.sdp });
    e.local = await getMedia(p, p.video);
    for (const tr of e.local.getTracks()) e.pc.addTrack(tr, e.local);
    if (p.muted) e.local.getAudioTracks().forEach((tr) => { tr.enabled = false; });
    meter(id, e, e.local, 'local');
    if (p.video) engineEvent(id, 'stream', { Stream: 'local', Kind: 'video' });
    notifyStreams();
    const ans = await e.pc.createAnswer();
    await e.pc.setLocalDescription(ans);
    e.ready = true;
    setTimeout(() => meter(id, e, e.remote, 'remote'), 500);
    return { sdp: e.pc.localDescription.sdp };
  },
  async setAnswer(id, p) {
    const e = engines.get(id);
    await e.pc.setRemoteDescription({ type: 'answer', sdp: p.sdp });
    setTimeout(() => meter(id, e, e.remote, 'remote'), 500);
    return {};
  },
  async renegotiate(id, p) {
    const e = engines.get(id);
    await e.pc.setRemoteDescription({ type: 'offer', sdp: p.sdp });
    const ans = await e.pc.createAnswer();
    await e.pc.setLocalDescription(ans);
    return { sdp: e.pc.localDescription.sdp };
  },
  async localOffer(id) {
    const e = engines.get(id);
    const o = await e.pc.createOffer();
    await e.pc.setLocalDescription(o);
    return { sdp: e.pc.localDescription.sdp };
  },
  async candidates(id, p) {
    const e = engines.get(id);
    for (const c of p.candidates || []) {
      if (!c.candidate) continue;
      try { await e.pc.addIceCandidate(c); } catch { /* candidato inválido */ }
    }
    return {};
  },
  async mute(id, p) { const e = engines.get(id); e && e.local && e.local.getAudioTracks().forEach((tr) => { tr.enabled = !p.muted; }); return {}; },
  async videoOff(id, p) { const e = engines.get(id); e && e.local && e.local.getVideoTracks().forEach((tr) => { tr.enabled = !p.off; }); return {}; },
  async volume(id, p) { const e = engines.get(id); if (e) e.audioEl.volume = Math.max(0, Math.min(1, p.spk)); return {}; },
  async screen(id, p) {
    const e = engines.get(id);
    const sender = e.pc.getSenders().find((s) => s.track && s.track.kind === 'video');
    if (!sender) throw new Error(t('Para compartir pantalla, iniciá una videollamada'));
    if (p.on) {
      e.screen = await navigator.mediaDevices.getDisplayMedia({ video: { frameRate: 30 }, audio: false });
      const tr = e.screen.getVideoTracks()[0];
      tr.onended = () => rpc.call('call.screen', { id, on: false });
      await sender.replaceTrack(tr);
    } else if (e.screen) {
      e.screen.getTracks().forEach((tr) => tr.stop());
      e.screen = null;
      await sender.replaceTrack(e.local.getVideoTracks()[0] || null);
    }
    notifyStreams();
    return {};
  },
  async stats(id) {
    const e = engines.get(id);
    const out = { engine: 'WebRTC' };
    (await e.pc.getStats()).forEach((r) => {
      if (r.type === 'candidate-pair' && r.nominated && r.currentRoundTripTime !== undefined) out.rtt_ms = Math.round(r.currentRoundTripTime * 1000);
      if (r.type === 'inbound-rtp' && r.kind === 'audio') { out.lost = r.packetsLost; out.jitter_ms = (r.jitter * 1000).toFixed(1); }
    });
    return out;
  },
  close(id) {
    const e = engines.get(id);
    if (!e) return;
    for (const h of e.timers || []) clearInterval(h);
    e.pc.close();
    for (const s of [e.local, e.screen]) s && s.getTracks().forEach((tr) => tr.stop());
    e.audioEl.srcObject = null;
    if (e.actx) e.actx.close();
    engines.delete(id);
    notifyStreams();
  },
};

rpc.on('call.engine', async (m) => {
  const d = m.d || {};
  const fn = ops[d.op];
  if (!fn) return;
  if (d.op === 'close') { fn(d.call); return; }
  try {
    const result = await fn(d.call, d);
    rpc.call('call.engine.reply', { req: d.req, result });
  } catch (e) {
    rpc.call('call.engine.reply', { req: d.req, error: e.message || String(e) });
  }
});

// ---------------- menús de la llamada (como en Discord) ----------------
function shareLabel(o) {
  if (!o) return '';
  const res = o.res === 'source' ? t('Original') : o.res === '2160' ? '4K' : `${o.res}p`;
  return `${res} · ${o.fps} fps${o.audio ? ` · ${t('con audio')}` : ''}`;
}

function DeviceList({ title, items, value, onPick }) {
  return html`<div class="dm-sec"><div class="dm-title">${title}</div>
    <button class=${`menu-item${!value ? ' sel' : ''}`} onClick=${() => onPick('')}><span class="mi-text">${t('Predeterminado del sistema')}</span>${!value ? html`<${Icon} name="check" size=16 />` : null}</button>
    ${items.map((d) => html`<button class=${`menu-item${value === d.id ? ' sel' : ''}`} onClick=${() => onPick(d.id)}>
      <span class="mi-text">${d.label}</span>${value === d.id ? html`<${Icon} name="check" size=16 />` : null}</button>`)}
  </div>`;
}

function VoiceMenu() {
  const s = useStore((st) => st.settings);
  const ui = s.ui || {};
  const [dev] = useDevices();
  const mics = dev.devices.filter((d) => d.kind === 'audioinput');
  const spks = dev.devices.filter((d) => d.kind === 'audiooutput');
  const profile = ui.inputProfile || 'isolation';
  return html`<div class="device-menu">
    ${dev.loading ? html`<div class="dm-sec"><${Spinner} sm /></div>` : html`
      <${DeviceList} title=${t('Dispositivo de entrada')} items=${mics} value=${s.call_mic} onPick=${(id) => setSetting({ call_mic: id })} />
      <${DeviceList} title=${t('Dispositivo de salida')} items=${spks} value=${s.call_speaker} onPick=${(id) => setSetting({ call_speaker: id })} />`}
    <div class="dm-sec">
      <div class="dm-title">${t('Volumen de entrada')}</div><${Range} value=${ui.micVolume ?? 100} onChange=${(v) => setUI({ micVolume: v })} />
      <div class="dm-title">${t('Volumen de salida')}</div><${Range} value=${ui.speakerVolume ?? 100} onChange=${(v) => setUI({ speakerVolume: v })} />
    </div>
    <div class="dm-sec">
      <div class="dm-title">${t('Perfil de entrada')}</div>
      <${Seg} value=${profile} options=${[['isolation', t('Aislamiento de voz')], ['studio', t('Estudio')], ['custom', t('Personalizado')]]} onChange=${(v) => setUI({ inputProfile: v })} />
      ${profile === 'custom' ? html`<label class="dm-row">${t('Supresión de ruido')}<${Switch} checked=${s.call_noise_suppression} onChange=${(v) => setSetting({ call_noise_suppression: v })} /></label>
        <label class="dm-row">${t('Cancelación de eco')}<${Switch} checked=${s.call_echo_cancel} onChange=${(v) => setSetting({ call_echo_cancel: v })} /></label>` : null}
      <div class="small muted" style="margin-top:6px">${t('El perfil se aplica en la próxima llamada.')}</div>
    </div>
    <button class="menu-item" onClick=${() => { closeMenu(); openModal('settings', { tab: 'voice' }); }}><${Icon} name="settings" size=18 /><span class="mi-text">${t('Ajustes de voz')}</span></button>
  </div>`;
}

function CameraMenu() {
  const s = useStore((st) => st.settings);
  const ui = s.ui || {};
  const [dev] = useDevices();
  const cams = dev.devices.filter((d) => d.kind === 'videoinput');
  return html`<div class="device-menu">
    ${dev.loading ? html`<div class="dm-sec"><${Spinner} sm /></div>` : html`<${DeviceList} title=${t('Cámara')} items=${cams} value=${s.call_camera} onPick=${(id) => setSetting({ call_camera: id })} />`}
    <div class="dm-sec">
      <div class="dm-title">${t('Calidad de la cámara')}</div>
      <${Seg} value=${ui.videoQuality || 'auto'} options=${[['auto', t('Auto')], ['480', '480p'], ['720', '720p'], ['1080', '1080p']]} onChange=${(v) => setUI({ videoQuality: v })} />
      <label class="dm-row">${t('Espejar mi video')}<${Switch} checked=${ui.mirrorCamera !== false} onChange=${(v) => setUI({ mirrorCamera: v })} /></label>
    </div>
    <button class="menu-item" onClick=${() => { closeMenu(); openModal('settings', { tab: 'voice' }); }}><${Icon} name="settings" size=18 /><span class="mi-text">${t('Ajustes de video')}</span></button>
  </div>`;
}

function UserVolumeMenu({ user, name }) {
  const ui = useStore((st) => st.settings.ui || {});
  const vols = ui.userVolumes || {};
  const v = vols[user] ?? 100;
  const set = (n) => setUI({ userVolumes: { ...vols, [user]: n } });
  return html`<div class="device-menu">
    <div class="dm-sec"><div class="dm-title">${t('Volumen de {u}', { u: name })}</div>
      <${Range} value=${v} min=${0} max=${200} onChange=${set} />
      <label class="dm-row">${t('Silenciar para mí')}<${Switch} checked=${v === 0} onChange=${(m) => set(m ? 0 : 100)} /></label>
    </div>
    ${v !== 100 ? html`<button class="menu-item" onClick=${() => set(100)}><${Icon} name="refresh" size=18 /><span class="mi-text">${t('Restablecer volumen')}</span></button>` : null}
  </div>`;
}

const RES_OPTS = () => [['720', '720p'], ['1080', '1080p'], ['1440', '1440p'], ['2160', '4K'], ['source', t('Original')]];
const FPS_OPTS = [[15, '15'], [30, '30'], [60, '60']];

function shareDefaults() {
  const ui = (state.settings && state.settings.ui) || {};
  const preset = ui.streamPreset || 'balanced';
  const base = preset === 'custom' || !STREAM_PRESETS[preset] ? { res: ui.shareRes || '1080', fps: ui.shareFps || 30, bitrate: ui.shareBitrate || 8 } : STREAM_PRESETS[preset];
  return { preset, res: base.res, fps: base.fps, mbps: base.bitrate, audio: ui.shareAudio ? 'system' : '', audio_volume: (ui.shareAudioVolume ?? 100) / 100, hint: ui.shareHint || 'auto' };
}

function openGoLive(c) { update((s) => { s.goLive = c.id; }); }

export function GoLiveLayer() {
  const id = useStore((s) => s.goLive);
  const c = useStore((s) => (s.goLive ? (s.calls || {})[s.goLive] : null));
  if (!id || !c || c.state === 'ended') return null;
  return html`<${GoLiveModal} c=${c} onClose=${() => update((s) => { s.goLive = null; })} />`;
}

function SourceCard({ on, thumb, icon, title, sub, onPick }) {
  return html`<button class=${`src-card${on ? ' on' : ''}`} onClick=${onPick}>
    <div class="src-thumb">${thumb ? html`<img src=${thumb} alt="" />` : html`<${Icon} name=${icon || 'screen'} size=34 />`}</div>
    <div class="src-name">${title}</div>${sub ? html`<div class="src-sub">${sub}</div>` : null}
  </button>`;
}

function GoLiveModal({ c, onClose }) {
  const [o, setO] = useState(() => ({ ...shareDefaults(), source: c.share ? c.share.source : '' }));
  const [apps, setApps] = useState(null);
  const [scr, setScr] = useState(null);
  const [tab, setTab] = useState('screens');
  const [busy, setBusy] = useState(false);
  const loadApps = () => rpc.call('media.audioApps').then((a) => setApps(a || [])).catch(() => setApps([]));
  useEffect(() => { loadApps(); }, []);
  // Pantallas con vista previa (se refresca mientras el panel está abierto, como en Discord).
  useEffect(() => {
    let alive = true;
    const load = (thumbs) => rpc.call('media.screens', { thumbs }).then((r) => {
      if (!alive) return;
      setScr((prev) => {
        if (!prev || !thumbs) return r;
        return r; // miniaturas nuevas
      });
      setO((x) => (x.source || !(r.monitors || []).length ? x : { ...x, source: r.monitors[0].id }));
    }).catch(() => alive && setScr({ direct: false, monitors: [] }));
    load(false).then(() => load(true));
    const h = setInterval(() => load(true), 4000);
    return () => { alive = false; clearInterval(h); };
  }, []);
  const set = (patch) => setO((x) => ({ ...x, ...patch }));
  const pickPreset = (k) => {
    const p = STREAM_PRESETS[k];
    set(p ? { preset: k, res: p.res, fps: p.fps, mbps: p.bitrate } : { preset: 'custom' });
  };
  const custom = (patch) => set({ preset: 'custom', ...patch });
  const go = async () => {
    if (!o.source) return;
    setBusy(true);
    setUI({ streamPreset: o.preset, shareRes: o.res, shareFps: o.fps, shareBitrate: o.mbps, shareAudio: !!o.audio,
      shareAudioVolume: Math.round(o.audio_volume * 100), shareHint: o.hint });
    try {
      await rpc.call('call.screen', { id: c.id, on: true, opts: { source: o.source, res: o.res, fps: o.fps, mbps: o.mbps, audio: o.audio, audio_volume: o.audio_volume, hint: o.hint } });
      playSound('screen_on');
      onClose();
    } catch (e) {
      if (!/cancelado/.test(e.message)) toast(e.message, 'err');
      setBusy(false);
    }
  };
  const mons = (scr && scr.monitors) || [];
  const presets = [['smooth', t('Fluidez'), '720p · 60 fps'], ['balanced', t('Equilibrada'), '1080p · 30 fps'], ['text', t('Nitidez'), t('Original · 15 fps')], ['ultra', t('Máxima'), '1440p · 60 fps']];
  return html`<${Modal} title=${c.sharing ? t('Cambiar transmisión') : t('Transmitir pantalla')} cls="golive" onClose=${onClose}
    foot=${html`<button class="btn" onClick=${onClose}>${t('Cancelar')}</button>
      <button class="btn primary" disabled=${busy || !o.source} onClick=${go}>${busy ? html`<${Spinner} sm />` : html`<${Icon} name="screen" size=16 />`} ${c.sharing ? t('Cambiar') : t('Transmitir')}</button>`}>
    <div class="modal-body col">
      <div class="seg src-tabs">
        <button class=${tab === 'screens' ? 'on' : ''} onClick=${() => setTab('screens')}>${t('Pantallas')}</button>
        <button class=${tab === 'windows' ? 'on' : ''} onClick=${() => setTab('windows')}>${t('Ventanas')}</button>
      </div>
      ${tab === 'screens' ? html`<div class="src-grid">
        ${!scr ? html`<div class="src-loading"><${Spinner} /></div>` : null}
        ${mons.map((m, i) => html`<${SourceCard} key=${m.id} on=${o.source === m.id} thumb=${m.thumb}
          title=${mons.length > 1 ? `${t('Pantalla {n}', { n: i + 1 })} · ${m.name}` : m.name}
          sub=${`${m.width}×${m.height} · ${Math.round(m.refresh)} Hz${m.primary ? ` · ${t('principal')}` : ''}`} onPick=${() => set({ source: m.id })} />`)}
        ${scr && !scr.direct ? html`<${SourceCard} on=${o.source === 'screen'} icon="screen" title=${t('Elegir pantalla')} sub=${t('Con el selector del sistema')} onPick=${() => set({ source: 'screen' })} />` : null}
      </div>` : html`<div class="src-grid">
        <${SourceCard} on=${o.source === 'window'} icon="layers" title=${t('Elegir una ventana…')} sub=${t('Se abre el selector del sistema con todas tus ventanas')} onPick=${() => set({ source: 'window' })} />
        ${scr && scr.last_window ? html`<${SourceCard} on=${o.source === 'window:last'} icon="refresh" title=${t('La misma ventana que la última vez')} sub=${t('Sin volver a preguntar, si sigue abierta')} onPick=${() => set({ source: 'window:last' })} />` : null}
      </div>
      <div class="small muted">${t('En Wayland, por privacidad, ninguna aplicación puede ver la lista de tus ventanas: la elegís en el selector del sistema y Hilvan la recuerda para la próxima.')}</div>`}

      <div class="dm-title">${t('Calidad')}</div>
      <div class="preset-grid compact">
        ${presets.map(([k, l, spec]) => html`<button class=${`preset-card${o.preset === k ? ' on' : ''}`} onClick=${() => pickPreset(k)}><b>${l}</b><span class="mono small">${spec}</span></button>`)}
      </div>
      <div class="golive-grid">
        <div class="dm-title">${t('Resolución')}</div><${Seg} value=${o.res} options=${RES_OPTS()} onChange=${(v) => custom({ res: v })} />
        <div class="dm-title">${t('Cuadros por segundo')}</div><${Seg} value=${o.fps} options=${FPS_OPTS} onChange=${(v) => custom({ fps: v })} />
        <div class="dm-title">${t('Bitrate máximo')}</div><${Range} value=${o.mbps} min=${1} max=${40} suffix=" Mbps" onChange=${(v) => custom({ mbps: v })} />
        <div class="dm-title">${t('Optimizar para')}</div><${Seg} value=${o.hint} options=${[['auto', t('Auto')], ['motion', t('Movimiento')], ['detail', t('Detalle')]]} onChange=${(v) => set({ hint: v })} />
      </div>
      <div class="dm-title">${t('Sonido')}</div>
      <div class="radio-list">
        <label class=${`radio-item${!o.audio ? ' on' : ''}`}><input type="radio" name="gla" checked=${!o.audio} onChange=${() => set({ audio: '' })} />
          <div><b>${t('Sin audio')}</b><div class="small muted">${t('Solo la imagen')}</div></div></label>
        <label class=${`radio-item${o.audio === 'system' ? ' on' : ''}`}><input type="radio" name="gla" checked=${o.audio === 'system'} onChange=${() => set({ audio: 'system' })} />
          <div><b>${t('Todo el sonido del equipo')}</b><div class="small muted">${t('Sin la voz de la llamada (se filtra para que no haya eco)')}</div></div></label>
        ${(apps || []).map((ap) => html`<label class=${`radio-item${o.audio === ap.id ? ' on' : ''}`}><input type="radio" name="gla" checked=${o.audio === ap.id} onChange=${() => set({ audio: ap.id })} />
          <div><b>${t('Solo {app}', { app: ap.name })}</b><div class="small muted">${ap.media || t('Aplicación')}</div></div></label>`)}
      </div>
      <div class="row small"><button class="link-btn" onClick=${loadApps}><${Icon} name="refresh" size=14 /> ${t('Actualizar aplicaciones')}</button>
        ${apps && !apps.length ? html`<span class="muted">${t('Ninguna aplicación está reproduciendo sonido ahora.')}</span>` : null}</div>
      ${o.audio ? html`<div class="dm-title">${t('Volumen del audio que transmitís')}</div><${Range} value=${Math.round(o.audio_volume * 100)} onChange=${(v) => set({ audio_volume: v / 100 })} />` : null}
    </div>
  <//>`;
}

function StreamMenu({ id }) {
  const c = useStore((s) => (s.calls || {})[id]);
  if (!c || !c.share) return null;
  const o = c.share;
  const change = (patch) => rpc.call('call.screen', { id, on: true, opts: { ...o, ...patch } }).catch((e) => toast(e.message, 'err'));
  return html`<div class="device-menu">
    <div class="dm-sec"><div class="dm-title">${t('Transmitiendo')} · ${shareLabel(o)}</div>
      <div class="dm-title">${t('Resolución')}</div><${Seg} value=${o.res} options=${RES_OPTS()} onChange=${(v) => change({ res: v })} />
      <div class="dm-title">${t('Cuadros por segundo')}</div><${Seg} value=${o.fps} options=${FPS_OPTS} onChange=${(v) => change({ fps: v })} />
      <label class="dm-row">${t('Compartir sonido del equipo')}<${Switch} checked=${!!o.audio} onChange=${(v) => change({ audio: v ? 'system' : '' })} /></label>
      ${o.audio ? html`<div class="dm-title">${t('Volumen del audio')}</div><${Range} value=${Math.round((o.audio_volume || 1) * 100)} onChange=${(v) => change({ audio_volume: v / 100 })} />` : null}
    </div>
    <button class="menu-item" onClick=${() => { closeMenu(); openGoLive(c); }}><${Icon} name="screen" size=18 /><span class="mi-text">${t('Cambiar pantalla o ventana')}</span></button>
    <button class="menu-item danger" onClick=${() => { closeMenu(); playSound('screen_off'); rpc.call('call.screen', { id, on: false }); }}><${Icon} name="close" size=18 /><span class="mi-text">${t('Dejar de transmitir')}</span></button>
  </div>`;
}

rpc.on('call.notice', (m) => toast(m.d.text, 'err'));
