// Ajustes de la aplicación y de la cuenta.
import { html, useState, useEffect, useRef } from '../vendor/preact-htm.js';
import { useStore, state, update, toast, closeModal, openModal, mutAcct } from './store.js';
import { t, LANGS, lang } from './i18n.js';
import { Icon } from './icons.js';
import { Modal, Spinner, Avatar, Switch, Seg, Setting, confirmDialog, ErrorText } from './ui.js';
import { rpc, upload } from './rpc.js';
import { localpart, formatShort, mediaURL, setHour12 } from './util.js';
import { openExternal, saveTextFile } from './message.js';
import { AdminPanel } from './admin.js';
import { refreshPacksFor } from './emoji.js';
import { playSound, SOUND_NAMES } from './sounds.js';
import { policy, off, lockedKeys, Logo } from './policy.js';
import { AppIconPicker } from './appicon.js';
import { UpdateSection } from './update.js';

// --- preferencias ---
export function pref(key, def) {
  const ui = (state.settings && state.settings.ui) || {};
  return ui[key] === undefined ? def : ui[key];
}

export async function setSetting(patch) {
  try {
    const s = await rpc.call('settings.set', patch);
    update((st) => { st.settings = s; });
    applyAppearance(s);
    return s;
  } catch (e) { toast(e.message, 'err'); return null; }
}
export const setUI = (patch) => setSetting({ ui: patch });

let darkMQ = null;
let lastDark = null;
function reportTheme(s) {
  const dark = s.theme === 'dark' || (s.theme !== 'light' && window.matchMedia('(prefers-color-scheme: dark)').matches);
  if (dark === lastDark) return;
  lastDark = dark;
  rpc.call('app.theme', { dark }).catch(() => {});
}

export function applyAppearance(s) {
  const root = document.documentElement;
  if (!darkMQ) {
    darkMQ = window.matchMedia('(prefers-color-scheme: dark)');
    darkMQ.addEventListener('change', () => reportTheme(state.settings || {}));
  }
  reportTheme(s);
  if (s.theme === 'light' || s.theme === 'dark') root.dataset.theme = s.theme; else delete root.dataset.theme;
  if (s.accent) root.style.setProperty('--accent', s.accent);
  const ui = s.ui || {};
  root.style.setProperty('--fs', `${ui.fontSize || 15}px`);
  root.style.setProperty('--saturation', `${ui.saturation ?? 100}%`);
  root.classList.toggle('desat', (ui.saturation ?? 100) < 100);
  setHour12(ui.time24 === false);
  const b = document.body.classList;
  b.toggle('compact', !!ui.compact);
  b.toggle('bubbles', ui.layout === 'bubbles');
  b.toggle('reduce-motion', !!ui.reduceMotion);
  b.toggle('underline-links', !!ui.underlineLinks);
  b.toggle('no-name-colors', ui.nameColors === false);
  b.toggle('hide-avatars-list', ui.listAvatars === false);
  b.toggle('no-previews', ui.roomPreviews === false);
}

const ACCENTS = ['#6d5dfc', '#3d7cf5', '#8e4ec6', '#d6409f', '#e5484d', '#e2701a', '#30a46c', '#12a594', '#16c2a3', '#6f6e77'];

// Secciones de la navegación (clave, icono, título, palabras para la búsqueda).
function sections(isAdmin) {
  const android = state.info && state.info.os === 'android';
  return [
    { group: t('Usuario') },
    ['account', 'users', t('Cuenta'), 'perfil nombre foto correo contraseña desactivar'],
    ['privacy', 'eye', t('Privacidad'), 'lectura escribiendo presencia ignorados bloquear ip'],
    ['security', 'shield', t('Seguridad y cifrado'), 'verificar clave recuperación respaldo cifrado msc4153 exportar importar'],
    ['sessions', 'device', t('Sesiones'), 'dispositivos cerrar sesión'],
    { group: t('Aplicación') },
    ['appearance', 'palette', t('Apariencia'), 'tema oscuro claro acento color burbujas compacto hora lista icono logo estilo'],
    ['accessibility', 'eye', t('Accesibilidad'), 'movimiento animaciones saturación enlaces tamaño texto gif'],
    ['voice', 'mic', t('Voz y video'), 'micrófono altavoz cámara volumen ruido eco sensibilidad pulsar hablar atenuación códec'],
    android ? null : ['streaming', 'screen', t('Transmisión'), 'compartir pantalla stream resolución fps audio calidad bitrate cursor'],
    ['sounds', 'bell', t('Sonidos'), 'sonido llamada silenciar ensordecer cámara'],
    ['chat', 'thread', t('Chat'), 'enter markdown emoji enlaces imágenes recibos uniones comprimir'],
    ['notifications', 'bell', t('Notificaciones'), 'escritorio no molestar palabras clave menciones'],
    ['emotes', 'smile', t('Emojis y stickers'), 'paquetes personalizados'],
    android ? null : ['keys', 'sliders', t('Atajos de teclado'), 'teclas'],
    ['system', 'server', t('Red y sistema'), 'proxy tor segundo plano bandeja minimizar cerrar inicio automático caché resincronizar idioma salir'],
    off('labs') ? null : ['labs', 'sparkles', t('Funciones experimentales'), 'sliding sync desarrollador'],
    isAdmin ? { group: t('Servidor') } : null,
    isAdmin ? ['admin', 'server', t('Administración'), 'usuarios salas synapse'] : null,
    { group: '' },
    ['about', 'info', t('Acerca de'), 'versión'],
  ].filter(Boolean);
}

export function SettingsModal({ tab: initial }) {
  const [tab, setTab] = useState(initial || 'account');
  // En el teléfono: primero la lista de secciones; al elegir una, su contenido a todo el ancho.
  const [showBody, setShowBody] = useState(!!initial);
  useEffect(() => {
    window.hvModalBack = () => {
      if (showBody && matchMedia('(max-width: 720px)').matches) { setShowBody(false); return true; }
      return false;
    };
    return () => { window.hvModalBack = null; };
  }, [showBody]);
  const [isAdmin, setIsAdmin] = useState(false);
  const [q, setQ] = useState('');
  const cur = useStore((s) => s.cur);
  useEffect(() => { if (cur) rpc.call('admin.check', { a: cur }).then((r) => setIsAdmin(r.admin)).catch(() => {}); }, [cur]);
  const secs = sections(isAdmin);
  const ql = q.trim().toLowerCase();
  const visible = ql ? secs.filter((sx) => sx.group === undefined && `${sx[2]} ${sx[3]}`.toLowerCase().includes(ql)) : secs;
  const Body = {
    account: AccountTab, privacy: PrivacyTab, security: SecurityTab, sessions: SessionsTab, appearance: AppearanceTab,
    accessibility: AccessibilityTab, voice: VoiceTab, streaming: StreamingTab, sounds: SoundsTab, chat: ChatTab,
    notifications: NotificationsTab, emotes: EmotesTab, keys: KeysTab, system: SystemTab, labs: LabsTab, admin: AdminPanel, about: AboutTab,
  }[tab] || AccountTab;
  const current = secs.find((sx) => sx[0] === tab);
  return html`<${Modal} title=${t('Ajustes')} cls="xwide settings-modal" onClose=${closeModal} noHead>
    <div class=${`settings${showBody ? ' show-body' : ''}`}>
      <nav class="settings-nav">
        <div class="row" style="padding:6px 8px 8px"><b style="flex:1;font-size:17px">${t('Ajustes')}</b>
          <button class="icon-btn sm" onClick=${closeModal} aria-label=${t('Cerrar')}><${Icon} name="close" size=18 /></button></div>
        <label class="search" style="margin:0 4px 8px"><${Icon} name="search" size=16 /><input placeholder=${t('Buscar ajustes')} value=${q} onInput=${(e) => setQ(e.currentTarget.value)} /></label>
        ${visible.map((sx) => sx.group !== undefined
          ? (sx.group ? html`<div class="nav-group">${sx.group}</div>` : html`<div class="rail-sep" style="width:auto;margin:6px 8px"></div>`)
          : html`<button class=${tab === sx[0] ? 'on' : ''} onClick=${() => { setTab(sx[0]); setShowBody(true); }}><${Icon} name=${sx[1]} size=18 />${sx[2]}<span class="nav-chev"><${Icon} name="chevronRight" size=16 /></span></button>`)}
      </nav>
      <div class="settings-body">
        <div class="settings-mhead">
          <button class="icon-btn" onClick=${() => setShowBody(false)} aria-label=${t('Volver')}><${Icon} name="chevronLeft" /></button>
          <b class="grow">${current ? current[2] : t('Ajustes')}</b>
          <button class="icon-btn" onClick=${closeModal} aria-label=${t('Cerrar')}><${Icon} name="close" /></button>
        </div>
        <div class="settings-inner">
        ${lockedKeys().length || policy().organization ? html`<div class="managed-banner"><${Icon} name="shield" size=16 />
          <span>${policy().organization ? t('Hilvan está administrado por {o}.', { o: policy().organization }) : t('Hilvan está administrado por tu organización.')}
          ${lockedKeys().length ? ' ' + t('Algunos ajustes los fija tu organización y no se pueden cambiar.') : ''}</span></div>` : null}
        <${Body} /></div></div>
    </div><//>`;
}

// ---------------- Cuenta ----------------
async function withPassword(cur, method, params, title) {
  try {
    const r = await rpc.call(method, { a: cur, ...params }).catch(async (e) => {
      if (e.message !== 'password_required') throw e;
      const pass = await confirmDialog({ title: title || t('Confirmá tu contraseña'), input: 'password', ok: t('Confirmar') });
      if (!pass) return null;
      return rpc.call(method, { a: cur, ...params, password: pass });
    });
    if (r && r.approval_url) openExternal(r.approval_url);
    return r;
  } catch (e) { toast(e.message, 'err'); return null; }
}

