// Sonidos de eventos sintetizados (sin archivos): suaves, cortos y consistentes.
import { state } from './store.js';

let ctx = null;
const loops = new Map();

function ac() {
  if (!ctx) ctx = new (window.AudioContext || window.webkitAudioContext)();
  if (ctx.state === 'suspended') ctx.resume().catch(() => {});
  return ctx;
}

function volume() {
  const ui = (state.settings && state.settings.ui) || {};
  return (ui.soundVolume ?? 70) / 100;
}

function enabled(name) {
  const ui = (state.settings && state.settings.ui) || {};
  const s = ui.sounds || {};
  return s[name] !== false;
}

// note: frecuencia, inicio, duración, tipo de onda, volumen relativo.
function tone(c, out, f, t0, dur, type = 'sine', vol = 1) {
  const o = c.createOscillator();
  const g = c.createGain();
  o.type = type;
  o.frequency.setValueAtTime(f, t0);
  g.gain.setValueAtTime(0, t0);
  g.gain.linearRampToValueAtTime(0.22 * vol, t0 + 0.012);
  g.gain.exponentialRampToValueAtTime(0.0001, t0 + dur);
  o.connect(g).connect(out);
  o.start(t0);
  o.stop(t0 + dur + 0.02);
}

const PATTERNS = {
  message: [[1318.5, 0, 0.16], [1760, 0.09, 0.22]],
  mention: [[1046.5, 0, 0.14], [1318.5, 0.08, 0.14], [1760, 0.16, 0.26]],
  join: [[659.25, 0, 0.14, 'triangle'], [987.77, 0.1, 0.2, 'triangle']],
  leave: [[987.77, 0, 0.14, 'triangle'], [659.25, 0.1, 0.22, 'triangle']],
  mute: [[440, 0, 0.09, 'triangle'], [330, 0.07, 0.12, 'triangle']],
  unmute: [[330, 0, 0.09, 'triangle'], [440, 0.07, 0.12, 'triangle']],
  deafen: [[392, 0, 0.1, 'triangle'], [294, 0.08, 0.1, 'triangle'], [220, 0.16, 0.14, 'triangle']],
  undeafen: [[220, 0, 0.1, 'triangle'], [294, 0.08, 0.1, 'triangle'], [392, 0.16, 0.14, 'triangle']],
  camera_on: [[880, 0, 0.08], [1174.7, 0.06, 0.12]],
  camera_off: [[1174.7, 0, 0.08], [880, 0.06, 0.12]],
  screen_on: [[740, 0, 0.1, 'triangle'], [1108.7, 0.08, 0.1, 'triangle'], [1480, 0.16, 0.16, 'triangle']],
  screen_off: [[1480, 0, 0.1, 'triangle'], [1108.7, 0.08, 0.1, 'triangle'], [740, 0.16, 0.16, 'triangle']],
  connected: [[523.25, 0, 0.12], [659.25, 0.09, 0.12], [783.99, 0.18, 0.2]],
  disconnected: [[783.99, 0, 0.12], [659.25, 0.09, 0.12], [523.25, 0.18, 0.24]],
  ring: [[880, 0, 0.18], [1108.7, 0.2, 0.18], [880, 0.4, 0.18], [1108.7, 0.6, 0.3]],
  ringback: [[440, 0, 0.9, 'sine', 0.5], [480, 0, 0.9, 'sine', 0.5]],
};

export const SOUND_NAMES = Object.keys(PATTERNS);

export function playSound(name, { force = false } = {}) {
  if (!force && !enabled(name)) return;
  const p = PATTERNS[name];
  if (!p) return;
  try {
    const c = ac();
    const out = c.createGain();
    out.gain.value = volume();
    out.connect(c.destination);
    const t0 = c.currentTime + 0.01;
    for (const [f, at, dur, type, vol] of p) tone(c, out, f, t0 + at, dur, type, vol ?? 1);
  } catch { /* audio no disponible */ }
}

// loopSound repite un sonido (timbre de llamada) hasta stopLoop.
export function loopSound(name, every = 2000) {
  stopLoop(name);
  playSound(name);
  loops.set(name, setInterval(() => playSound(name), every));
}

export function stopLoop(name) {
  const h = loops.get(name);
  if (h) { clearInterval(h); loops.delete(name); }
}
