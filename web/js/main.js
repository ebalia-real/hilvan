// Punto de entrada de la interfaz.
import { html, render, useEffect } from '../vendor/preact-htm.js';
import { useStore, state, update, setView, closeModal, ensureRooms, reportFocus } from './store.js';
import { initI18n, t } from './i18n.js';
import { rpc } from './rpc.js';
import { Login } from './login.js';
import { Rail, RoomList, roomName } from './sidebar.js';
import { Timeline } from './timeline.js';
import { Viewer, PipLayer } from './viewer.js';
import { Composer } from './composer.js';
import { RoomHeader, RightPanel } from './panels.js';
import { PopoverLayer, DialogLayer, Toasts, Spinner, Avatar, closeMenu } from './ui.js';
import { SecurityModal, VerifyUserModal, VerifyLayer } from './security.js';
import { SettingsModal, applyAppearance } from './settings.js';
import * as M from './modals.js';
import { CallLayer, CallStage } from './call.js';
import { Icon } from './icons.js';
import { Logo } from './policy.js';

function ModalLayer() {
  const modal = useStore((s) => s.modal);
  if (!modal) return null;
  const p = modal.props || {};
  switch (modal.type) {
    case 'login': return html`<div class="overlay" style="padding:0;display:block"><${Login} onCancel=${closeModal} /></div>`;
    case 'settings': return html`<${SettingsModal} ...${p} />`;
    case 'security': return html`<${SecurityModal} ...${p} />`;
    case 'verifyUser': return html`<${VerifyUserModal} ...${p} />`;
    case 'create': return html`<${M.CreateModal} ...${p} />`;
    case 'newdm': return html`<${M.NewDMModal} ...${p} />`;
    case 'explore': return html`<${M.ExploreModal} ...${p} />`;
    case 'join': return html`<${M.JoinModal} ...${p} />`;
    case 'invite': return html`<${M.InviteModal} ...${p} />`;
    case 'upload': return html`<${M.UploadModal} ...${p} />`;
    case 'poll': return html`<${M.PollModal} ...${p} />`;
    case 'schedule': return html`<${M.ScheduleModal} ...${p} />`;
    case 'forward': return html`<${M.ForwardModal} ...${p} />`;
    case 'source': return html`<${M.SourceModal} ...${p} />`;
    case 'report': return html`<${M.ReportModal} ...${p} />`;
    case 'power': return html`<${M.PowerModal} ...${p} />`;
    case 'roomSettings': return html`<${M.RoomSettingsModal} ...${p} />`;
    case 'space': return html`<${M.SpaceModal} ...${p} />`;
    case 'bridges': return html`<${M.BridgesModal} ...${p} />`;
    case 'export': return html`<${M.ExportModal} ...${p} />`;
    default: return null;
  }
}

