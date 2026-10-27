// Íconos de la app a elección, como los íconos alternativos de Telegram o Discord.
// Todos parten de la "H hilvanada" del logo; también hay color propio e imagen propia.
// Se dibujan acá (SVG → PNG de 512 px) y el núcleo los aplica al lanzador, la bandeja y las
// notificaciones.
import { html, useEffect, useMemo, useRef, useState } from '../vendor/preact-htm.js';
import { useStore, toast } from './store.js';
import { t } from './i18n.js';
import { Icon } from './icons.js';
import { Seg } from './ui.js';
import { rpc } from './rpc.js';
import { ICON_STYLES, iconSVG } from './appicon-styles.js';

export { ICON_STYLES, iconSVG };

const SIZE = 512;

export const svgURL = (svg) => `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;

function loadImage(url) {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error(t('No se pudo leer la imagen')));
    img.src = url;
  });
}

async function canvasPNG(draw) {
  const c = document.createElement('canvas');
  c.width = SIZE;
  c.height = SIZE;
  const ctx = c.getContext('2d');
  ctx.imageSmoothingQuality = 'high';
  await draw(ctx);
  const blob = await new Promise((r) => c.toBlob(r, 'image/png'));
  if (!blob) throw new Error(t('No se pudo preparar el ícono'));
  const bytes = new Uint8Array(await blob.arrayBuffer());
  let bin = '';
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}

// SVG → PNG (base64). Con blob: la imagen es del mismo origen y el lienzo no queda "manchado".
export async function svgToPNG(svg) {
  const url = URL.createObjectURL(new Blob([svg], { type: 'image/svg+xml' }));
  try {
    const img = await loadImage(url);
    return await canvasPNG((ctx) => ctx.drawImage(img, 0, 0, SIZE, SIZE));
  } finally {
    URL.revokeObjectURL(url);
  }
}

function clipShape(ctx, shape) {
  ctx.beginPath();
  if (shape === 'circle') ctx.arc(256, 256, 240, 0, Math.PI * 2);
  else if (shape === 'squircle') ctx.roundRect(16, 16, 480, 480, 120);
  else if (shape === 'square') ctx.roundRect(16, 16, 480, 480, 56);
  else ctx.rect(0, 0, SIZE, SIZE);
  ctx.clip();
}

// Imagen propia: recortada al centro (cuadrada) y con la forma elegida.
export async function imageToPNG(file, shape) {
  const url = URL.createObjectURL(file);
  try {
    const img = await loadImage(url);
    return await canvasPNG((ctx) => {
      clipShape(ctx, shape);
      const s = Math.min(img.naturalWidth, img.naturalHeight);
      const box = shape === 'full' ? [0, 0, SIZE] : [16, 16, 480];
      ctx.drawImage(img, (img.naturalWidth - s) / 2, (img.naturalHeight - s) / 2, s, s, box[0], box[1], box[2], box[2]);
    });
  } finally {
    URL.revokeObjectURL(url);
  }
}

async function apply(style, cfg, png) {
  try {
    await rpc.call('app.icon.set', { style, cfg, png });
    toast(t('Ícono cambiado'));
  } catch (e) {
    toast(e.message, 'err');
  }
}

const DEFAULT_CUSTOM = { bg: ['#6d5dfc', '#9b46f2'], fg: '#ffffff', shape: 'squircle' };

// AppIconPicker: la grilla de estilos de Ajustes → Apariencia.
export function AppIconPicker() {
  const s = useStore((st) => st.settings);
  const info = useStore((st) => st.info);
  const cur = s.app_icon || 'classic';
  const saved = s.app_icon_cfg && typeof s.app_icon_cfg === 'object' ? s.app_icon_cfg : null;
  const [custom, setCustom] = useState(() => (cur === 'custom' && saved && saved.bg ? saved : DEFAULT_CUSTOM));
  const [img, setImg] = useState(null); // { file, url }
  const [imgShape, setImgShape] = useState('squircle');
  const [open, setOpen] = useState(cur === 'custom' ? 'custom' : null);
  const [busy, setBusy] = useState(false);
  const [tray, setTray] = useState(null);
  const fileRef = useRef();
  useEffect(() => { rpc.call('app.tray').then(setTray).catch(() => {}); }, []);
  useEffect(() => () => { if (img) URL.revokeObjectURL(img.url); }, [img]);
  const tiles = useMemo(() => ICON_STYLES.map((st) => ({ ...st, src: svgURL(iconSVG(st)) })), []);
  const customSVG = iconSVG({ ...custom, shine: true, thread: true });

  const choose = async (st) => {
    setOpen(null);
    if (busy) return;
    setBusy(true);
    try {
      if (st.id === 'classic') await apply('classic', null, null);
      else await apply(st.id, null, await svgToPNG(iconSVG(st)));
    } catch (e) {
      toast(e.message, 'err');
    }
    setBusy(false);
  };
  const applyCustom = async () => {
    setBusy(true);
    try { await apply('custom', custom, await svgToPNG(customSVG)); } catch (e) { toast(e.message, 'err'); }
    setBusy(false);
  };
  const pickImage = (e) => {
    const f = e.currentTarget.files && e.currentTarget.files[0];
    e.currentTarget.value = '';
    if (!f) return;
    if (!/^image\//.test(f.type)) { toast(t('Elegí una imagen (PNG, JPG, SVG o WebP)'), 'err'); return; }
    setImg({ file: f, url: URL.createObjectURL(f) });
    setOpen('image');
  };
  const applyImage = async () => {
    if (!img) return;
    setBusy(true);
    try { await apply('image', { shape: imgShape }, await imageToPNG(img.file, imgShape)); } catch (e) { toast(e.message, 'err'); }
    setBusy(false);
  };
  const shapeOpts = [['squircle', t('Redondeado')], ['circle', t('Círculo')], ['square', t('Cuadrado')]];

  return html`<div class="appicon">
    <div class="appicon-grid" role="listbox" aria-label=${t('Ícono de la app')}>
      ${tiles.map((st) => html`<button class=${`appicon-tile${cur === st.id ? ' on' : ''}`} role="option" aria-selected=${cur === st.id} disabled=${busy} onClick=${() => choose(st)}>
        <img src=${st.src} alt="" /><span>${t(st.name)}</span></button>`)}
      <button class=${`appicon-tile${cur === 'custom' ? ' on' : ''}${open === 'custom' ? ' open' : ''}`} onClick=${() => setOpen(open === 'custom' ? null : 'custom')}>
        <img src=${svgURL(customSVG)} alt="" /><span>${t('Tu color')}</span></button>
      <button class=${`appicon-tile${cur === 'image' ? ' on' : ''}${open === 'image' ? ' open' : ''}`} onClick=${() => fileRef.current.click()}>
        ${img ? html`<img class=${`shape-${imgShape}`} src=${img.url} alt="" />` : html`<span class="appicon-add"><${Icon} name="image" size=26 /></span>`}
        <span>${t('Tu imagen')}</span></button>
      <input ref=${fileRef} type="file" accept="image/png,image/jpeg,image/webp,image/svg+xml,image/gif" hidden onChange=${pickImage} />
    </div>
    ${open === 'custom' ? html`<div class="appicon-editor card">
      <img class="appicon-preview" src=${svgURL(customSVG)} alt="" />
      <div class="col grow" style="gap:10px">
        <div class="row wrap" style="gap:14px">
          <label class="appicon-color">${t('Fondo')}<input type="color" value=${custom.bg[0]} onInput=${(e) => setCustom((c) => ({ ...c, bg: [e.currentTarget.value, c.bg[1]] }))} /></label>
          <label class="appicon-color">${t('Degradé')}<input type="color" value=${custom.bg[1]} onInput=${(e) => setCustom((c) => ({ ...c, bg: [c.bg[0], e.currentTarget.value] }))} /></label>
          <label class="appicon-color">${t('Letra')}<input type="color" value=${custom.fg} onInput=${(e) => setCustom((c) => ({ ...c, fg: e.currentTarget.value }))} /></label>
        </div>
        <${Seg} value=${custom.shape} options=${shapeOpts} onChange=${(v) => setCustom((c) => ({ ...c, shape: v }))} />
        <div class="row"><button class="btn primary" disabled=${busy} onClick=${applyCustom}>${t('Usar este ícono')}</button></div>
      </div></div>` : null}
    ${open === 'image' && img ? html`<div class="appicon-editor card">
      <img class=${`appicon-preview shape-${imgShape}`} src=${img.url} alt="" />
      <div class="col grow" style="gap:10px">
        <div class="small muted">${t('Se recorta al centro para que quede cuadrada.')}</div>
        <${Seg} value=${imgShape} options=${[...shapeOpts, ['full', t('Tal cual')]]} onChange=${setImgShape} />
        <div class="row" style="gap:8px"><button class="btn primary" disabled=${busy} onClick=${applyImage}>${t('Usar esta imagen')}</button>
          <button class="btn ghost" onClick=${() => fileRef.current.click()}>${t('Elegir otra')}</button></div>
      </div></div>` : null}
    <div class="hint">${info && info.os === 'android'
      ? t('Los estilos cambian el ícono del teléfono (el lanzador puede tardar unos segundos en actualizarlo). “Tu color” y “Tu imagen” se usan dentro de la app: Android no permite íconos propios en el lanzador.')
      : info && (info.os === 'windows' || info.os === 'darwin')
      ? t('Se usa dentro de la app y en las notificaciones.')
      : tray && !tray.launcher
      ? t('Este perfil es aparte (--data): el ícono se usa en la bandeja y las notificaciones, sin cambiar el lanzador del sistema.')
      : t('Se ve en el dock, el menú de aplicaciones, la bandeja del sistema y las notificaciones.')}</div>
  </div>`;
}
