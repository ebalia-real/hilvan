// Estilos del ícono de Hilvan (sin dependencias): los usa la interfaz y, al compilar la app de
// Android, el generador de íconos del lanzador.
const SHAPES = {
  full: (a) => `<rect x="0" y="0" width="512" height="512" ${a}/>`,
  squircle: (a) => `<rect x="16" y="16" width="480" height="480" rx="120" ${a}/>`,
  circle: (a) => `<circle cx="256" cy="256" r="240" ${a}/>`,
  square: (a) => `<rect x="16" y="16" width="480" height="480" rx="56" ${a}/>`,
};

function grad(id, colors, vertical) {
  const stops = colors.map((c, i) => `<stop offset="${colors.length === 1 ? 0 : i / (colors.length - 1)}" stop-color="${c}"/>`).join('');
  return `<linearGradient id="${id}" x1="0" y1="0" x2="${vertical ? 0 : 1}" y2="1">${stops}</linearGradient>`;
}

// La H: dos barras y el travesaño "hilvanado" en dos puntadas.
const H_RECTS = [[150, 122, 62, 268, 31], [300, 122, 62, 268, 31], [220, 242, 33, 28, 14], [259, 242, 33, 28, 14]];
const hFill = (fill) => H_RECTS.map(([x, y, w, h, r]) => `<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="${r}" fill="${fill}"/>`).join('');
const thread = (stroke) => `<path d="M331 124c0-42 52-54 74-24" fill="none" stroke="${stroke}" stroke-width="14" stroke-linecap="round" opacity=".9"/>`;

// Neón: la H en contorno, con brillo.
function neonH(stroke) {
  return `<filter id="glow" x="-30%" y="-30%" width="160%" height="160%"><feGaussianBlur stdDeviation="9" result="b"/>
    <feMerge><feMergeNode in="b"/><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge></filter>
    <g filter="url(#glow)" fill="none" stroke="${stroke}" stroke-width="16" stroke-linejoin="round">
    <rect x="152" y="124" width="58" height="264" rx="29"/><rect x="302" y="124" width="58" height="264" rx="29"/>
    <path d="M224 256h64" stroke-linecap="round" stroke-dasharray="24 16"/></g>`;
}

// Retro: la H en píxeles, con sombra.
function retroH(fg, shadow) {
  const cells = [];
  for (let r = 2; r <= 9; r++) cells.push([2, r], [3, r], [8, r], [9, r]);
  cells.push([4, 5], [5, 5], [6, 5], [7, 5], [4, 6], [5, 6], [6, 6], [7, 6]);
  const px = (c, off, fill) => cells.map(([x, y]) => `<rect x="${64 + x * 32 + off}" y="${64 + y * 32 + off}" width="32" height="32" fill="${fill}"/>`).join('');
  return px(cells, 12, shadow) + px(cells, 0, fg);
}

// Hilván: tela con la H cosida a puntadas y la aguja.
function stitchH() {
  return `<pattern id="tela" width="16" height="16" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
      <rect width="16" height="16" fill="#f3eadb"/><path d="M0 8h16" stroke="#e7dac4" stroke-width="3"/></pattern>
    <g fill="none" stroke="#b4372c" stroke-width="28" stroke-linecap="round" stroke-dasharray="40 28">
      <path d="M181 140v232"/><path d="M331 140v232"/><path d="M200 256h112" stroke-dasharray="36 22"/></g>
    <path d="M331 140c4-46 40-74 74-84" fill="none" stroke="#b4372c" stroke-width="8" stroke-linecap="round"/>
    <path d="M372 112l70-76" stroke="#8a95a1" stroke-width="12" stroke-linecap="round"/>
    <ellipse cx="434" cy="45" rx="4" ry="9" transform="rotate(43 434 45)" fill="#f3eadb"/>`;
}

