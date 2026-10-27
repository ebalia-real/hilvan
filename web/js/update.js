// Actualizaciones de Hilvan: aviso arriba de la lista de chats y sección en Ajustes → Acerca de.
// El núcleo busca versiones nuevas en GitHub y solo instala las firmadas por quien publica.
import { html, useEffect, useState } from '../vendor/preact-htm.js';
import { useStore, update, toast, state } from './store.js';
import { t } from './i18n.js';
import { Icon } from './icons.js';
import { Switch, Setting } from './ui.js';
import { rpc } from './rpc.js';

rpc.on('update', (m) => update((s) => { s.update = m.d; }));
rpc.on('$open', () => {
  rpc.call('update.status').then((r) => update((s) => { s.update = r.info; s.packaging = r.packaging; })).catch(() => {});
});

const install = () => rpc.call('update.install').catch((e) => toast(e.message, 'err'));
const openNotes = (u) => u && u.url && rpc.call('app.openExternal', { url: u.url }).catch(() => {});

function UpdateState({ u, compact }) {
  if (!u) return null;
  switch (u.state) {
    case 'available':
      return html`<div class="row wrap" style="gap:8px">
        <button class="btn primary" style="height:32px" onClick=${install}><${Icon} name="download" size=16 /> ${t('Actualizar')}</button>
        ${u.url ? html`<button class="btn ghost" style="height:32px" onClick=${() => openNotes(u)}>${t('Novedades')}</button>` : null}
        ${compact ? html`<button class="btn ghost" style="height:32px" onClick=${() => { try { sessionStorage.setItem('hv.updSkip', u.version); } catch { /* */ } update((s) => { s.update = { ...s.update }; }); }}>${t('Más tarde')}</button>` : null}
      </div>`;
    case 'downloading':
      return html`<div class="col" style="gap:6px"><div class="small muted">${t('Descargando…')} ${Math.round((u.progress || 0) * 100)}%</div>
        <div class="transfer"><div class="tbar"><span style=${`width:${Math.round((u.progress || 0) * 100)}%`}></span></div></div></div>`;
    case 'installing':
      return html`<div class="small muted">${t('Verificando la firma e instalando…')}</div>`;
    case 'ready':
      return html`<div class="small muted">${t('Listo: Hilvan se reinicia con la versión nueva.')}</div>`;
    case 'manual':
      return html`<div class="small muted">${u.manual}</div>
        ${u.url ? html`<button class="btn" style="height:32px;width:fit-content" onClick=${() => openNotes(u)}>${t('Ver la versión')}</button>` : null}`;
    case 'error':
      return html`<div class="small" style="color:var(--danger)">${u.error}</div>
        <button class="btn" style="height:32px;width:fit-content" onClick=${() => rpc.call('update.check').then(install)}>${t('Reintentar')}</button>`;
    default:
      return null;
  }
}

// UpdateBanner: aviso arriba de la lista de chats cuando hay una versión nueva.
export function UpdateBanner() {
  const u = useStore((s) => s.update);
  if (!u || u.state === 'uptodate' || !u.version) return null;
  let skipped = '';
  try { skipped = sessionStorage.getItem('hv.updSkip') || ''; } catch { /* */ }
  if (u.state === 'available' && skipped === u.version) return null;
  return html`<div class="list-banner">
    <div class="row"><${Icon} name="sparkles" size=18 /><b>${t('Hilvan {v} está disponible', { v: u.version })}</b></div>
    <${UpdateState} u=${u} compact />
  </div>`;
}

// UpdateSection: en Ajustes → Acerca de.
export function UpdateSection() {
  const u = useStore((s) => s.update);
  const pkg = useStore((s) => s.packaging);
  const settings = useStore((s) => s.settings);
  const [busy, setBusy] = useState(false);
  const check = async () => {
    setBusy(true);
    try {
      const r = await rpc.call('update.check');
      if (r.state === 'uptodate') toast(t('Tenés la última versión'));
    } catch (e) { toast(e.message, 'err'); } finally { setBusy(false); }
  };
  useEffect(() => { if (!state.update) rpc.call('update.status').then((r) => update((s) => { s.update = r.info; s.packaging = r.packaging; })).catch(() => {}); }, []);
  const dev = pkg === 'dev';
  return html`<div class="col" style="gap:10px;width:100%;max-width:520px;text-align:left;margin-top:12px">
    <h4 style="margin:0">${t('Actualizaciones')}</h4>
    ${dev ? html`<div class="small muted">${t('Esta es una compilación de desarrollo: no se actualiza sola.')}</div>` : html`
      <${Setting} title=${t('Buscar actualizaciones automáticamente')} sub=${t('Cada 12 horas. Solo se instalan versiones firmadas por quien publica Hilvan.')}>
        <${Switch} checked=${settings.update_check !== false} onChange=${(v) => import('./settings.js').then((m) => m.setSetting({ update_check: v }))} /><//>
      ${u && u.version && u.state !== 'uptodate' ? html`<div class="card col" style="gap:8px"><b>${t('Hilvan {v} está disponible', { v: u.version })}</b><${UpdateState} u=${u} /></div>`
        : html`<div class="small muted">${u && u.checked ? t('Tenés la última versión.') : ''}</div>`}
      <button class="btn" style="width:fit-content" disabled=${busy} onClick=${check}><${Icon} name="refresh" size=16 /> ${busy ? t('Buscando…') : t('Buscar ahora')}</button>`}
  </div>`;
}
