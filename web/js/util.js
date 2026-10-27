// Utilidades: multimedia, formato, saneamiento de HTML (lista blanca estricta), blurhash.
import { locale, t } from './i18n.js';

export function mxcParts(mxc) {
  const m = /^mxc:\/\/([^/]+)\/([^/?#]+)/.exec(mxc || '');
  return m ? [m[1], m[2]] : null;
}

export function mediaURL(a, mxc, opts = {}) {
  const p = mxcParts(mxc);
  if (!p || !a) return '';
  const q = new URLSearchParams();
  if (opts.w) q.set('w', Math.round(opts.w * (window.devicePixelRatio > 1 ? 2 : 1)));
  if (opts.h) q.set('h', Math.round(opts.h * (window.devicePixelRatio > 1 ? 2 : 1)));
  if (opts.crop) q.set('m', 'crop');
  if (opts.anim) q.set('anim', '1');
  if (opts.dl) q.set('dl', '1');
  const qs = q.toString();
  return `/media/${encodeURIComponent(a)}/${encodeURIComponent(p[0])}/${encodeURIComponent(p[1])}${qs ? `?${qs}` : ''}`;
}

export function encMediaURL(a, room, eventId, thumb = false, dl = false) {
  const q = [];
  if (thumb) q.push('thumb=1');
  if (dl) q.push('dl=1');
  return `/media-enc/${encodeURIComponent(a)}/${encodeURIComponent(room)}/${encodeURIComponent(eventId)}${q.length ? `?${q.join('&')}` : ''}`;
}

const COLORS = ['#e5484d', '#e2701a', '#c58a00', '#30a46c', '#12a594', '#0091ff', '#3e63dd', '#8e4ec6', '#d6409f', '#6f6e77'];
export function colorFor(id = '') {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) | 0;
  return COLORS[Math.abs(h) % COLORS.length];
}

export function initials(name = '') {
  const clean = name.replace(/^[@#!+]/, '').trim();
  const ch = [...clean][0] || '?';
  return ch.toUpperCase();
}

export function localpart(uid = '') {
  return uid.replace(/^@/, '').split(':')[0];
}

export function nameOf(profiles, uid) {
  const p = profiles && profiles[uid];
  return (p && p.name) || localpart(uid) || uid;
}

const dayMs = 86400000;
let hour12 = false;
export function setHour12(v) { hour12 = !!v; }
export function formatTime(ts) {
  return new Date(ts).toLocaleTimeString(locale(), { hour: hour12 ? 'numeric' : '2-digit', minute: '2-digit', hour12 });
}
export function formatDay(ts) {
  const d = new Date(ts);
  const now = new Date();
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  if (ts >= start) return t('Hoy');
  if (ts >= start - dayMs) return t('Ayer');
  const opts = { weekday: 'long', day: 'numeric', month: 'long' };
  if (d.getFullYear() !== now.getFullYear()) opts.year = 'numeric';
  return d.toLocaleDateString(locale(), opts);
}
export function formatShort(ts) {
  if (!ts) return '';
  const now = Date.now();
  const d = new Date(ts);
  const today = new Date(); today.setHours(0, 0, 0, 0);
  if (ts >= today.getTime()) return formatTime(ts);
  if (now - ts < 6 * dayMs) return d.toLocaleDateString(locale(), { weekday: 'short' });
  return d.toLocaleDateString(locale(), { day: 'numeric', month: 'short' });
}
export function sameDay(a, b) {
  const x = new Date(a), y = new Date(b);
  return x.getFullYear() === y.getFullYear() && x.getMonth() === y.getMonth() && x.getDate() === y.getDate();
}
export function formatSize(n) {
  if (!n && n !== 0) return '';
  const u = ['B', 'KB', 'MB', 'GB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(i ? 1 : 0)} ${u[i]}`;
}
export function formatDuration(ms) {
  const s = Math.max(0, Math.round(ms / 1000));
  const m = Math.floor(s / 60);
  return `${m}:${String(s % 60).padStart(2, '0')}`;
}

export function escapeHTML(s = '') {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

// --- enlaces Matrix ---
export function parseMatrixLink(href = '') {
  let m = /^https:\/\/matrix\.to\/#\/([^?]+)(?:\?(.*))?$/.exec(href);
  if (m) {
    const parts = decodeURIComponent(m[1]).split('/');
    const via = new URLSearchParams(m[2] || '').getAll('via');
    const id = parts[0];
    const ev = parts[1];
    if (id.startsWith('@')) return { type: 'user', id };
    if (id.startsWith('#') || id.startsWith('!')) return { type: 'room', id, event: ev, via };
  }
  m = /^matrix:(u|r|roomid)\/([^/?]+)(?:\/e\/([^?]+))?(?:\?(.*))?$/.exec(href);
  if (m) {
    const id = decodeURIComponent(m[2]);
    const via = new URLSearchParams(m[4] || '').getAll('via');
    if (m[1] === 'u') return { type: 'user', id: `@${id}` };
    if (m[1] === 'r') return { type: 'room', id: `#${id}`, via };
    return { type: 'room', id: `!${id}`, event: m[3] ? `$${decodeURIComponent(m[3])}` : undefined, via };
  }
  return null;
}

// --- saneamiento de HTML de mensajes (spec Matrix, lista blanca) ---
const ALLOWED = new Set(['font', 'del', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'blockquote', 'p', 'a', 'ul', 'ol', 'sup', 'sub', 'li',
  'b', 'i', 'u', 'strong', 'em', 's', 'strike', 'code', 'hr', 'br', 'div', 'table', 'thead', 'tbody', 'tr', 'th', 'td', 'caption',
  'pre', 'span', 'img', 'details', 'summary']);
const DROP = new Set(['script', 'style', 'iframe', 'object', 'embed', 'mx-reply', 'svg', 'math', 'form', 'input', 'button', 'textarea', 'select', 'link', 'meta', 'title', 'head', 'video', 'audio', 'canvas', 'template', 'noscript']);
const SAFE_SCHEMES = /^(https?|ftp|mailto|magnet|matrix):/i;
const COLOR = /^#[0-9a-fA-F]{6}$/;

export function sanitizeHTML(html, ctx = {}) {
  const doc = new DOMParser().parseFromString(`<body>${html}</body>`, 'text/html');
  const out = document.createElement('div');
  let depth = 0;
  const noLink = (el) => { for (let n = el; n && n !== out; n = n.parentNode) if (/^(A|CODE|PRE)$/.test(n.tagName)) return true; return false; };
  const appendText = (dst, text) => {
    if (noLink(dst) || !/(https?:\/\/|www\.)/i.test(text)) { dst.appendChild(document.createTextNode(text)); return; }
    let last = 0;
    text.replace(URL_RE, (u, _g, off) => {
      if (off > last) dst.appendChild(document.createTextNode(text.slice(last, off)));
      const a = document.createElement('a');
      a.href = u.startsWith('www.') ? `https://${u}` : u;
      a.rel = 'noopener noreferrer nofollow'; a.target = '_blank'; a.dataset.ext = '1';
      a.textContent = u;
      dst.appendChild(a);
      last = off + u.length;
      return u;
    });
    if (last < text.length) dst.appendChild(document.createTextNode(text.slice(last)));
  };
  const walk = (src, dst) => {
    if (++depth > 100) return;
    for (const node of [...src.childNodes]) {
      if (node.nodeType === 3) { appendText(dst, node.nodeValue); continue; }
      if (node.nodeType !== 1) continue;
      const tag = node.tagName.toLowerCase();
      if (DROP.has(tag)) continue;
      if (!ALLOWED.has(tag)) { walk(node, dst); continue; }
      let el;
      if (tag === 'a') {
        const href = node.getAttribute('href') || '';
        const ml = parseMatrixLink(href);
        if (ml && ml.type === 'user') {
          el = document.createElement('span');
          el.className = 'pill' + (ml.id === ctx.me ? ' me' : '');
          el.dataset.user = ml.id;
          el.textContent = node.textContent || ml.id;
          dst.appendChild(el);
          continue;
        }
        if (ml && ml.type === 'room') {
          el = document.createElement('span');
          el.className = 'pill';
          el.dataset.room = ml.id;
          if (ml.event) el.dataset.event = ml.event;
          if (ml.via && ml.via.length) el.dataset.via = ml.via.join(',');
          el.textContent = node.textContent || ml.id;
          dst.appendChild(el);
          continue;
        }
        el = document.createElement('a');
        if (SAFE_SCHEMES.test(href)) {
          el.setAttribute('href', href);
          el.setAttribute('rel', 'noopener noreferrer nofollow');
          el.setAttribute('target', '_blank');
          el.dataset.ext = '1';
        }
      } else if (tag === 'img') {
        const src = node.getAttribute('src') || '';
        if (!src.startsWith('mxc://')) {
          // Por privacidad no cargamos imágenes externas.
          const alt = node.getAttribute('alt') || node.getAttribute('title');
          if (alt) dst.appendChild(document.createTextNode(alt));
          continue;
        }
        el = document.createElement('img');
        const emote = node.hasAttribute('data-mx-emoticon');
        const h = Math.min(parseInt(node.getAttribute('height') || '32', 10) || 32, 256);
        el.src = mediaURL(ctx.a, src, { w: emote ? 64 : 512, h: emote ? 64 : 512, anim: true });
        el.loading = 'lazy';
        el.decoding = 'async';
        el.alt = node.getAttribute('alt') || '';
        el.title = node.getAttribute('title') || el.alt;
        if (emote) el.className = 'emote';
        else el.style.maxHeight = `${h}px`;
      } else {
        el = document.createElement(tag);
        if (tag === 'font' || tag === 'span') {
          const c = node.getAttribute('data-mx-color') || node.getAttribute('color');
          const bg = node.getAttribute('data-mx-bg-color');
          if (c && COLOR.test(c)) el.style.color = c;
          if (bg && COLOR.test(bg)) el.style.backgroundColor = bg;
          if (node.hasAttribute('data-mx-spoiler')) {
            el.className = 'spoiler';
            el.dataset.spoiler = '1';
          }
        }
        if (tag === 'ol') {
          const st = parseInt(node.getAttribute('start') || '', 10);
          if (st > 0) el.setAttribute('start', String(st));
        }
        if (tag === 'code') {
          const cls = node.getAttribute('class') || '';
          if (/^language-[\w+-]+$/.test(cls)) el.className = cls;
        }
      }
      dst.appendChild(el);
      walk(node, el);
    }
    depth--;
  };
  walk(doc.body, out);
  return out.innerHTML;
}

// linkify convierte texto plano en HTML seguro con enlaces y menciones.
const URL_RE = /\b((?:https?:\/\/|www\.)[^\s<]+[^\s<.,:;"')\]!?])/gi;
const MXID_RE = /(^|[\s(])(@[a-z0-9._=\-/+]+:[a-z0-9.-]+(?::\d+)?)/gi;
export function linkify(text = '', ctx = {}) {
  let html = escapeHTML(text);
  html = html.replace(URL_RE, (u) => {
    const href = u.startsWith('www.') ? `https://${u}` : u;
    const ml = parseMatrixLink(href.replace(/&amp;/g, '&'));
    if (ml && ml.type === 'user') return `<span class="pill" data-user="${escapeHTML(ml.id)}">${escapeHTML(ml.id)}</span>`;
    if (ml && ml.type === 'room') return `<span class="pill" data-room="${escapeHTML(ml.id)}">${escapeHTML(ml.id)}</span>`;
    return `<a href="${href}" target="_blank" rel="noopener noreferrer nofollow" data-ext="1">${u}</a>`;
  });
  html = html.replace(MXID_RE, (_, pre, id) => `${pre}<span class="pill${id === ctx.me ? ' me' : ''}" data-user="${id}">${id}</span>`);
  return html.replace(/\n/g, '<br>');
}

const EMOJI_ONLY = /^(?:\p{Extended_Pictographic}|\p{Emoji_Component}|‍|️|\s)+$/u;
export function isEmojiOnly(text = '') {
  const s = text.trim();
  if (!s || s.length > 32 || /\d/.test(s.replace(/[⃣]/g, ''))) return false;
  return EMOJI_ONLY.test(s);
}

export function debounce(fn, ms) {
  let tm;
  return (...args) => { clearTimeout(tm); tm = setTimeout(() => fn(...args), ms); };
}
export function throttle(fn, ms) {
  let last = 0, tm;
  return (...args) => {
    const now = Date.now();
    clearTimeout(tm);
    if (now - last >= ms) { last = now; fn(...args); } else tm = setTimeout(() => { last = Date.now(); fn(...args); }, ms - (now - last));
  };
}

export async function copyText(text) {
  try { await navigator.clipboard.writeText(text); return true; } catch {
    const ta = document.createElement('textarea');
    ta.value = text; document.body.appendChild(ta); ta.select();
    const ok = document.execCommand('copy'); ta.remove(); return ok;
  }
}

// --- blurhash (decodificador compacto) ---
const B83 = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz#$%*+,-.:;=?@[]^_{|}~';
const d83 = (s) => [...s].reduce((v, c) => v * 83 + B83.indexOf(c), 0);
const s2l = (v) => { const x = v / 255; return x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4; };
const l2s = (v) => { const x = Math.max(0, Math.min(1, v)); return Math.round((x <= 0.0031308 ? x * 12.92 : 1.055 * x ** (1 / 2.4) - 0.055) * 255); };
const sp = (v, e) => Math.sign(v) * Math.abs(v) ** e;
const bhCache = new Map();
export function blurhashURL(hash, w = 32, h = 32) {
  if (!hash || hash.length < 6) return '';
  if (bhCache.has(hash)) return bhCache.get(hash);
  try {
    const sf = d83(hash[0]);
    const nx = (sf % 9) + 1, ny = Math.floor(sf / 9) + 1;
    const maxv = (d83(hash[1]) + 1) / 166;
    const colors = [];
    for (let i = 0; i < nx * ny; i++) {
      if (i === 0) {
        const v = d83(hash.slice(2, 6));
        colors.push([s2l(v >> 16), s2l((v >> 8) & 255), s2l(v & 255)]);
      } else {
        const v = d83(hash.slice(4 + i * 2, 6 + i * 2));
        colors.push([sp((Math.floor(v / 361) - 9) / 9, 2) * maxv, sp(((Math.floor(v / 19) % 19) - 9) / 9, 2) * maxv, sp(((v % 19) - 9) / 9, 2) * maxv]);
      }
    }
    const c = document.createElement('canvas');
    c.width = w; c.height = h;
    const ctx = c.getContext('2d');
    const img = ctx.createImageData(w, h);
    for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) {
      let r = 0, g = 0, b = 0;
      for (let j = 0; j < ny; j++) for (let i = 0; i < nx; i++) {
        const basis = Math.cos((Math.PI * x * i) / w) * Math.cos((Math.PI * y * j) / h);
        const col = colors[i + j * nx];
        r += col[0] * basis; g += col[1] * basis; b += col[2] * basis;
      }
      const o = 4 * (x + y * w);
      img.data[o] = l2s(r); img.data[o + 1] = l2s(g); img.data[o + 2] = l2s(b); img.data[o + 3] = 255;
    }
    ctx.putImageData(img, 0, 0);
    const url = c.toDataURL();
    if (bhCache.size > 500) bhCache.clear();
    bhCache.set(hash, url);
    return url;
  } catch { return ''; }
}

export function fileKind(mime = '') {
  if (mime.startsWith('image/')) return 'image';
  if (mime.startsWith('video/')) return 'video';
  if (mime.startsWith('audio/')) return 'audio';
  return 'file';
}

export function plural(n, one, many) { return n === 1 ? one : many; }

export function userServer(uid = '') { return uid.slice(uid.indexOf(':') + 1); }
