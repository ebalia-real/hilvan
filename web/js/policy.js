// Política de la organización (Element Pro): marca, funciones apagadas y ajustes administrados.
import { html } from '../vendor/preact-htm.js';
import { state, useStore } from './store.js';
import { mediaURL } from './util.js';

export const policy = () => (state.info && state.info.policy) || {};
// Funciones apagadas por la organización (o que la plataforma no tiene: en Android el WebView no
// puede capturar la pantalla).
export const off = (feature) => (policy().disable || []).includes(feature)
  || (feature === 'screenshare' && state.info && state.info.os === 'android');
export const brandName = () => (policy().brand && policy().brand.name) || 'Hilvan';
// El logo de la organización se publica en su servidor (mxc://); si no, el ícono que eligió la
// persona (Ajustes → Apariencia) o el de Hilvan.
export function logoSrc() {
  const u = (policy().brand && policy().brand.logo_url) || '';
  const a = state.cur || (state.accounts[0] && state.accounts[0].user_id);
  if (u.startsWith('mxc://') && a) return mediaURL(a, u, { w: 192, h: 192 });
  const s = state.settings || {};
  return s.app_icon && s.app_icon_rev ? `/appicon?v=${s.app_icon_rev}` : '/img/logo.svg';
}

// Logo se actualiza solo al cambiar el ícono o la política.
export function Logo({ style }) {
  useStore((s) => (s.settings || {}).app_icon_rev);
  useStore((s) => s.info);
  return html`<img src=${logoSrc()} alt="" style=${style} />`;
}
export const lockedKeys = () => Object.keys(policy().locked_settings || {});