function AccountTab() {
  const cur = useStore((s) => s.cur);
  const accounts = useStore((s) => s.accounts);
  const me = accounts.find((a) => a.user_id === cur);
  const [name, setName] = useState(me ? me.displayname : '');
  const [pids, setPids] = useState(null);
  const [emailSid, setEmailSid] = useState('');
  useEffect(() => setName(me ? me.displayname : ''), [cur]);
  const loadPids = () => rpc.call('account.3pids', { a: cur }).then((r) => setPids((r && r.threepids) || [])).catch(() => setPids([]));
  useEffect(() => { loadPids(); }, [cur]);
  if (!me) return null;
  const oauth = me.login_type === 'oauth';
  const saveName = () => rpc.call('profile.set', { a: cur, name }).then(() => toast(t('Guardado'))).catch((e) => toast(e.message, 'err'));
  const pickAvatar = () => {
    const inp = document.createElement('input');
    inp.type = 'file'; inp.accept = 'image/*';
    inp.onchange = async () => {
      try {
        const r = await upload({ a: cur, mode: 'plain', name: inp.files[0].name, mime: inp.files[0].type }, inp.files[0]);
        await rpc.call('profile.set', { a: cur, avatar: r.url });
        toast(t('Foto actualizada'));
      } catch (e) { toast(e.message, 'err'); }
    };
    inp.click();
  };
  const manage = async (action = '') => {
    const r = await rpc.call('account.manageUrl', { a: cur, action }).catch(() => ({}));
    if (r.url) openExternal(r.url); else toast(t('Tu servidor no ofrece gestión de cuenta externa'));
  };
  const changePassword = async () => {
    if (oauth) { manage(''); return; }
    const old = await confirmDialog({ title: t('Contraseña actual'), input: 'password', ok: t('Siguiente') });
    if (!old) return;
    const nw = await confirmDialog({ title: t('Contraseña nueva'), text: t('Al menos 8 caracteres.'), input: 'password', ok: t('Siguiente') });
    if (!nw) return;
    const again = await confirmDialog({ title: t('Repetí la contraseña nueva'), input: 'password', ok: t('Cambiar') });
    if (again !== nw) { toast(t('Las contraseñas no coinciden'), 'err'); return; }
    try { await rpc.call('account.password', { a: cur, old, new: nw, logout_others: false }); toast(t('Contraseña cambiada')); } catch (e) { toast(e.message, 'err'); }
  };
  const addEmail = async () => {
    const email = await confirmDialog({ title: t('Agregar correo'), input: 'text', placeholder: 'nombre@ejemplo.com', ok: t('Enviar confirmación') });
    if (!email) return;
    try { const r = await rpc.call('account.emailRequest', { a: cur, email }); setEmailSid(r.sid); toast(t('Revisá tu correo y abrí el enlace de confirmación')); } catch (e) { toast(e.message, 'err'); }
  };
  const confirmEmail = async () => {
    const r = await withPassword(cur, 'account.emailAdd', { sid: emailSid });
    if (r) { setEmailSid(''); loadPids(); toast(t('Correo vinculado')); }
  };
  const deactivate = async () => {
    if (!(await confirmDialog({ title: t('Desactivar cuenta'), danger: true, ok: t('Desactivar para siempre'),
      text: t('Tu cuenta quedará inutilizable de forma permanente y no podrás volver a usar este nombre de usuario. Esta acción no se puede deshacer.') }))) return;
    const r = await withPassword(cur, 'account.deactivate', { erase: false }, t('Confirmá tu contraseña para desactivar'));
    if (r && !r.approval_url) { closeModal(); toast(t('Cuenta desactivada')); }
  };
  return html`<div class="col">
    <h3>${t('Cuenta')}</h3>
    <div class="profile-card">
      <div class="profile-banner"></div>
      <div class="row profile-row">
        <div class="profile-avatar" onClick=${pickAvatar} title=${t('Cambiar foto')}>
          <${Avatar} a=${cur} mxc=${me.avatar} name=${me.displayname || localpart(cur)} id=${cur} size=${76} /></div>
        <div class="col grow" style="gap:2px;margin-top:30px"><b style="font-size:18px">${me.displayname || localpart(cur)}</b><span class="muted selectable small">${cur}</span></div>
        <button class="btn" style="margin-top:30px" onClick=${pickAvatar}>${t('Cambiar foto')}</button>
      </div>
    </div>
    <div class="field"><label>${t('Nombre visible')}</label><div class="row"><input class="input" value=${name} onInput=${(e) => setName(e.currentTarget.value)} onKeyDown=${(e) => e.key === 'Enter' && saveName()} />
      <button class="btn primary" onClick=${saveName}>${t('Guardar')}</button></div></div>
    <div class="card small col" style="gap:4px">
      <div><span class="muted">${t('Servidor')}:</span> <span class="selectable">${me.homeserver}</span></div>
      <div><span class="muted">${t('Dispositivo')}:</span> <span class="selectable mono">${me.device_id}</span></div>
      <div><span class="muted">${t('Inicio de sesión')}:</span> ${{ oauth: 'OAuth 2.0 (MSC3861)', sso: 'SSO', password: t('Contraseña') }[me.login_type] || me.login_type}</div>
      <div><span class="muted">${t('Sincronización')}:</span> ${me.sync_mode === 'sliding' ? 'Simplified Sliding Sync (MSC4186)' : '/sync'}</div>
    </div>

    <h4>${t('Correos y teléfonos')}</h4>
    ${pids === null ? html`<${Spinner} sm />` : pids.length === 0 ? html`<div class="small muted">${t('No hay correos ni teléfonos vinculados.')}</div>` : pids.map((p) => html`<${Setting}
      title=${p.address} sub=${p.medium === 'email' ? t('Correo') : t('Teléfono')}>
      <button class="icon-btn" title=${t('Quitar')} onClick=${async () => { if (await confirmDialog({ title: t('Quitar'), text: p.address, danger: true, ok: t('Quitar') })) rpc.call('account.3pidDelete', { a: cur, medium: p.medium, address: p.address }).then(loadPids).catch((e) => toast(e.message, 'err')); }}><${Icon} name="trash" size=18 /></button><//>`)}
    <div class="row">${!oauth ? html`<button class="btn" onClick=${addEmail}><${Icon} name="plus" size=16 /> ${t('Agregar correo')}</button>` : null}
      ${emailSid ? html`<button class="btn primary" onClick=${confirmEmail}>${t('Ya confirmé el correo')}</button>` : null}</div>

    <h4>${t('Contraseña y acceso')}</h4>
    <div class="row wrap">
      <button class="btn" onClick=${changePassword}><${Icon} name="key" size=16 /> ${t('Cambiar contraseña')}</button>
      ${oauth ? html`<button class="btn" onClick=${() => manage('')}><${Icon} name="globe" size=16 /> ${t('Gestionar cuenta')}</button>` : null}
      <button class="btn danger" onClick=${async () => {
        if (await confirmDialog({ title: t('Cerrar sesión'), text: t('¿Cerrar la sesión de {u} en este dispositivo?', { u: cur }), ok: t('Cerrar sesión'), danger: true })) { closeModal(); rpc.call('account.logout', { a: cur }); }
      }}><${Icon} name="logout" size=16 /> ${t('Cerrar sesión')}</button>
    </div>

    <h4 class="danger-title">${t('Zona de peligro')}</h4>
    <div class="card warn row"><div class="grow small">${t('Desactivar la cuenta es permanente.')}</div>
      <button class="btn danger" onClick=${deactivate}>${t('Desactivar cuenta')}</button></div>
  </div>`;
}

// ---------------- Privacidad ----------------
function PrivacyTab() {
  const s = useStore((st) => st.settings);
  const cur = useStore((st) => st.cur);
  const [ignored, setIgnored] = useState(null);
  const load = () => rpc.call('user.ignored', { a: cur }).then(setIgnored).catch(() => setIgnored([]));
  useEffect(() => { load(); }, [cur]);
  const receipts = s.private_receipts ? 'private' : 'public';
  return html`<div>
    <h3>${t('Privacidad')}</h3>
    <${Setting} title=${t('Confirmaciones de lectura')} sub=${t('Pública: otros ven hasta dónde leíste. Privada: solo se sincroniza entre tus dispositivos.')}>
      <${Seg} value=${receipts} options=${[['public', t('Pública')], ['private', t('Privada')]]}
        onChange=${(v) => setSetting({ send_read_receipts: true, private_receipts: v === 'private' })} /><//>
    <${Setting} title=${t('Indicador de escritura')} sub=${t('Mostrar a otros cuando estás escribiendo')}><${Switch} checked=${s.send_typing} onChange=${(v) => setSetting({ send_typing: v })} /><//>
    <${Setting} title=${t('Compartir estado en línea')}><${Switch} checked=${s.show_presence} onChange=${(v) => setSetting({ show_presence: v })} /><//>
    <${Setting} title=${t('Ocultar mi IP en llamadas')} sub=${t('Las llamadas pasan siempre por el servidor de retransmisión (TURN). Un poco más de latencia, más privacidad.')}>
      <${Switch} checked=${s.call_force_relay} onChange=${(v) => setSetting({ call_force_relay: v })} /><//>
    <${Setting} title=${t('Vista previa de enlaces')} sub=${t('La genera tu servidor; el sitio no ve tu IP.')}><${Switch} checked=${s.url_previews} onChange=${(v) => setSetting({ url_previews: v })} /><//>
    <${Setting} title=${t('Vista previa de enlaces en salas cifradas')} sub=${t('Desactivado por defecto: el servidor vería el enlace.')}><${Switch} checked=${s.url_previews_encrypted} onChange=${(v) => setSetting({ url_previews_encrypted: v })} /><//>
    <h4>${t('Usuarios ignorados')}</h4>
    ${ignored === null ? html`<${Spinner} sm />` : ignored.length === 0 ? html`<div class="small muted">${t('No ignorás a nadie.')}</div>` : ignored.map((u) => html`<${Setting} title=${u}>
      <button class="btn" onClick=${() => rpc.call('user.ignore', { a: cur, user: u, ignore: false }).then(() => setTimeout(load, 500))}>${t('Dejar de ignorar')}</button><//>`)}
    <div class="small muted" style="margin-top:12px">${t('Hilvan nunca carga imágenes de sitios externos dentro de los mensajes, para que nadie pueda rastrear cuándo los leés.')}</div>
  </div>`;
}

// ---------------- Seguridad y cifrado ----------------
function SecurityTab() {
  const cur = useStore((s) => s.cur);
  const crypto = useStore((s) => (s.cur && s.acct[s.cur] ? s.acct[s.cur].crypto : null));
  const s = useStore((st) => st.settings);
  const info = useStore((st) => st.info);
  useEffect(() => { rpc.call('crypto.status', { a: cur }).then((c) => mutAcct(cur, (x) => { x.crypto = c; })).catch(() => {}); }, [cur]);
  const exportKeys = async () => {
    const pass = await confirmDialog({ title: t('Exportar claves'), text: t('Elegí una frase para proteger el archivo. La vas a necesitar para importarlo.'), input: 'password', ok: t('Exportar') });
    if (!pass) return;
    try {
      const r = await rpc.call('crypto.export', { a: cur, passphrase: pass });
      await saveTextFile('hilvan-claves.txt', r.data);
    } catch (e) { toast(e.message, 'err'); }
  };
  const importKeys = () => {
    const inp = document.createElement('input');
    inp.type = 'file';
    inp.onchange = async () => {
      const text = await inp.files[0].text();
      const pass = await confirmDialog({ title: t('Importar claves'), text: t('Ingresá la frase con la que se exportó el archivo.'), input: 'password', ok: t('Importar') });
      if (!pass) return;
      try { const r = await rpc.call('crypto.import', { a: cur, passphrase: pass, data: text }); toast(t('Se importaron {n} de {m} claves', { n: r.imported, m: r.total })); } catch (e) { toast(e.message, 'err'); }
    };
    inp.click();
  };
  return html`<div class="col">
    <h3>${t('Seguridad y cifrado')}</h3>
    ${crypto ? html`<div class=${`card ${crypto.verified ? 'ok' : 'warn'} col`} style="gap:6px">
      <div class="row" style="font-weight:650"><${Icon} name=${crypto.verified ? 'shieldOk' : 'shieldWarn'} />
        ${crypto.verified ? t('Este dispositivo está verificado') : crypto.cross_signing ? t('Este dispositivo no está verificado') : t('Tu cuenta todavía no tiene identidad criptográfica')}</div>
      <div class="small muted">${t('Huella del dispositivo')}: <span class="mono selectable">${crypto.fingerprint}</span></div>
      <div class="small">${crypto.backup ? (crypto.backup_key ? t('Respaldo de claves activo ({n} claves)', { n: crypto.backup.count }) : t('Hay un respaldo de claves, pero este dispositivo no tiene la clave')) : t('Sin respaldo de claves')}</div>
      <div class="row wrap">
        ${!crypto.cross_signing ? html`<button class="btn primary" onClick=${() => openModal('security', { step: 'setup' })}>${t('Configurar recuperación')}</button>` : null}
        ${crypto.cross_signing && !crypto.verified ? html`<button class="btn primary" onClick=${() => openModal('security', { step: 'verify' })}>${t('Verificar este dispositivo')}</button>` : null}
        ${crypto.cross_signing && !crypto.backup_key ? html`<button class="btn" onClick=${() => openModal('security', { step: 'backup' })}>${t('Ingresar clave de recuperación')}</button>` : null}
        ${crypto.verified ? html`<button class="btn" onClick=${() => openModal('security', { step: 'setup' })}>${t('Crear nueva clave de recuperación')}</button>` : null}
      </div>
    </div>` : html`<${Spinner} />`}
    <h4>${t('Opciones de cifrado')}</h4>
    <${Setting} title=${t('Criptografía invisible (MSC4153)')} sub=${t('No envía claves a dispositivos que su dueño no verificó. Más seguro y sin advertencias molestas.')}>
      <${Switch} checked=${s.invisible_crypto} onChange=${(v) => setSetting({ invisible_crypto: v })} /><//>
    <${Setting} title=${t('Compartir historial al invitar (MSC4268)')} sub=${t('Quien invitás puede leer los mensajes anteriores si el historial es visible para miembros.')}>
      <${Switch} checked=${s.share_history_on_invite} onChange=${(v) => setSetting({ share_history_on_invite: v })} /><//>
    <div class="row"><button class="btn" onClick=${exportKeys}><${Icon} name="download" size=16 /> ${t('Exportar claves')}</button>
      <button class="btn" onClick=${importKeys}><${Icon} name="upload" size=16 /> ${t('Importar claves')}</button></div>
    <div class="small muted">${t('Las credenciales se guardan en')}: ${info && info.secrets === 'keyring' ? t('el llavero del sistema') : t('un archivo protegido de este usuario')}</div>
    <${PinningSection} cur=${cur} />
  </div>`;
}

// Fijación del certificado del servidor: si alguien intercepta la conexión con otro certificado
// (aunque sea "válido"), Hilvan se niega a conectar.
function PinningSection({ cur }) {
  const [st, setSt] = useState(null);
  const load = (enable) => rpc.call('account.pinning', { a: cur, ...(enable === undefined ? {} : { enable }) }).then(setSt).catch((e) => toast(e.message, 'err'));
  useEffect(() => { load(); }, [cur]);
  if (!st) return null;
  const hosts = Object.entries(st.pins || {});
  const org = Object.keys(st.policy || {});
  return html`<h4>${t('Fijación de certificados')}</h4>
    <${Setting} title=${t('Fijar el certificado de mi servidor')} sub=${t('Hilvan recuerda la clave del servidor la primera vez y bloquea la conexión si cambia por otra (protege contra interceptaciones).')}>
      <${Switch} checked=${st.enabled} onChange=${(v) => load(v)} /><//>
    ${hosts.map(([h, pins]) => html`<div class="small muted">${h}: <span class="mono">${pins.length} ${t('claves')} · ${pins[0].slice(0, 16)}…</span></div>`)}
    ${st.enabled && hosts.length ? html`<button class="btn sm" onClick=${() => load(true)}>${t('Volver a aprender (si tu servidor cambió de certificado)')}</button>` : null}
    ${org.length ? html`<div class="small muted">${t('Tu organización fija los certificados de')}: ${org.join(', ')}</div>` : null}`;
}

// ---------------- Sesiones ----------------
const INACTIVE_DAYS = 90;

function SessionsTab() {
  const cur = useStore((s) => s.cur);
  const crypto = useStore((s) => (s.cur && s.acct[s.cur] ? s.acct[s.cur].crypto : null));
  const changed = useStore((s) => s.devicesChanged || 0);
  const queue = useStore((s) => s.verifyQueue);
  const [devs, setDevs] = useState(null);
  const [err, setErr] = useState(null);
  const [sel, setSel] = useState(() => new Set());
  const load = () => rpc.call('devices.list', { a: cur }).then((d) => {
    setDevs(d);
    setSel((x) => new Set([...x].filter((id) => d.some((dv) => dv.id === id && !dv.current))));
  }).catch(setErr);
  useEffect(() => { load(); }, [cur, changed]);
  const rename = async (d) => {
    const name = await confirmDialog({ title: t('Renombrar dispositivo'), input: 'text', value: d.name, ok: t('Guardar') });
    if (name) rpc.call('devices.rename', { a: cur, id: d.id, name }).then(load);
  };
  const trust = (tr) => ({ verified: [t('Verificado'), 'shield-ok', 'shieldOk'], cross_signed: [t('Verificado por su dueño'), 'shield-ok', 'shieldOk'],
    unverified: [t('No verificado'), 'shield-warn', 'shieldWarn'], no_crypto: [t('Sin cifrado'), 'muted', 'shield'], changed: [t('Identidad cambiada'), 'shield-bad', 'shieldWarn'] }[tr] || [tr, 'muted', 'shield']);
  const list = (devs || []).slice().sort((x, y) => (y.current ? 1 : 0) - (x.current ? 1 : 0) || (y.last_seen_ts || 0) - (x.last_seen_ts || 0));
  const others = list.filter((d) => !d.current);
  const inactive = (d) => d.last_seen_ts && Date.now() - d.last_seen_ts > INACTIVE_DAYS * 86400000;
  const unverified = (d) => d.trust === 'unverified' || d.trust === 'changed';
  const toggle = (id) => setSel((x) => { const n = new Set(x); if (n.has(id)) n.delete(id); else n.add(id); return n; });
  const selectWhere = (f) => setSel(new Set(others.filter(f).map((d) => d.id)));
  const allOn = others.length > 0 && others.every((d) => sel.has(d.id));
  const chosen = others.filter((d) => sel.has(d.id));
  const chosenUnverified = chosen.filter(unverified);
  const canVerify = crypto && crypto.verified;
  const signOut = async (ds) => {
    if (!ds.length) return;
    const names = ds.slice(0, 4).map((d) => d.name || d.id).join(', ') + (ds.length > 4 ? '…' : '');
    if (!(await confirmDialog({ title: ds.length === 1 ? t('Cerrar sesión del dispositivo') : t('Cerrar {n} sesiones', { n: ds.length }),
      text: t('Se cerrará la sesión en: {d}', { d: names }), ok: t('Cerrar sesión'), danger: true }))) return;
    // Un solo pedido (y una sola confirmación de contraseña) para todas.
    await withPassword(cur, 'devices.delete', { ids: ds.map((d) => d.id) });
    setSel(new Set());
    load();
  };
  // Verificar varias: se piden de a una; al terminar una, sigue la próxima.
  const verifyMany = (ds) => {
    if (!ds.length) return;
    update((s) => { s.verifyQueue = { a: cur, ids: ds.map((d) => d.id), total: ds.length }; });
    rpc.call('verify.start', { a: cur, user: cur }).catch((e) => toast(e.message, 'err'));
  };
  return html`<div class="col">
    <h3>${t('Sesiones')}</h3>
    <div class="small muted">${t('Dispositivos donde tu cuenta tiene una sesión abierta. Cerrá los que no reconozcas.')}</div>
    <${ErrorText} err=${err} />
    ${others.length ? html`<div class="sess-bar">
      <label class="sess-check"><input type="checkbox" checked=${allOn} indeterminate=${!allOn && sel.size > 0}
        onChange=${() => setSel(allOn ? new Set() : new Set(others.map((d) => d.id)))} /> ${sel.size ? (sel.size === 1 ? t('1 seleccionada') : t('{n} seleccionadas', { n: sel.size })) : t('Seleccionar todas')}</label>
      <span class="grow"></span>
      <button class="chip" onClick=${() => selectWhere(unverified)}>${t('No verificadas')} (${others.filter(unverified).length})</button>
      <button class="chip" onClick=${() => selectWhere(inactive)}>${t('Inactivas +{d} días', { d: INACTIVE_DAYS })} (${others.filter(inactive).length})</button>
    </div>` : null}
    ${sel.size ? html`<div class="sess-actions">
      ${chosenUnverified.length ? html`<button class="btn" disabled=${!canVerify} title=${canVerify ? '' : t('Primero verificá este dispositivo (Seguridad y cifrado)')}
        onClick=${() => verifyMany(chosenUnverified)}><${Icon} name="shieldOk" size=16 /> ${t('Verificar ({n})', { n: chosenUnverified.length })}</button>` : null}
      <button class="btn danger" onClick=${() => signOut(chosen)}><${Icon} name="logout" size=16 /> ${t('Cerrar sesión ({n})', { n: chosen.length })}</button>
      <button class="btn ghost" onClick=${() => setSel(new Set())}>${t('Cancelar')}</button>
      ${chosenUnverified.length && !canVerify ? html`<span class="small muted" style="align-self:center">${t('Para verificar otras sesiones, primero verificá esta.')}</span>` : null}
    </div>` : null}
    ${queue && queue.a === cur ? html`<div class="card col" style="gap:4px">
      <b>${t('Verificando {n} de {m}', { n: queue.total - queue.ids.length + 1, m: queue.total })}</b>
      <div class="small muted">${t('Aceptá la solicitud en el otro dispositivo y compará los emojis. Al terminar sigue con el próximo.')}</div>
      <button class="btn sm" style="width:fit-content" onClick=${() => update((s) => { s.verifyQueue = null; })}>${t('Detener')}</button></div>` : null}
    ${devs ? list.map((d) => {
      const [label, cls, ic] = trust(d.trust);
      return html`<div class=${`setting sess-row${sel.has(d.id) ? ' on' : ''}`} onClick=${(e) => { if (!d.current && !e.target.closest('button,input,a')) toggle(d.id); }}>
        ${d.current ? html`<span class="sess-cb-space"></span>` : html`<input type="checkbox" class="sess-cb" checked=${sel.has(d.id)} onChange=${() => toggle(d.id)} aria-label=${t('Seleccionar')} />`}
        <${Icon} name="device" size=24 />
        <div class="st-text"><div class="st-title">${d.name || d.id} ${d.current ? html`<span class="chip on tiny">${t('Este')}</span>` : null}
          ${inactive(d) ? html`<span class="chip tiny">${t('inactiva')}</span>` : null}</div>
          <div class="st-sub"><span class=${cls}><${Icon} name=${ic} size=13 /> ${label}</span> · <span class="mono">${d.id}</span>${d.last_seen_ts ? ` · ${formatShort(d.last_seen_ts)}` : ''}${d.last_seen_ip ? ` · ${d.last_seen_ip}` : ''}</div></div>
        <button class="icon-btn" title=${t('Renombrar')} onClick=${() => rename(d)}><${Icon} name="edit" size=18 /></button>
        ${!d.current && unverified(d) && canVerify ? html`<button class="btn" style="height:30px" onClick=${() => verifyMany([d])}>${t('Verificar')}</button>` : null}
        ${!d.current ? html`<button class="icon-btn" title=${t('Cerrar sesión')} onClick=${() => signOut([d])}><${Icon} name="logout" size=18 /></button>` : null}
      </div>`;
    }) : html`<${Spinner} />`}
    ${others.length ? html`<button class="btn danger" style="width:fit-content" onClick=${async () => {
      if (await confirmDialog({ title: t('Cerrar las demás sesiones'), text: t('Se cerrará la sesión en {n} dispositivos.', { n: others.length }), danger: true, ok: t('Cerrar sesiones') })) {
        await withPassword(cur, 'account.signOutOthers', {});
        load();
      }
    }}><${Icon} name="logout" size=16 /> ${t('Cerrar todas las demás sesiones')}</button>` : null}
  </div>`;
}

// ---------------- Apariencia ----------------
function AppearanceTab() {
  const s = useStore((st) => st.settings);
  const info = useStore((st) => st.info);
  const ui = s.ui || {};
  return html`<div>
    <h3>${t('Apariencia')}</h3>
    <${Setting} title=${t('Tema')}><${Seg} value=${s.theme || 'auto'} options=${[['auto', t('Sistema')], ['light', t('Claro')], ['dark', t('Oscuro')]]} onChange=${(v) => setSetting({ theme: v })} /><//>
    <${Setting} title=${t('Color de acento')}><div class="accent-dots">${ACCENTS.map((c) => html`<button class=${s.accent === c ? 'on' : ''} style=${`background:${c}`} onClick=${() => setSetting({ accent: c })} aria-label=${c}></button>`)}
      <label class="accent-custom" title=${t('Elegir otro color')}><input type="color" value=${s.accent || '#6d5dfc'} onChange=${(e) => setSetting({ accent: e.currentTarget.value })} /></label></div><//>
    <h4>${t('Ícono de la app')}</h4><${AppIconPicker} />
    <h4>${t('Mensajes')}</h4>
    <div class="layout-cards">
      ${[['modern', t('Moderno'), t('Lista limpia, ideal para grupos')], ['bubbles', t('Burbujas'), t('Como en el teléfono')]].map(([k, l, d]) => html`
        <button class=${`layout-card${(ui.layout || 'modern') === k ? ' on' : ''}`} onClick=${() => setUI({ layout: k })}>
          <div class=${`lc-preview ${k}`}><span></span><span></span><span class="me"></span></div><b>${l}</b><span class="small muted">${d}</span></button>`)}
    </div>
    <${Setting} title=${t('Diseño compacto')} sub=${t('Menos espacio entre mensajes')}><${Switch} checked=${!!ui.compact} onChange=${(v) => setUI({ compact: v })} /><//>
    <${Setting} title=${t('Formato de hora')}><${Seg} value=${ui.time24 === false ? '12' : '24'} options=${[['24', '13:45'], ['12', '1:45 p. m.']]} onChange=${(v) => setUI({ time24: v === '24' })} /><//>
    <${Setting} title=${t('Colores en los nombres')}><${Switch} checked=${ui.nameColors !== false} onChange=${(v) => setUI({ nameColors: v })} /><//>
    <h4>${t('Lista de chats')}</h4>
    <${Setting} title=${t('Mostrar vista previa del último mensaje')}><${Switch} checked=${ui.roomPreviews !== false} onChange=${(v) => setUI({ roomPreviews: v })} /><//>
    <${Setting} title=${t('Ordenar por')}><${Seg} value=${ui.roomSort || 'activity'} options=${[['activity', t('Actividad')], ['unread', t('No leídos primero')], ['alpha', t('A-Z')]]} onChange=${(v) => setUI({ roomSort: v })} /><//>
    <${Setting} title=${t('Mostrar avatares')}><${Switch} checked=${ui.listAvatars !== false} onChange=${(v) => setUI({ listAvatars: v })} /><//>
  </div>`;
}

function AccessibilityTab() {
  const s = useStore((st) => st.settings);
  const ui = s.ui || {};
  return html`<div>
    <h3>${t('Accesibilidad')}</h3>
    <div class="field"><label>${t('Tamaño del texto')}</label><${Range} value=${ui.fontSize || 15} min=${12} max=${22} suffix="px"
      onLive=${(v) => document.documentElement.style.setProperty('--fs', `${v}px`)} onChange=${(v) => setUI({ fontSize: v })} /></div>
    <${Setting} title=${t('Reducir movimiento')} sub=${t('Desactiva animaciones y transiciones')}><${Switch} checked=${!!ui.reduceMotion} onChange=${(v) => setUI({ reduceMotion: v })} /><//>
    <div class="field"><label>${t('Saturación de colores')}</label><${Range} value=${ui.saturation ?? 100} max=${100} step=${5}
      onLive=${(v) => document.documentElement.style.setProperty('--saturation', `${v}%`)} onChange=${(v) => setUI({ saturation: v })} /></div>
    <${Setting} title=${t('Subrayar enlaces')}><${Switch} checked=${!!ui.underlineLinks} onChange=${(v) => setUI({ underlineLinks: v })} /><//>
    <${Setting} title=${t('Reproducir GIF y stickers animados automáticamente')}><${Switch} checked=${ui.autoplayGifs !== false} onChange=${(v) => setUI({ autoplayGifs: v })} /><//>
  </div>`;
}

// ---------------- Voz y video (estilo Discord) ----------------
export function useDevices() {
  const [st, setSt] = useState({ loading: true, native: false, devices: [] });
  const load = async () => {
    setSt((x) => ({ ...x, loading: true }));
    try {
      const r = await rpc.call('media.devices');
      if (r.native && r.devices) { setSt({ loading: false, native: true, devices: r.devices }); return; }
    } catch { /* sin motor nativo */ }
    // Navegador: pedir permiso una vez para conocer los nombres reales.
    try {
      if (navigator.mediaDevices && navigator.mediaDevices.getUserMedia) {
        const sm = await navigator.mediaDevices.getUserMedia({ audio: true }).catch(() => null);
        const list = await navigator.mediaDevices.enumerateDevices();
        if (sm) sm.getTracks().forEach((tr) => tr.stop());
        setSt({ loading: false, native: false, devices: list.filter((d) => d.deviceId && d.deviceId !== 'default' && d.label).map((d) => ({ id: d.deviceId, label: d.label, kind: d.kind })) });
        return;
      }
    } catch { /* sin acceso */ }
    setSt({ loading: false, native: false, devices: [] });
  };
  useEffect(() => { load(); }, []);
  return [st, load];
}

function DeviceSelect({ icon, value, devices, onChange, emptyText }) {
  return html`<div><div class="device-select"><${Icon} name=${icon} size=17 />
    <select value=${value || ''} onChange=${(e) => onChange(e.currentTarget.value)}>
      <option value="">${t('Predeterminado del sistema')}</option>
      ${devices.map((d) => html`<option value=${d.id}>${d.label}${d.default ? ` · ${t('predeterminado')}` : ''}</option>`)}
    </select><${Icon} name="chevronDown" size=16 /></div>${!devices.length ? html`<div class="small muted" style="margin-top:4px">${emptyText}</div>` : null}</div>`;
}

export function Range({ value, min = 0, max = 200, step = 1, onChange, onLive, suffix = '%' }) {
  const [v, setV] = useState(value);
  useEffect(() => setV(value), [value]);
  return html`<div class="row range-row"><input type="range" min=${min} max=${max} step=${step} value=${v}
    style=${`--pct:${((v - min) * 100) / (max - min)}%`}
    onInput=${(e) => { const n = Number(e.currentTarget.value); setV(n); onLive && onLive(n); }} onChange=${(e) => onChange(Number(e.currentTarget.value))} />
    <span class="small muted range-val">${v}${suffix}</span></div>`;
}

function LevelMeter({ level, threshold }) {
  // level y threshold en dB (-70..0) → 0..1
  const norm = (db) => Math.max(0, Math.min(1, (db + 70) / 70));
  const n = 48;
  const lit = Math.round(norm(level) * n);
  const th = threshold !== null && threshold !== undefined ? norm(threshold) : null;
  return html`<div class="level-meter" title=${`${Math.round(level)} dB`}>
    ${Array.from({ length: n }, (_, i) => html`<span class=${i < lit ? (th !== null && i / n < th ? 'lit low' : 'lit') : ''}></span>`)}
    ${th !== null ? html`<i class="lm-th" style=${`left:${th * 100}%`}></i>` : null}
  </div>`;
}

function VoiceTab() {
  const s = useStore((st) => st.settings);
  const ui = s.ui || {};
  const [dev, reload] = useDevices();
  const [testing, setTesting] = useState(false);
  const [level, setLevel] = useState(-90);
  const mics = dev.devices.filter((d) => d.kind === 'audioinput');
  const spks = dev.devices.filter((d) => d.kind === 'audiooutput');
  const cams = dev.devices.filter((d) => d.kind === 'videoinput');
  const profile = ui.inputProfile || 'isolation';
  const ns = profile === 'isolation' ? true : profile === 'studio' ? false : s.call_noise_suppression;
  const ec = profile === 'isolation' ? true : profile === 'studio' ? false : s.call_echo_cancel;
  const agc = profile === 'isolation' ? true : profile === 'studio' ? false : ui.agc !== false;
  const noiseLevel = profile === 'isolation' ? 3 : (ui.noiseLevel ?? 2);
  const browserTest = useRef(null);

  const testOpts = () => ({ mic: s.call_mic || '', speaker: s.call_speaker || '', gain: (ui.micVolume ?? 100) / 100,
    out_gain: (ui.speakerVolume ?? 100) / 100, loopback: ui.testLoopback !== false, noise: ns, echo: false, agc, noise_level: noiseLevel });

  const stopTest = () => {
    setTesting(false);
    setLevel(-90);
    if (browserTest.current) { browserTest.current(); browserTest.current = null; }
    rpc.call('media.mictest', { stop: true }).catch(() => {});
  };
  const startTest = async () => {
    setTesting(true);
    if (dev.native) {
      try { await rpc.call('media.mictest', testOpts()); } catch (e) { toast(e.message, 'err'); setTesting(false); }
      return;
    }
    try {
      const sm = await navigator.mediaDevices.getUserMedia({ audio: { deviceId: s.call_mic || undefined, noiseSuppression: ns, echoCancellation: ec, autoGainControl: agc } });
      const actx = new AudioContext();
      const src = actx.createMediaStreamSource(sm);
      const an = actx.createAnalyser();
      an.fftSize = 1024;
      const gain = actx.createGain();
      gain.gain.value = (ui.micVolume ?? 100) / 100;
      src.connect(gain).connect(an);
      if (ui.testLoopback !== false) an.connect(actx.destination);
      const buf = new Float32Array(an.fftSize);
      let alive = true;
      const tick = () => {
        if (!alive) return;
        an.getFloatTimeDomainData(buf);
        let sum = 0;
        for (const x of buf) sum += x * x;
        setLevel(20 * Math.log10(Math.sqrt(sum / buf.length) || 1e-5));
        requestAnimationFrame(tick);
      };
      tick();
      browserTest.current = () => { alive = false; sm.getTracks().forEach((tr) => tr.stop()); actx.close(); };
    } catch { toast(t('No se pudo acceder al micrófono'), 'err'); setTesting(false); }
  };
  useEffect(() => {
    const off = rpc.on('mictest', (m) => setLevel(m.d.rms));
    return () => { off(); stopTest(); };
  }, []);
  // Si cambia algo durante la prueba, la reiniciamos con los valores nuevos.
  useEffect(() => { if (testing && dev.native) rpc.call('media.mictest', testOpts()).catch(() => {}); },
    [s.call_mic, s.call_speaker, ui.micVolume, ui.speakerVolume, profile, ns, agc, noiseLevel, ui.testLoopback]);

  const autoSens = ui.autoSensitivity !== false;
  const sens = ui.sensitivity ?? -50;
  return html`<div class="col voice-tab">
    <h3>${t('Voz')}</h3>
    ${dev.loading ? html`<${Spinner} />` : html`
    <div class="grid2">
      <div class="field"><label>${t('Micrófono')}</label>
        <${DeviceSelect} icon="mic" value=${s.call_mic} devices=${mics} onChange=${(v) => setSetting({ call_mic: v })} emptyText=${t('No se detectaron micrófonos')} /></div>
      <div class="field"><label>${t('Altavoz')}</label>
        <${DeviceSelect} icon="volume" value=${s.call_speaker} devices=${spks} onChange=${(v) => setSetting({ call_speaker: v })} emptyText=${t('No se detectaron salidas de audio')} /></div>
      <div class="field"><label>${t('Volumen del micrófono')}</label><${Range} value=${ui.micVolume ?? 100} onChange=${(v) => setUI({ micVolume: v })} /></div>
      <div class="field"><label>${t('Volumen del altavoz')}</label><${Range} value=${ui.speakerVolume ?? 100} onChange=${(v) => setUI({ speakerVolume: v })} /></div>
    </div>
    <div class="row mic-test">
      <button class=${`btn ${testing ? '' : 'primary'}`} style="min-width:160px" onClick=${() => (testing ? stopTest() : startTest())}>
        ${testing ? t('Detener prueba') : t('Probar micrófono')}</button>
      <${LevelMeter} level=${level} threshold=${autoSens || ui.inputMode === 'ptt' ? null : sens} />
    </div>
    <div class="row small"><label class="row check"><input type="checkbox" checked=${ui.testLoopback !== false} onChange=${(e) => setUI({ testLoopback: e.currentTarget.checked })} /> ${t('Escucharme durante la prueba')}</label>
      <span class="grow"></span><button class="btn ghost" style="height:28px" onClick=${reload}><${Icon} name="refresh" size=15 /> ${t('Actualizar dispositivos')}</button></div>
    `}

    <div class="divider-line"></div>
    <h4>${t('Perfil de entrada')}</h4>
    <div class="radio-list">
      ${[['isolation', t('Aislamiento de voz'), t('Solo tu voz: Hilvan filtra el ruido de fondo, el eco y nivela el volumen.')],
        ['studio', t('Estudio'), t('Audio puro: micrófono abierto sin procesamiento.')],
        ['custom', t('Personalizado'), t('Modo avanzado: todos los controles a tu gusto.')]].map(([k, l, d]) => html`
        <label class=${`radio-item${profile === k ? ' on' : ''}`}><input type="radio" name="profile" checked=${profile === k} onChange=${() => setUI({ inputProfile: k })} />
          <div><div class="st-title">${l}</div><div class="st-sub">${d}</div></div></label>`)}
    </div>
    ${profile === 'custom' ? html`
      <${Setting} title=${t('Supresión de ruido')}><${Switch} checked=${s.call_noise_suppression} onChange=${(v) => setSetting({ call_noise_suppression: v })} /><//>
      ${s.call_noise_suppression ? html`<${Setting} title=${t('Intensidad de la supresión')}><${Seg} value=${noiseLevel} options=${[[0, t('Baja')], [1, t('Media')], [2, t('Alta')], [3, t('Máxima')]]} onChange=${(v) => setUI({ noiseLevel: v })} /><//>` : null}
      <${Setting} title=${t('Cancelación de eco')}><${Switch} checked=${s.call_echo_cancel} onChange=${(v) => setSetting({ call_echo_cancel: v })} /><//>
      <${Setting} title=${t('Control automático de ganancia')}><${Switch} checked=${ui.agc !== false} onChange=${(v) => setUI({ agc: v })} /><//>` : null}

    <h4>${t('Modo de entrada')}</h4>
    <div class="radio-list">
      ${[['vad', t('Detección de voz'), t('Se transmite cuando hablás.')], ['ptt', t('Pulsar para hablar'), t('Se transmite solo mientras mantenés la tecla.')]].map(([k, l, d]) => html`
        <label class=${`radio-item${(ui.inputMode || 'vad') === k ? ' on' : ''}`}><input type="radio" name="imode" checked=${(ui.inputMode || 'vad') === k} onChange=${() => setUI({ inputMode: k })} />
          <div><div class="st-title">${l}</div><div class="st-sub">${d}</div></div></label>`)}
    </div>
    ${ui.inputMode === 'ptt' ? html`
      <${Setting} title=${t('Tecla para hablar')} sub=${t('Funciona mientras la ventana de Hilvan está enfocada.')}><${KeyCapture} value=${ui.pttKey || 'Space'} onChange=${(k) => setUI({ pttKey: k })} /><//>
      <div class="field"><label>${t('Retardo al soltar la tecla')}</label><${Range} value=${ui.pttDelay ?? 200} min=${0} max=${2000} step=${20} suffix=" ms" onChange=${(v) => setUI({ pttDelay: v })} /></div>` : html`
      <${Setting} title=${t('Sensibilidad automática')} sub=${t('Hilvan decide cuándo estás hablando.')}><${Switch} checked=${autoSens} onChange=${(v) => setUI({ autoSensitivity: v })} /><//>
      ${!autoSens ? html`<div class="field"><label>${t('Sensibilidad de entrada')}</label><${Range} value=${sens} min=${-70} max=${0} onChange=${(v) => setUI({ sensitivity: v })} suffix=" dB" />
        <div class="small muted">${t('Lo que esté por debajo de la línea no se transmite. Probá el micrófono para ajustarla.')}</div></div>` : null}`}

    <h4>${t('Atenuación')}</h4>
    <div class="field"><label>${t('Bajar el volumen de las transmisiones cuando alguien habla')}</label><${Range} value=${ui.attenuation ?? 0} max=${100} onChange=${(v) => setUI({ attenuation: v })} /></div>

    <div class="divider-line"></div>
    <h3>${t('Video')}</h3>
    <div class="field"><label>${t('Cámara')}</label><${DeviceSelect} icon="video" value=${s.call_camera} devices=${cams} onChange=${(v) => setSetting({ call_camera: v })} emptyText=${t('No se detectó ninguna cámara')} /></div>
    <${Setting} title=${t('Espejar mi video')} sub=${t('Solo en tu pantalla; los demás te ven normal.')}><${Switch} checked=${ui.mirrorCamera !== false} onChange=${(v) => setUI({ mirrorCamera: v })} /><//>
    <${Setting} title=${t('Calidad de la cámara')}><${Seg} value=${ui.videoQuality || 'auto'} options=${[['auto', t('Auto')], ['480', '480p'], ['720', '720p'], ['1080', '1080p']]} onChange=${(v) => setUI({ videoQuality: v })} /><//>
    <${Setting} title=${t('Desenfocar el fondo')} sub=${t('Cuando tu equipo lo permite')}><${Switch} checked=${!!ui.blurBackground} onChange=${(v) => setUI({ blurBackground: v })} /><//>
    <${Setting} title=${t('Entrar a las llamadas con la cámara apagada')}><${Switch} checked=${ui.joinCameraOff !== false} onChange=${(v) => setUI({ joinCameraOff: v })} /><//>
    <${Setting} title=${t('Entrar a las llamadas con el micrófono silenciado')}><${Switch} checked=${!!ui.joinMuted} onChange=${(v) => setUI({ joinMuted: v })} /><//>

    <div class="divider-line"></div>
    <h3>${t('Avanzado')}</h3>
    <${Setting} title=${t('Modo de baja latencia')} sub=${t('Prioriza la fluidez sobre la calidad máxima')}><${Switch} checked=${s.call_low_latency} onChange=${(v) => setSetting({ call_low_latency: v })} /><//>
    <${Setting} title=${t('Aceleración por hardware')} sub=${t('Usa la GPU para codificar y decodificar video cuando es posible')}><${Switch} checked=${s.call_hw_accel} onChange=${(v) => setSetting({ call_hw_accel: v })} /><//>
    <${Setting} title=${t('Códec de video preferido')}><${Seg} value=${ui.videoCodec || 'auto'} options=${[['auto', t('Auto')], ['vp8', 'VP8'], ['vp9', 'VP9'], ['h264', 'H.264'], ['av1', 'AV1']]} onChange=${(v) => setUI({ videoCodec: v })} /><//>
    <${Setting} title=${t('Prioridad de red para llamadas (QoS)')} sub=${t('Marca los paquetes de voz como prioritarios')}><${Switch} checked=${ui.qos !== false} onChange=${(v) => setUI({ qos: v })} /><//>
    <${Setting} title=${t('Ocultar mi IP en llamadas')} sub=${t('Siempre por el servidor de retransmisión (TURN)')}><${Switch} checked=${s.call_force_relay} onChange=${(v) => setSetting({ call_force_relay: v })} /><//>
    <${Setting} title=${t('Mostrar estadísticas en las llamadas')} sub=${t('Latencia, pérdida de paquetes, códec y bitrate')}><${Switch} checked=${!!ui.callStats} onChange=${(v) => setUI({ callStats: v })} /><//>
    <${Setting} title=${t('Servidor Jitsi para llamadas grupales')}><input class="input" style="width:240px" value=${s.jitsi_domain} onChange=${(e) => setSetting({ jitsi_domain: e.currentTarget.value.trim() })} /><//>
  </div>`;
}

function KeyCapture({ value, onChange }) {
  const [listening, setListening] = useState(false);
  useEffect(() => {
    if (!listening) return;
    const onKey = (e) => { e.preventDefault(); e.stopPropagation(); setListening(false); onChange(e.code); };
    window.addEventListener('keydown', onKey, true);
    return () => window.removeEventListener('keydown', onKey, true);
  }, [listening]);
  return html`<button class=${`btn${listening ? ' primary' : ''}`} onClick=${() => setListening(true)}>${listening ? t('Presioná una tecla…') : html`<kbd class="kbd">${value}</kbd>`}</button>`;
}

// ---------------- Transmisión (compartir pantalla) ----------------
export const STREAM_PRESETS = {
  smooth: { res: '720', fps: 60, bitrate: 6 },
  balanced: { res: '1080', fps: 30, bitrate: 8 },
  text: { res: 'source', fps: 15, bitrate: 6 },
  ultra: { res: '1440', fps: 60, bitrate: 16 },
};

function StreamingTab() {
  const s = useStore((st) => st.settings);
  const ui = s.ui || {};
  const preset = ui.streamPreset || 'balanced';
  const cur = preset === 'custom' ? { res: ui.shareRes || '1080', fps: ui.shareFps || 30, bitrate: ui.shareBitrate || 8 } : STREAM_PRESETS[preset];
  const setCustom = (patch) => setUI({ streamPreset: 'custom', shareRes: cur.res, shareFps: cur.fps, shareBitrate: cur.bitrate, ...patch });
  return html`<div class="col">
    <h3>${t('Transmisión')}</h3>
    <div class="small muted">${t('Cómo se ve y se escucha tu pantalla cuando la compartís en una llamada.')}</div>
    <h4>${t('Calidad')}</h4>
    <div class="preset-grid">
      ${[['smooth', t('Fluidez'), '720p · 60 fps', t('Juegos y videos')], ['balanced', t('Equilibrada'), '1080p · 30 fps', t('Uso general')],
        ['text', t('Nitidez'), t('Original · 15 fps'), t('Código, documentos y texto')], ['ultra', t('Máxima'), '1440p · 60 fps', t('Conexiones muy rápidas')],
        ['custom', t('Personalizada'), `${cur.res === 'source' ? t('Original') : `${cur.res}p`} · ${cur.fps} fps`, t('Elegí cada valor')]].map(([k, l, spec, d]) => html`
        <button class=${`preset-card${preset === k ? ' on' : ''}`} onClick=${() => setUI({ streamPreset: k })}>
          <b>${l}</b><span class="mono small">${spec}</span><span class="small muted">${d}</span></button>`)}
    </div>
    <${Setting} title=${t('Resolución')}><${Seg} value=${cur.res} options=${[['720', '720p'], ['1080', '1080p'], ['1440', '1440p'], ['2160', '4K'], ['source', t('Original')]]} onChange=${(v) => setCustom({ shareRes: v })} /><//>
    <${Setting} title=${t('Cuadros por segundo')}><${Seg} value=${cur.fps} options=${[[5, '5'], [15, '15'], [30, '30'], [60, '60']]} onChange=${(v) => setCustom({ shareFps: v })} /><//>
    <div class="field"><label>${t('Bitrate máximo')}</label><${Range} value=${cur.bitrate} min=${1} max=${40} suffix=" Mbps" onChange=${(v) => setCustom({ shareBitrate: v })} /></div>
    <${Setting} title=${t('Optimizar para')}><${Seg} value=${ui.shareHint || 'auto'} options=${[['auto', t('Auto')], ['motion', t('Movimiento')], ['detail', t('Detalle')]]} onChange=${(v) => setUI({ shareHint: v })} /><//>
    <${Setting} title=${t('Códec de la transmisión')}><${Seg} value=${ui.shareCodec || 'auto'} options=${[['auto', t('Auto')], ['vp9', 'VP9'], ['h264', 'H.264'], ['av1', 'AV1']]} onChange=${(v) => setUI({ shareCodec: v })} /><//>

    <h4>${t('Audio')}</h4>
    <${Setting} title=${t('Compartir el audio de la pantalla o de la aplicación')}><${Switch} checked=${!!ui.shareAudio} onChange=${(v) => setUI({ shareAudio: v })} /><//>
    ${ui.shareAudio ? html`<div class="field"><label>${t('Volumen del audio que transmitís')}</label><${Range} value=${ui.shareAudioVolume ?? 100} onChange=${(v) => setUI({ shareAudioVolume: v })} /></div>` : null}
    <div class="field"><label>${t('Volumen de las transmisiones que mirás')}</label><${Range} value=${ui.streamVolume ?? 100} onChange=${(v) => setUI({ streamVolume: v })} /></div>
    <${Setting} title=${t('Silenciar mi micrófono para la transmisión')} sub=${t('Tu voz sigue en la llamada, pero no se mezcla en el audio de la pantalla')}><${Switch} checked=${ui.shareMuteMic !== false} onChange=${(v) => setUI({ shareMuteMic: v })} /><//>

    <h4>${t('Al compartir')}</h4>
    <${Setting} title=${t('Mostrar el cursor')}><${Switch} checked=${ui.shareCursor !== false} onChange=${(v) => setUI({ shareCursor: v })} /><//>
    <${Setting} title=${t('Mostrar vista previa de mi transmisión a los demás')} sub=${t('Una miniatura antes de que se unan a mirarla')}><${Switch} checked=${ui.streamPreviews !== false} onChange=${(v) => setUI({ streamPreviews: v })} /><//>
    <${Setting} title=${t('Pausar mis notificaciones mientras transmito')} sub=${t('Para que no aparezcan en la pantalla compartida')}><${Switch} checked=${ui.shareDND !== false} onChange=${(v) => setUI({ shareDND: v })} /><//>
    <${Setting} title=${t('Abrir en pantalla completa al ver una transmisión')}><${Switch} checked=${!!ui.streamFullscreen} onChange=${(v) => setUI({ streamFullscreen: v })} /><//>
    <div class="small muted">${t('En Wayland, el sistema te pedirá elegir la pantalla o ventana a compartir (portal seguro de escritorio).')}</div>
  </div>`;
}

// ---------------- Sonidos ----------------
function SoundsTab() {
  const s = useStore((st) => st.settings);
  const ui = s.ui || {};
  const sounds = ui.sounds || {};
  const labels = {
    message: t('Mensaje nuevo'), mention: t('Mención'), ring: t('Llamada entrante'), ringback: t('Llamando…'), join: t('Alguien se une a la llamada'),
    leave: t('Alguien sale de la llamada'), mute: t('Silenciar micrófono'), unmute: t('Activar micrófono'), deafen: t('Ensordecer'), undeafen: t('Dejar de ensordecer'),
    camera_on: t('Cámara encendida'), camera_off: t('Cámara apagada'), screen_on: t('Empezar a transmitir'), screen_off: t('Dejar de transmitir'),
    connected: t('Conectado a la llamada'), disconnected: t('Llamada desconectada'),
  };
  const allOn = SOUND_NAMES.every((n) => sounds[n] !== false);
  return html`<div>
    <h3>${t('Sonidos')}</h3>
    <div class="field"><label>${t('Volumen de los sonidos')}</label><${Range} value=${ui.soundVolume ?? 70} max=${100} onChange=${(v) => setUI({ soundVolume: v })} /></div>
    <${Setting} title=${t('Todos los sonidos')}><${Switch} checked=${allOn} onChange=${(v) => setUI({ sounds: Object.fromEntries(SOUND_NAMES.map((n) => [n, v])) })} /><//>
    ${SOUND_NAMES.map((name) => html`<div class="setting">
      <div class="st-text"><div class="st-title">${labels[name] || name}</div>
        <button class="link-btn" onClick=${() => playSound(name, { force: true })}>${t('Escuchar')}</button></div>
      <${Switch} checked=${sounds[name] !== false} onChange=${(v) => setUI({ sounds: { ...sounds, [name]: v } })} /></div>`)}
  </div>`;
}

// ---------------- Chat ----------------
function ChatTab() {
  const s = useStore((st) => st.settings);
  const ui = s.ui || {};
  return html`<div>
    <h3>${t('Chat')}</h3>
    <h4>${t('Escribir')}</h4>
    <${Setting} title=${t('Ctrl+Enter para enviar')} sub=${t('Enter inserta una nueva línea')}><${Switch} checked=${!!ui.ctrlEnter} onChange=${(v) => setUI({ ctrlEnter: v })} /><//>
    <${Setting} title=${t('Formato Markdown')} sub=${t('**negrita**, _cursiva_, `código`, listas, citas…')}><${Switch} checked=${ui.markdown !== false} onChange=${(v) => setUI({ markdown: v })} /><//>
    <${Setting} title=${t('Corrector ortográfico')}><${Switch} checked=${s.spellcheck} onChange=${(v) => setSetting({ spellcheck: v })} /><//>
    <h4>${t('Mostrar')}</h4>
    <${Setting} title=${t('Emojis grandes cuando el mensaje es solo emojis')}><${Switch} checked=${ui.bigEmoji !== false} onChange=${(v) => setUI({ bigEmoji: v })} /><//>
    <${Setting} title=${t('Vista previa de enlaces')}><${Switch} checked=${s.url_previews} onChange=${(v) => setSetting({ url_previews: v })} /><//>
    <${Setting} title=${t('Cargar imágenes y videos automáticamente')}><${Switch} checked=${s.media_autoload} onChange=${(v) => setSetting({ media_autoload: v })} /><//>
    <${Setting} title=${t('Mostrar cuando otros escriben')}><${Switch} checked=${ui.showTyping !== false} onChange=${(v) => setUI({ showTyping: v })} /><//>
    <${Setting} title=${t('Mostrar confirmaciones de lectura')}><${Switch} checked=${ui.showReceipts !== false} onChange=${(v) => setUI({ showReceipts: v })} /><//>
    <${Setting} title=${t('Mostrar quién entra y sale')}><${Switch} checked=${ui.showJoinLeave !== false} onChange=${(v) => setUI({ showJoinLeave: v })} /><//>
    <${Setting} title=${t('Mostrar cambios de nombre y foto')}><${Switch} checked=${ui.showProfileChanges !== false} onChange=${(v) => setUI({ showProfileChanges: v })} /><//>
    <h4>${t('Archivos')}</h4>
    <${Setting} title=${t('Comprimir fotos grandes antes de enviarlas')} sub=${t('Hasta 2560 px; ahorra datos y tiempo')}><${Switch} checked=${s.compress_images} onChange=${(v) => setSetting({ compress_images: v })} /><//>
  </div>`;
}

// ---------------- Notificaciones ----------------
function NotificationsTab() {
  const s = useStore((st) => st.settings);
  const cur = useStore((st) => st.cur);
  const [rules, setRules] = useState(null);
  const [kw, setKw] = useState('');
  const load = () => rpc.call('notify.rules', { a: cur }).then(setRules).catch(() => setRules({ categories: {}, keywords: [] }));
  useEffect(() => { load(); }, [cur]);
  const cat = (key, on) => rpc.call('notify.category', { a: cur, key, on }).then(load).catch((e) => toast(e.message, 'err'));
  const addKw = () => { if (kw.trim()) rpc.call('notify.keyword', { a: cur, keyword: kw.trim() }).then(() => { setKw(''); load(); }).catch((e) => toast(e.message, 'err')); };
  const c = (rules && rules.categories) || {};
  const cats = [['dm', t('Mensajes directos')], ['group', t('Mensajes en grupos')], ['mentions', t('Menciones a vos')], ['room_mentions', t('Menciones a toda la sala (@room)')],
    ['invites', t('Invitaciones')], ['calls', t('Llamadas')], ['bots', t('Silenciar mensajes de bots')]];
  return html`<div>
    <h3>${t('Notificaciones')}</h3>
    <${Setting} title=${t('Notificaciones de escritorio')}><${Switch} checked=${s.notifications} onChange=${(v) => setSetting({ notifications: v })} /><//>
    <${Setting} title=${t('Mostrar el contenido')} sub=${t('Si lo desactivás, solo verás quién escribió')}><${Switch} checked=${s.notify_show_content} onChange=${(v) => setSetting({ notify_show_content: v })} /><//>
    <${Setting} title=${t('Sonido')}><${Switch} checked=${s.notify_sound} onChange=${(v) => setSetting({ notify_sound: v })} /><//>
    <h4>${t('No molestar')}</h4>
    <${Setting} title=${t('Activar horario de silencio')} sub=${t('Sin notificaciones durante este horario (todos los días)')}><${Switch} checked=${s.dnd_enabled} onChange=${(v) => setSetting({ dnd_enabled: v })} /><//>
    ${s.dnd_enabled ? html`<div class="row" style="padding:6px 0"><span>${t('Desde')}</span><input class="input" type="time" style="width:130px" value=${s.dnd_from} onChange=${(e) => setSetting({ dnd_from: e.currentTarget.value })} />
      <span>${t('hasta')}</span><input class="input" type="time" style="width:130px" value=${s.dnd_to} onChange=${(e) => setSetting({ dnd_to: e.currentTarget.value })} /></div>` : null}
    <h4>${t('Qué te notifica (en todos tus dispositivos)')}</h4>
    ${!rules ? html`<${Spinner} sm />` : html`
      ${cats.filter(([k]) => k in c).map(([k, l]) => html`<${Setting} title=${l}><${Switch} checked=${c[k]} onChange=${(v) => cat(k, v)} /><//>`)}
      <h4>${t('Palabras clave')}</h4>
      <div class="small muted">${t('Recibí una notificación cuando alguien escriba estas palabras.')}</div>
      <div class="row wrap" style="margin:8px 0">${(rules.keywords || []).map((k) => html`<span class="chip on" style="cursor:pointer" title=${t('Quitar')}
        onClick=${() => rpc.call('notify.keyword', { a: cur, keyword: k, remove: true }).then(load)}>${k} ✕</span>`)}</div>
      <div class="row"><input class="input" style="max-width:260px" placeholder=${t('Agregar palabra')} value=${kw} onInput=${(e) => setKw(e.currentTarget.value)} onKeyDown=${(e) => e.key === 'Enter' && addKw()} />
        <button class="btn" onClick=${addKw}>${t('Agregar')}</button></div>`}
    <div class="small muted" style="margin-top:12px">${t('Hilvan evalúa las reglas en tu dispositivo, así que también funcionan en salas cifradas. Cada sala puede ajustarse desde su panel de información.')}</div>
  </div>`;
}

// ---------------- Emojis y stickers ----------------
function EmotesTab() {
  const cur = useStore((s) => s.cur);
  const [packs, setPacks] = useState(null);
  const load = () => rpc.call('emotes.list', { a: cur, room: '' }).then(setPacks);
  useEffect(() => { load(); }, [cur]);
  if (!packs) return html`<${Spinner} />`;
  const mine = packs.find((p) => p.source === 'user') || { images: {}, raw: {} };
  const images = mine.images || {};
  const save = async (nextImages) => {
    const raw = { ...(mine.raw || {}) };
    raw.images = nextImages;
    if (!raw.pack) raw.pack = { display_name: t('Mis emojis') };
    try { await rpc.call('emotes.save', { a: cur, room: '', content: raw }); refreshPacksFor(cur); load(); } catch (e) { toast(e.message, 'err'); }
  };
  const add = (asSticker) => {
    const inp = document.createElement('input');
    inp.type = 'file'; inp.accept = 'image/*'; inp.multiple = true;
    inp.onchange = async () => {
      const next = { ...images };
      for (const f of inp.files) {
        try {
          const r = await upload({ a: cur, mode: 'plain', name: f.name, mime: f.type }, f);
          let code = f.name.replace(/\.[^.]+$/, '').toLowerCase().replace(/[^a-z0-9_-]+/g, '_').slice(0, 32) || 'emoji';
          while (next[code]) code += '_';
          next[code] = { url: r.url, usage: [asSticker ? 'sticker' : 'emoticon'], info: { mimetype: f.type, size: f.size } };
        } catch (e) { toast(e.message, 'err'); }
      }
      save(next);
    };
    inp.click();
  };
  const rename = async (code) => {
    const nc = await confirmDialog({ title: t('Renombrar atajo'), input: 'text', value: code, ok: t('Guardar') });
    if (!nc || nc === code) return;
    const clean = nc.toLowerCase().replace(/[^a-z0-9_-]+/g, '_').slice(0, 32);
    const next = {};
    for (const [k, v] of Object.entries(images)) next[k === code ? clean : k] = v;
    save(next);
  };
  return html`<div class="col">
    <h3>${t('Emojis y stickers')}</h3>
    <div class="muted small">${t('Tus emojis personalizados funcionan en todas tus conversaciones (MSC2545). Escribí :atajo: para usarlos.')}</div>
    <div class="row"><button class="btn primary" onClick=${() => add(false)}><${Icon} name="plus" size=16 /> ${t('Agregar emojis')}</button>
      <button class="btn" onClick=${() => add(true)}><${Icon} name="sticker" size=16 /> ${t('Agregar stickers')}</button></div>
    <div style="display:grid;grid-template-columns:repeat(auto-fill,minmax(160px,1fr));gap:10px">
      ${Object.entries(images).map(([code, img]) => html`<div class="card row" style="padding:8px;gap:8px">
        <img src=${mediaURL(cur, img.url, { w: 64, h: 64, anim: true })} style="width:36px;height:36px;object-fit:contain" />
        <div style="flex:1;min-width:0;cursor:pointer" onClick=${() => rename(code)} title=${t('Renombrar')}><div class="small" style="overflow:hidden;text-overflow:ellipsis">:${code}:</div>
          <div class="small muted">${(img.usage || []).includes('sticker') ? t('Sticker') : t('Emoji')}</div></div>
        <button class="icon-btn sm" onClick=${() => { const n = { ...images }; delete n[code]; save(n); }}><${Icon} name="trash" size=15 /></button></div>`)}
    </div>
    ${packs.filter((p) => p.source === 'room').length ? html`<h4>${t('Paquetes de salas')}</h4>
      ${packs.filter((p) => p.source === 'room').map((p) => html`<${Setting} title=${p.name || p.state_key} sub=${t('{n} imágenes · usar en todas las salas', { n: Object.keys(p.images || {}).length })}>
        <${Switch} checked=${p.global} onChange=${(v) => rpc.call('emotes.global', { a: cur, room: p.room, state_key: p.state_key, on: v }).then(() => { refreshPacksFor(cur); load(); })} /><//>`)}` : null}
  </div>`;
}

// ---------------- Atajos ----------------
function KeysTab() {
  const rows = [
    [t('Buscar o saltar a un chat'), 'Ctrl K'], [t('Chat anterior / siguiente'), 'Alt ↑ / Alt ↓'], [t('Chat no leído anterior / siguiente'), 'Alt Shift ↑ / ↓'],
    [t('Abrir ajustes'), 'Ctrl ,'], [t('Editar tu último mensaje'), '↑'], [t('Cancelar respuesta o edición'), 'Esc'], [t('Cerrar el panel lateral'), 'Esc'],
    [t('Nueva línea'), 'Shift Enter'], [t('Autocompletar mención, emoji o comando'), 'Tab'], [t('Responder a un mensaje'), t('Doble clic')],
    [t('Silenciar / activar micrófono en llamada'), 'Ctrl Shift M'], [t('Ensordecer'), 'Ctrl Shift D'], [t('Colgar'), 'Ctrl Shift E'],
  ];
  return html`<div><h3>${t('Atajos de teclado')}</h3>${rows.map(([a, k]) => html`<div class="setting"><div class="st-text">${a}</div><kbd class="kbd">${k}</kbd></div>`)}</div>`;
}

// Android: conexión en segundo plano, inicio con el teléfono, batería y notificaciones.
function AndroidSystem({ s }) {
  const [battery, setBattery] = useState('');
  const check = () => rpc.call('app.platform', { action: 'batteryStatus' }).then(setBattery).catch(() => {});
  useEffect(() => {
    check();
    const onVis = () => { if (document.visibilityState === 'visible') check(); };
    document.addEventListener('visibilitychange', onVis);
    return () => document.removeEventListener('visibilitychange', onVis);
  }, []);
  return html`
    <${Setting} title=${t('Seguir conectado con la app cerrada')} sub=${t('Recibís mensajes y llamadas al instante. Android muestra un aviso fijo mientras tanto.')}><${Switch} checked=${s.close_to_tray} onChange=${(v) => setSetting({ close_to_tray: v })} /><//>
    <${Setting} title=${t('Conectarse al prender el teléfono')}><${Switch} checked=${s.autostart} onChange=${(v) => setSetting({ autostart: v })} /><//>
    <${Setting} title=${t('Uso de batería')} sub=${battery === 'libre' ? t('Sin restricciones: Android no va a cortar la conexión.')
      : t('Restringido: Android puede cortar la conexión para ahorrar batería y los avisos llegan tarde.')}>
      ${battery === 'libre' ? html`<span class="chip on">${t('Sin restricciones')}</span>`
        : html`<button class="btn" onClick=${() => rpc.call('app.platform', { action: 'battery' })}>${t('Quitar restricción')}</button>`}<//>
    <${Setting} title=${t('Avisos del sistema')} sub=${t('Sonido, vibración y canales de las notificaciones de Android')}>
      <button class="btn" onClick=${() => rpc.call('app.platform', { action: 'notifications' })}>${t('Abrir')}</button><//>`;
}

// Aviso si el escritorio no muestra íconos de la bandeja (GNOME sin AppIndicator).
function TrayWarning() {
  const on = useStore((st) => st.settings.tray_icon);
  const [tray, setTray] = useState(null);
  useEffect(() => {
    if (!on) return undefined;
    const check = () => rpc.call('app.tray').then(setTray).catch(() => {});
    const id = setTimeout(check, 600);
    return () => clearTimeout(id);
  }, [on]);
  if (!on || !tray || !tray.shell || tray.available) return null;
  return html`<div class="card warn small">${t('Tu escritorio no está mostrando íconos de la bandeja. En GNOME instalá y activá la extensión "AppIndicator and KStatusNotifierItem Support"; en KDE, XFCE o Cinnamon ya viene.')}</div>`;
}

// ---------------- Red y sistema ----------------
function SystemTab() {
  const s = useStore((st) => st.settings);
  const info = useStore((st) => st.info);
  const cur = useStore((st) => st.cur);
  const [proxy, setProxy] = useState(s.proxy || '');
  return html`<div>
    <h3>${t('Red y sistema')}</h3>
    <${Setting} title=${t('Idioma')}><div class="device-select" style="width:200px"><select value=${s.language || lang} onChange=${(e) => { setSetting({ language: e.currentTarget.value }).then(() => location.reload()); }}>
      ${LANGS.map(([c, n]) => html`<option value=${c}>${n}</option>`)}</select><${Icon} name="chevronDown" size=16 /></div><//>
    <h4>${t('Conexión')}</h4>
    <div class="field"><label>${t('Proxy')}</label>
      <div class="row"><input class="input" placeholder="socks5://127.0.0.1:9050" value=${proxy} onInput=${(e) => setProxy(e.currentTarget.value)} />
        <button class="btn primary" onClick=${() => setSetting({ proxy: proxy.trim() }).then((r) => r && toast(t('Proxy aplicado')))}>${t('Aplicar')}</button></div>
      <div class="row wrap" style="margin-top:4px"><button class="chip" onClick=${() => setProxy('')}>${t('Sin proxy')}</button>
        <button class="chip" onClick=${() => setProxy('socks5://127.0.0.1:9050')}>Tor</button>
        <button class="chip" onClick=${() => setProxy('socks5://127.0.0.1:9150')}>Tor Browser</button></div>
      <div class="hint">${t('Toda la conexión con tus servidores Matrix pasa por el proxy. Útil para Tor o redes restringidas.')}</div></div>
    <h4>${t('Comportamiento')}</h4>
    ${info && info.os === 'android' ? html`<${AndroidSystem} s=${s} />` : info && info.os !== 'linux' ? html`<${Setting} title=${t('Iniciar con la sesión del sistema')}><${Switch} checked=${s.autostart} onChange=${(v) => setSetting({ autostart: v })} /><//>` : html`<${Setting} title=${t('Ícono en la bandeja del sistema')} sub=${t('Como en Element o Nheko: un clic muestra u oculta Hilvan, y tiene un punto rojo si hay mensajes sin leer.')}><${Switch} checked=${s.tray_icon} onChange=${(v) => setSetting({ tray_icon: v })} /><//>
    <${TrayWarning} />
    <${Setting} title=${t('Al cerrar la ventana, seguir en la bandeja')} sub=${s.tray_icon
      ? t('Hilvan sigue conectado y te avisa de los mensajes. Para salir del todo: clic derecho en el ícono → Salir de Hilvan.')
      : t('Hilvan sigue conectado y te avisa de los mensajes; abrilo de nuevo desde el menú de aplicaciones.')}><${Switch} checked=${s.close_to_tray} onChange=${(v) => setSetting({ close_to_tray: v })} /><//>
    <${Setting} title=${t('Iniciar con la sesión del sistema')}><${Switch} checked=${s.autostart} onChange=${(v) => setSetting({ autostart: v })} /><//>
    ${s.autostart ? html`<${Setting} title=${t('Iniciar minimizado en la bandeja')} sub=${t('Arranca sin abrir la ventana')}><${Switch} checked=${s.start_hidden} onChange=${(v) => setSetting({ start_hidden: v })} /><//>` : null}`}
    ${info && info.os === 'android' ? null : html`<${Setting} title=${t('Guardar credenciales en el llavero del sistema')} sub=${t('Se aplica al reiniciar Hilvan')}><${Switch} checked=${s.use_keyring} onChange=${(v) => setSetting({ use_keyring: v })} /><//>`}
    <h4>${t('Almacenamiento')}</h4>
    <${Setting} title=${t('Tamaño máximo de la caché multimedia')}><${Seg} value=${s.media_cache_mb || 2048} options=${[[512, '512 MB'], [1024, '1 GB'], [2048, '2 GB'], [5120, '5 GB']]} onChange=${(v) => setSetting({ media_cache_mb: v })} /><//>
    <div class="row wrap">
      <button class="btn" onClick=${() => rpc.call('cache.clear').then(() => toast(t('Caché multimedia vaciada')))}><${Icon} name="trash" size=16 /> ${t('Vaciar caché multimedia')}</button>
      <button class="btn" onClick=${async () => {
        if (await confirmDialog({ title: t('Resincronizar cuenta'), text: t('Se borra la copia local de los mensajes y se descarga de nuevo. Tus claves de cifrado se conservan.'), ok: t('Resincronizar') })) {
          rpc.call('account.resync', { a: cur }).then(() => { closeModal(); location.reload(); }).catch((e) => toast(e.message, 'err'));
        }
      }}><${Icon} name="refresh" size=16 /> ${t('Resincronizar esta cuenta')}</button>
    </div>
    <h4>${t('Aplicación')}</h4>
    <button class="btn danger" onClick=${() => rpc.call('app.quit')}><${Icon} name="logout" size=16 /> ${t('Salir de Hilvan')}</button>
    <div class="small muted" style="margin-top:8px">${t('Versión')} ${info ? info.version : ''} · ${info ? info.os : ''}</div>
  </div>`;
}

function LabsTab() {
  const s = useStore((st) => st.settings);
  const ui = s.ui || {};
  return html`<div>
    <h3>${t('Funciones experimentales')}</h3>
    <div class="small muted" style="margin-bottom:8px">${t('Funciones nuevas de Matrix que Hilvan ya soporta. Algunas dependen de tu servidor.')}</div>
    <${Setting} title=${t('Simplified Sliding Sync (MSC4186)')} sub=${t('Inicio y sincronización casi instantáneos si tu servidor lo soporta.')}>
      <${Seg} value=${s.sliding_sync || 'auto'} options=${[['auto', t('Auto')], ['on', t('Sí')], ['off', t('No')]]} onChange=${(v) => setSetting({ sliding_sync: v })} /><//>
    <${Setting} title=${t('Criptografía invisible (MSC4153)')}><${Switch} checked=${s.invisible_crypto} onChange=${(v) => setSetting({ invisible_crypto: v })} /><//>
    <${Setting} title=${t('Compartir historial al invitar (MSC4268)')}><${Switch} checked=${s.share_history_on_invite} onChange=${(v) => setSetting({ share_history_on_invite: v })} /><//>
    <${Setting} title=${t('Modo desarrollador')} sub=${t('Muestra IDs de eventos y el código fuente en los menús')}><${Switch} checked=${!!ui.devMode} onChange=${(v) => setUI({ devMode: v })} /><//>
  </div>`;
}

function AboutTab() {
  const info = useStore((st) => st.info);
  return html`<div class="col" style="align-items:center;text-align:center;padding-top:30px">
    <${Logo} style="width:96px;height:96px" />
    <h3 style="font-size:24px">Hilvan</h3>
    <div class="muted">${t('Versión')} ${info ? info.version : ''}</div>
    <div class="muted" style="max-width:460px">${t('Cliente Matrix minimalista, rápido y seguro. Hecho en Go, con cifrado de extremo a extremo, multicuenta, espacios, hilos, llamadas y mucho más.')}</div>
    <div class="small muted" style="max-width:460px">${t('Sin telemetría: Hilvan no envía datos de uso a nadie. Los informes de problemas solo van a tu organización (si la configuró) o se guardan en tu equipo.')}</div>
    <div class="row wrap" style="justify-content:center">
      <button class="btn" onClick=${reportProblem}><${Icon} name="flag" size=16 /> ${t('Informar un problema')}</button>
      <button class="btn" onClick=${() => rpc.call('app.sbom').then((r) => toast(`${t('Lista de componentes guardada en')} ${r.path}`)).catch((e) => toast(e.message, 'err'))}>
        <${Icon} name="layers" size=16 /> ${t('Lista de componentes (SBOM)')}</button>
      ${policy().support_url ? html`<a class="btn" href=${policy().support_url} target="_blank" rel="noopener noreferrer" data-ext="1"><${Icon} name="globe" size=16 /> ${t('Soporte de tu organización')}</a>` : null}
    </div>
    <${UpdateSection} />
  </div>`;
}

async function reportProblem() {
  const text = await confirmDialog({ title: t('Informar un problema'), text: policy().report_url ? t('Se enviará a tu organización junto con el registro reciente de Hilvan (sin el contenido de tus mensajes).')
    : t('Se guardará en Descargas junto con el registro reciente de Hilvan (sin el contenido de tus mensajes), para que lo compartas con quien quieras.'), input: 'text', placeholder: t('¿Qué pasó?'), ok: t('Enviar') });
  if (!text) return;
  try {
    const r = await rpc.call('app.report', { text, logs: true });
    toast(r.sent ? t('Informe enviado a tu organización') : `${t('Informe guardado en')} ${r.saved}`);
  } catch (e) { toast(e.message, 'err'); }
}