// Vista en cuadrícula: varias salas con su línea de tiempo y su compositor, a la vez.
function GridView() {
  const cur = useStore((s) => s.cur);
  const grid = useStore((s) => s.view.grid) || [];
  const rooms = useStore((s) => (s.cur && s.acct[s.cur] ? s.acct[s.cur].rooms : null)) || {};
  const cells = grid.filter((id) => rooms[id] && rooms[id].membership === 'join');
  const cols = cells.length <= 1 ? 1 : cells.length <= 4 ? 2 : 3;
  const remove = (id) => setView({ grid: grid.filter((x) => x !== id) });
  const addMenu = (e) => {
    const cand = Object.values(rooms).filter((r) => r.membership === 'join' && r.type !== 'm.space' && !grid.includes(r.id)).sort((x, y) => y.last_ts - x.last_ts).slice(0, 15);
    import('./ui.js').then((m) => m.openMenu(e.currentTarget, cand.map((r) => ({ icon: r.dm ? 'users' : 'hash', label: roomName(r), onClick: () => setView({ grid: [...grid, r.id].slice(-6) }) }))));
  };
  return html`<main class="main grid-main">
    <div class="grid-bar"><${Icon} name="grid" size=18 /><b class="grow">${t('Vista en cuadrícula')}</b>
      ${cells.length < 6 ? html`<button class="btn sm" onClick=${addMenu}><${Icon} name="plus" size=16 /> ${t('Agregar sala')}</button>` : null}
      <button class="btn sm ghost" onClick=${() => setView({ grid: null })}>${t('Salir de la cuadrícula')}</button></div>
    <div class="room-grid" style=${`grid-template-columns:repeat(${cols},minmax(0,1fr))`}>
      ${cells.map((id) => html`<section class="grid-cell" key=${id}>
        <header class="cell-head" onDblClick=${() => setView({ grid: null, room: id })}>
          <${Avatar} a=${cur} mxc=${rooms[id].avatar} name=${roomName(rooms[id])} id=${rooms[id].dm || id} size=${24} />
          <span class="cell-name">${roomName(rooms[id])}</span>${rooms[id].unread ? html`<span class="badge">${rooms[id].unread}</span>` : null}
          <button class="icon-btn sm" title=${t('Abrir sola')} onClick=${() => setView({ grid: null, room: id })}><${Icon} name="maximize" size=15 /></button>
          <button class="icon-btn sm" title=${t('Quitar de la cuadrícula')} onClick=${() => remove(id)}><${Icon} name="close" size=15 /></button>
        </header>
        <${Timeline} key=${`${cur}|${id}`} a=${cur} room=${id} summary=${rooms[id]} />
        <${Composer} a=${cur} room=${id} summary=${rooms[id]} />
      </section>`)}
      ${!cells.length ? html`<div class="welcome"><div><h2>${t('Agregá salas a la cuadrícula')}</h2><button class="btn primary" onClick=${addMenu}>${t('Agregar sala')}</button></div></div>` : null}
    </div>
  </main>`;
}

function Main() {
  const cur = useStore((s) => s.cur);
  const grid = useStore((s) => s.view.grid);
  const room = useStore((s) => s.view.room);
  const summary = useStore((s) => (s.cur && s.view.room && s.acct[s.cur] ? s.acct[s.cur].rooms[s.view.room] : null));
  const ready = useStore((s) => (s.cur && s.acct[s.cur] ? s.acct[s.cur].ready || s.acct[s.cur].loaded : false));
  useEffect(() => { reportFocus(); }, [room]);
  if (grid && grid.length) return html`<${GridView} />`;
  if (!room || !summary) {
    return html`<main class="main"><div class="welcome"><div>
      <${Logo} />
      <h2>${ready ? t('Elegí una conversación') : t('Preparando tus conversaciones…')}</h2>
      <div>${ready ? t('O empezá una nueva con el botón ✎ de arriba.') : html`<div style="display:flex;justify-content:center;margin-top:12px"><${Spinner} /></div>`}</div>
    </div></div></main>`;
  }
  if (summary.membership === 'invite' || summary.membership === 'knock') return html`<main class="main"><${InviteView} a=${cur} summary=${summary} /></main>`;
  return html`<main class="main">
    <${RoomHeader} a=${cur} room=${room} summary=${summary} />
    <${CallStage} a=${cur} room=${room} />
    <${Timeline} key=${`${cur}|${room}`} a=${cur} room=${room} summary=${summary} />
    <${Composer} a=${cur} room=${room} summary=${summary} />
  </main>`;
}

function InviteView({ a, summary }) {
  const accept = async () => {
    try { await rpc.call('room.join', { a, target: summary.id }); } catch (e) { (await import('./store.js')).toast(e.message, 'err'); }
  };
  const decline = async () => {
    try { await rpc.call('room.leave', { a, room: summary.id, forget: true }); setView({ room: null }); } catch (e) { (await import('./store.js')).toast(e.message, 'err'); }
  };
  return html`<div class="welcome"><div class="col" style="align-items:center">
    <${Avatar} a=${a} mxc=${summary.avatar} name=${summary.name} id=${summary.dm || summary.id} size=${84} />
    <h2>${summary.name || t('Invitación')}</h2>
    <div>${summary.membership === 'knock' ? t('Pediste unirte; esperando aprobación.') : summary.inviter_name || summary.inviter ? t('{u} te invitó', { u: summary.inviter_name || summary.inviter }) : ''}</div>
    ${summary.topic ? html`<div class="muted">${summary.topic}</div>` : null}
    ${summary.membership === 'invite' ? html`<div class="row" style="margin-top:10px"><button class="btn" onClick=${decline}>${t('Rechazar')}</button>
      <button class="btn primary" onClick=${accept}>${t('Aceptar')}</button></div>` : null}
  </div></div>`;
}

