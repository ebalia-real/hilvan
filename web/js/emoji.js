// Selector de emojis y stickers (incluye paquetes personalizados MSC2545).
import { html, useState, useEffect, useRef, useMemo } from '../vendor/preact-htm.js';
import { EMOJI_GROUPS } from './emoji-data.js';
import { t } from './i18n.js';
import { Icon } from './icons.js';
import { openMenu, closeMenu } from './ui.js';
import { rpc } from './rpc.js';
import { state } from './store.js';

const ALL = EMOJI_GROUPS.flatMap(([, list]) => list);
const GROUP_ICON = { smileys: '😀', people: '👋', nature: '🐻', food: '🍔', travel: '✈️', activities: '⚽', objects: '💡', symbols: '❤️', flags: '🏳️' };
const GROUP_NAME = { smileys: 'Caras', people: 'Personas', nature: 'Naturaleza', food: 'Comida', travel: 'Viajes', activities: 'Actividades', objects: 'Objetos', symbols: 'Símbolos', flags: 'Banderas' };

export function searchEmoji(q, limit = 40) {
  q = q.toLowerCase().replace(/:/g, '');
  if (!q) return [];
  const starts = [], contains = [];
  for (const e of ALL) {
    const names = [e[1], ...(e[2] || [])];
    if (names.some((n) => n.startsWith(q))) starts.push(e);
    else if (names.some((n) => n.includes(q))) contains.push(e);
    if (starts.length >= limit) break;
  }
  return [...starts, ...contains].slice(0, limit);
}

function recents() {
  try { return JSON.parse(localStorage.getItem('hv.recentEmoji') || '[]'); } catch { return []; }
}
function pushRecent(k) {
  const r = [k, ...recents().filter((x) => x !== k)].slice(0, 32);
  try { localStorage.setItem('hv.recentEmoji', JSON.stringify(r)); } catch { /* sin almacenamiento */ }
}

// --- paquetes personalizados (caché por cuenta+sala) ---
const packCache = new Map();
export async function loadPacks(a, room) {
  const key = `${a}|${room || ''}`;
  try {
    const packs = await rpc.call('emotes.list', { a, room: room || '' });
    packCache.set(key, packs || []);
    return packs || [];
  } catch { return packCache.get(key) || []; }
}
export function invalidatePacks() { packCache.clear(); }

export function emotesFor(a, room) {
  const packs = packCache.get(`${a}|${room || ''}`);
  if (!packs) { loadPacks(a, room); return []; }
  const out = [];
  for (const p of packs) {
    for (const [code, img] of Object.entries(p.images || {})) {
      const usage = (img && img.usage) || p.usage || [];
      if (usage.length && !usage.includes('emoticon')) continue;
      if (img && img.url) out.push({ code, url: img.url, pack: p.name });
    }
  }
  return out;
}

function stickersFor(packs) {
  const out = [];
  for (const p of packs) {
    for (const [code, img] of Object.entries(p.images || {})) {
      const usage = (img && img.usage) || p.usage || [];
      if (usage.length && !usage.includes('sticker')) continue;
      if (img && img.url) out.push({ code, url: img.url, body: img.body || code, info: img.info, pack: p.name || t('Stickers') });
    }
  }
  return out;
}

export function showEmojiPicker(anchor, opts) {
  openMenu(anchor, null, { align: 'end', render: () => html`<${Picker} ...${opts} />` });
}