export const ICON_STYLES = [
  { id: 'classic', name: 'Clásico', bg: ['#6D5DFC', '#9B46F2'], fg: '#fff', shine: true, thread: true },
  { id: 'round', name: 'Redondo', shape: 'circle', bg: ['#6D5DFC', '#9B46F2'], fg: '#fff', shine: true },
  { id: 'midnight', name: 'Medianoche', bg: ['#1b1c25', '#0c0d12'], fg: ['#9d8cff', '#d08bff'], thread: true, threadColor: '#b9a8ff' },
  { id: 'light', name: 'Claro', bg: ['#ffffff', '#eceef6'], fg: ['#6D5DFC', '#9B46F2'], border: '#dfe2ec', thread: true, threadColor: '#8f63f6' },
  { id: 'ocean', name: 'Océano', bg: ['#22c1ee', '#1d4ed8'], fg: '#fff', shine: true, thread: true },
  { id: 'forest', name: 'Bosque', bg: ['#34d399', '#047857'], fg: '#fff', shine: true, thread: true },
  { id: 'sunset', name: 'Atardecer', bg: ['#fbbf24', '#f43f5e', '#be185d'], fg: '#fff', shine: true, thread: true },
  { id: 'sakura', name: 'Sakura', bg: ['#fbcfe8', '#c4b5fd'], fg: '#5b3d8f', thread: true, threadColor: '#7c5bb5' },
  { id: 'gold', name: 'Oro', bg: ['#26211b', '#0f0d0a'], fg: ['#fde68a', '#d97706'], border: '#5c4a24', thread: true, threadColor: '#f5c451' },
  { id: 'neon', name: 'Neón', bg: ['#0b0b16', '#120a1f'], kind: 'neon', neon: ['#22d3ee', '#e879f9'] },
  { id: 'retro', name: 'Retro', shape: 'square', bg: ['#2b2145', '#1a1430'], kind: 'retro', fg: '#ffd166', shadow: '#ef476f' },
  { id: 'stitch', name: 'Hilván', kind: 'stitch' },
  { id: 'rainbow', name: 'Arcoíris', bg: ['#ff5f6d', '#ffc371', '#47e891', '#4facfe', '#a66cff'], fg: '#fff', shine: true, thread: true },
  { id: 'dark', name: 'Negro', bg: ['#16171c', '#16171c'], fg: '#fff', border: '#2b2d36', thread: true },
  { id: 'white', name: 'Blanco', bg: ['#ffffff', '#ffffff'], fg: '#15161b', border: '#dcdfe8', thread: true },
];

// iconSVG arma el SVG de un estilo (o de un color propio: {bg:[a,b], fg, shape}).
// iconSVG arma el SVG de un estilo. Con launcher: true sale "a sangre" (fondo cuadrado completo y
// el dibujo achicado al centro), como piden los íconos adaptables de Android, que recortan la forma.
export function iconSVG(o) {
  const shape = o.launcher ? SHAPES.full : (SHAPES[o.shape] || SHAPES.squircle);
  const defs = [];
  let body = '';
  const fgWrap = (inner) => (o.launcher ? `<g transform="translate(256 256) scale(0.62) translate(-256 -256)">${inner}</g>` : inner);
  if (o.kind === 'stitch') {
    body = shape(`fill="url(#tela)"${o.launcher ? '' : ' stroke="#dccbb0" stroke-width="6"'}`) + fgWrap(stitchH());
    return wrap(defs, body);
  }
  defs.push(grad('bg', o.bg, false));
  body += shape(`fill="url(#bg)"${o.border && !o.launcher ? ` stroke="${o.border}" stroke-width="6"` : ''}`);
  if (o.shine) {
    defs.push('<linearGradient id="shine" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#fff" stop-opacity=".16"/><stop offset=".5" stop-color="#fff" stop-opacity="0"/></linearGradient>');
    body += shape('fill="url(#shine)"');
  }
  const bodyBg = body;
  body = '';
  if (o.kind === 'neon') {
    // En coordenadas del dibujo: con las del objeto, el travesaño (alto cero) no se pintaría.
    defs.push(`<linearGradient id="fg" gradientUnits="userSpaceOnUse" x1="0" y1="120" x2="0" y2="392"><stop offset="0" stop-color="${o.neon[0]}"/><stop offset="1" stop-color="${o.neon[1]}"/></linearGradient>`);
    body += neonH('url(#fg)');
  } else if (o.kind === 'retro') {
    body += retroH(o.fg, o.shadow);
  } else {
    let fg = o.fg;
    if (Array.isArray(fg)) { defs.push(grad('fg', fg, true)); fg = 'url(#fg)'; }
    body += hFill(fg);
    if (o.thread) body += thread(o.threadColor || (typeof o.fg === 'string' ? o.fg : '#fff'));
  }
  return wrap(defs, bodyBg + fgWrap(body));
}

export function wrap(defs, body) {
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512" width="512" height="512"><defs>${defs.join('')}</defs>${body}</svg>`;
}