// Botón "Atrás" (Android): cierra lo último que se abrió. Devuelve si hizo algo; si no, la app
// pasa a segundo plano.
window.hvBack = () => {
  if (state.dialog) { const d = state.dialog; update((s) => { s.dialog = null; }); d.resolve(null); return true; }
  if (state.popover) { closeMenu(); return true; }
  if (state.viewer) { update((s) => { s.viewer = null; }); return true; }
  if (state.modal) { if (!(window.hvModalBack && window.hvModalBack())) closeModal(); return true; }
  if (state.view.grid && state.view.grid.length && window.innerWidth <= 720) { setView({ grid: null }); return true; }
  if (state.view.panel && state.view.room) { setView({ panel: null, thread: null }); return true; }
  if (state.view.room && window.innerWidth <= 720) { setView({ room: null }); return true; }
  return false;
};

function App() {
  const booted = useStore((s) => s.booted);
  const connected = useStore((s) => s.connected);
  const accounts = useStore((s) => s.accounts);
  const panel = useStore((s) => s.view.panel);
  const room = useStore((s) => s.view.room);
  const grid = useStore((s) => s.view.grid);
  useKeyboard();
  if (!booted) return html`<div class="login"><div class="col" style="align-items:center"><img src="/appicon" style="width:72px" /><${Spinner} /></div></div>`;
  if (!accounts.length) return html`<${Login} /><${ModalLayer} /><${PopoverLayer} /><${DialogLayer} /><${Toasts} />`;
  // La cuadrícula también ocupa la pantalla principal (en el teléfono, en lugar de la lista).
  const cls = `layout${panel && room ? ' with-panel' : ''}${room || (grid && grid.length) ? ' room-open' : ''}`;
  return html`<div class=${cls}>
    <${Rail} /><${RoomList} /><${Main} />${panel && room ? html`<${PanelWrap} />` : null}
  </div>
  <${ModalLayer} /><${VerifyLayer} /><${CallLayer} /><${PipLayer} /><${Viewer} /><${PopoverLayer} /><${DialogLayer} /><${Toasts} />`;
}

function PanelWrap() {
  const cur = useStore((s) => s.cur);
  const room = useStore((s) => s.view.room);
  const summary = useStore((s) => (s.cur && s.view.room && s.acct[s.cur] ? s.acct[s.cur].rooms[s.view.room] : null));
  return html`<${RightPanel} a=${cur} room=${room} summary=${summary} />`;
}

// Atajos: Alt+↑/↓ cambia de sala, Ctrl+, ajustes, Esc cierra paneles.
function useKeyboard() {
  useEffect(() => {
    const onKey = (e) => {
      if (e.altKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) {
        const ac = state.cur && state.acct[state.cur];
        if (!ac) return;
        const list = Object.values(ac.rooms).filter((r) => r.membership === 'join' && r.type !== 'm.space').sort((a, b) => b.last_ts - a.last_ts);
        const unreadOnly = e.shiftKey;
        const cand = unreadOnly ? list.filter((r) => r.unread > 0 || r.id === state.view.room) : list;
        const i = cand.findIndex((r) => r.id === state.view.room);
        const next = cand[(i + (e.key === 'ArrowDown' ? 1 : -1) + cand.length) % cand.length];
        if (next) setView({ room: next.id });
        e.preventDefault();
      } else if ((e.ctrlKey || e.metaKey) && e.key === ',') {
        e.preventDefault();
        update((s) => { s.modal = { type: 'settings', props: {} }; });
      } else if (e.key === 'Escape' && !state.modal && !state.popover && state.view.panel) {
        setView({ panel: null, thread: null });
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);
}

async function boot() {
  // Ajustes iniciales (idioma y tema) antes del primer render.
  let settings = {};
  try { settings = await rpc.call('settings.get'); } catch { /* se cargan luego */ }
  await initI18n(settings.language);
  applyAppearance(settings);
  update((s) => { s.settings = settings; });
  render(html`<${App} />`, document.getElementById('app'));
}

rpc.on('settings', (m) => applyAppearance(m.d));
boot();