function Picker({ a, room, onPick, onSticker, stickers: startStickers, emoteAs = 'code' }) {
  const [q, setQ] = useState('');
  const [mode, setMode] = useState(startStickers ? 'stickers' : 'emoji');
  const [packs, setPacks] = useState(() => packCache.get(`${a}|${room || ''}`) || []);
  const gridRef = useRef();
  const inputRef = useRef();
  useEffect(() => { loadPacks(a, room).then(setPacks); setTimeout(() => inputRef.current && inputRef.current.focus(), 30); }, []);
  const custom = useMemo(() => {
    const out = [];
    for (const p of packs) {
      const imgs = Object.entries(p.images || {}).filter(([, img]) => { const u = (img && img.usage) || p.usage || []; return !u.length || u.includes('emoticon'); });
      if (imgs.length) out.push({ id: p.id, name: p.name || t('Emojis personalizados'), imgs });
    }
    return out;
  }, [packs]);
  const stickerList = useMemo(() => stickersFor(packs), [packs]);
  const pick = (k) => { pushRecent(k); closeMenu(); onPick && onPick(k); };
  const pickEmote = (code, url) => { closeMenu(); onPick && onPick(emoteAs === 'url' ? url : `:${code}:`); };
  const results = q ? searchEmoji(q, 80) : null;
  const customResults = q ? custom.flatMap((p) => p.imgs.filter(([c]) => c.toLowerCase().includes(q.toLowerCase()))).slice(0, 24) : null;
  const scrollTo = (id) => { const el = gridRef.current && gridRef.current.querySelector(`[data-g="${id}"]`); if (el) el.scrollIntoView({ block: 'start' }); };
  const rec = recents();
  return html`<div class="emoji-picker">
    <div class="ep-search row">
      <input ref=${inputRef} class="input" style="height:34px" placeholder=${t('Buscar emoji')} value=${q} onInput=${(e) => setQ(e.currentTarget.value)} />
      ${onSticker ? html`<div class="seg"><button class=${mode === 'emoji' ? 'on' : ''} onClick=${() => setMode('emoji')}>😀</button>
        <button class=${mode === 'stickers' ? 'on' : ''} onClick=${() => setMode('stickers')}><${Icon} name="sticker" size=16 /></button></div>` : null}
    </div>
    ${mode === 'emoji' && !q ? html`<div class="ep-tabs">
      ${rec.length ? html`<button onClick=${() => scrollTo('rec')} title=${t('Recientes')}><${Icon} name="clock" size=17 /></button>` : null}
      ${custom.length ? html`<button onClick=${() => scrollTo('custom')} title=${t('Personalizados')}><${Icon} name="sparkles" size=17 /></button>` : null}
      ${EMOJI_GROUPS.map(([g]) => html`<button onClick=${() => scrollTo(g)} title=${t(GROUP_NAME[g])}>${GROUP_ICON[g]}</button>`)}
    </div>` : null}
    <div class="ep-grid" ref=${gridRef}>
      ${mode === 'stickers' ? (stickerList.length ? html`<div class="ep-cells stickers">${stickerList.filter((s) => !q || s.code.includes(q)).map((s) => html`
          <button title=${s.code} onClick=${() => { closeMenu(); onSticker(s); }}><img loading="lazy" src=${`/media/${encodeURIComponent(a)}/${s.url.slice(6)}?w=140&h=140&anim=1`} alt=${s.code} /></button>`)}</div>`
          : html`<div class="empty">${t('No tenés stickers todavía. Podés crear un paquete en Ajustes → Emojis y stickers.')}</div>`)
      : results ? html`<div class="ep-cells">
          ${customResults.map(([code, img]) => html`<button title=${`:${code}:`} onClick=${() => pickEmote(code, img.url)}><img loading="lazy" src=${`/media/${encodeURIComponent(a)}/${img.url.slice(6)}?w=56&h=56&anim=1`} alt=${code} /></button>`)}
          ${results.map((e) => html`<button title=${`:${e[1]}:`} onClick=${() => pick(e[0])}>${e[0]}</button>`)}
        </div>${!results.length && !customResults.length ? html`<div class="empty">${t('Sin resultados')}</div>` : null}`
      : html`
        ${rec.length ? html`<div data-g="rec"><div class="ep-group-title">${t('Recientes')}</div><div class="ep-cells">${rec.map((k) => html`<button onClick=${() => pick(k)}>${k}</button>`)}</div></div>` : null}
        ${custom.length ? html`<div data-g="custom">${custom.map((p) => html`<div class="ep-group-title">${p.name}</div><div class="ep-cells">
          ${p.imgs.map(([code, img]) => html`<button title=${`:${code}:`} onClick=${() => pickEmote(code, img.url)}><img loading="lazy" src=${`/media/${encodeURIComponent(a)}/${img.url.slice(6)}?w=56&h=56&anim=1`} alt=${code} /></button>`)}</div>`)}</div>` : null}
        ${EMOJI_GROUPS.map(([g, list]) => html`<div data-g=${g}><div class="ep-group-title">${t(GROUP_NAME[g])}</div>
          <div class="ep-cells">${list.map((e) => html`<button title=${`:${e[1]}:`} onClick=${() => pick(e[0])}>${e[0]}</button>`)}</div></div>`)}
      `}
    </div>
  </div>`;
}

export function refreshPacksFor(a) {
  for (const k of [...packCache.keys()]) if (k.startsWith(`${a}|`)) packCache.delete(k);
}

export function currentEmotes() { return state.cur ? emotesFor(state.cur, state.view.room) : []; }
