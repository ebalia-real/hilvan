// Panel de administración del servidor (API de administración de Synapse): lo que Synapse Pro/Element
// ofrecen en su consola, en Hilvan: usuarios, salas, incorporación masiva, denuncias, moderación,
// avisos al servidor, avisos de seguridad y estado de la federación.
import { html, useState, useEffect } from '../vendor/preact-htm.js';
import { state, toast } from './store.js';
import { t } from './i18n.js';
import { Icon } from './icons.js';
import { Spinner, Seg, confirmDialog, Avatar, ErrorText, Switch } from './ui.js';
import { rpc } from './rpc.js';
import { formatShort, localpart, userServer } from './util.js';

const req = (method, path, query, body) => rpc.call('admin.request', { a: state.cur, method, path, query, body });
const enc = encodeURIComponent;

export function AdminPanel() {
  const [tab, setTab] = useState('users');
  const tabs = [['users', t('Usuarios')], ['rooms', t('Salas')], ['onboard', t('Incorporación')], ['tokens', t('Registro')],
    ['reports', t('Denuncias')], ['notices', t('Avisos')], ['security', t('Seguridad')], ['server', t('Servidor')]];
  const Body = { users: Users, rooms: Rooms, onboard: Onboarding, tokens: Tokens, reports: Reports, notices: Notices, security: Security, server: Server }[tab];
  return html`<div class="col">
    <h3>${t('Administración del servidor')}</h3>
    <div class="admin-tabs">${tabs.map(([k, l]) => html`<button class=${tab === k ? 'on' : ''} onClick=${() => setTab(k)}>${l}</button>`)}</div>
    <${Body} />
  </div>`;
}

// ---------------- usuarios y moderación ----------------
function Users() {
  const [q, setQ] = useState('');
  const [data, setData] = useState(null);
  const [err, setErr] = useState(null);
  const load = () => req('GET', '/_synapse/admin/v2/users', { limit: '100', name: q, guests: 'false', deactivated: 'true' }).then(setData).catch(setErr);
  useEffect(() => { load(); }, []);
  const server = userServer(state.cur);
  const create = async () => {
    const name = await confirmDialog({ title: t('Nuevo usuario'), input: 'text', placeholder: t('nombre de usuario'), ok: t('Siguiente') });
    if (!name) return;
    const pass = await confirmDialog({ title: t('Contraseña para @{u}', { u: name }), input: 'password', ok: t('Crear') });
    if (!pass) return;
    try { await req('PUT', `/_synapse/admin/v2/users/${enc(`@${name}:${server}`)}`, null, { password: pass, displayname: name }); toast(t('Usuario creado')); load(); } catch (e) { toast(e.message, 'err'); }
  };
  const act = async (u, what) => {
    try {
      if (what === 'deactivate') {
        if (!(await confirmDialog({ title: t('Desactivar cuenta'), text: t('¿Desactivar {u}? No se puede deshacer.', { u: u.name }), danger: true, ok: t('Desactivar') }))) return;
        const erase = await confirmDialog({ title: t('¿Borrar también sus datos?'), text: t('Derecho al olvido (RGPD): oculta sus mensajes a quienes se unan después y borra su perfil.'), ok: t('Sí, borrar sus datos') });
        await req('POST', `/_synapse/admin/v1/deactivate/${enc(u.name)}`, null, { erase: !!erase });
      } else if (what === 'password') {
        const p = await confirmDialog({ title: t('Nueva contraseña para {u}', { u: u.name }), input: 'password', ok: t('Cambiar') });
        if (!p) return;
        await req('POST', `/_synapse/admin/v1/reset_password/${enc(u.name)}`, null, { new_password: p, logout_devices: true });
      } else if (what === 'admin') {
        await req('PUT', `/_synapse/admin/v1/users/${enc(u.name)}/admin`, null, { admin: !u.admin });
      } else if (what === 'shadow') {
        if (u.shadow_banned) await req('DELETE', `/_synapse/admin/v1/users/${enc(u.name)}/shadow_ban`);
        else {
          if (!(await confirmDialog({ title: t('Silenciar en secreto'), text: t('{u} podrá seguir escribiendo, pero nadie verá sus mensajes ni invitaciones (contra spam).', { u: u.name }), ok: t('Silenciar'), danger: true }))) return;
          await req('POST', `/_synapse/admin/v1/users/${enc(u.name)}/shadow_ban`);
        }
      } else if (what === 'redact') {
        const reason = await confirmDialog({ title: t('Borrar todos sus mensajes'), text: t('Se borran para todos los mensajes de {u} en todas las salas del servidor.', { u: u.name }), input: 'text', placeholder: t('Motivo (opcional)'), ok: t('Borrar'), danger: true });
        if (reason === null) return;
        const r = await req('POST', `/_synapse/admin/v1/user/${enc(u.name)}/redact`, null, { rooms: [], reason });
        toast(t('Borrado en curso ({id})', { id: r.redact_id || '' }));
        return;
      } else if (what === 'media') {
        if (!(await confirmDialog({ title: t('Poner en cuarentena sus archivos'), text: t('Los archivos que subió {u} dejan de poder descargarse.', { u: u.name }), ok: t('Cuarentena'), danger: true }))) return;
        const r = await req('POST', `/_synapse/admin/v1/user/${enc(u.name)}/media/quarantine`);
        toast(t('{n} archivos en cuarentena', { n: r.num_quarantined || 0 }));
        return;
      } else if (what === 'logout') {
        const devs = await req('GET', `/_synapse/admin/v2/users/${enc(u.name)}/devices`);
        await req('POST', `/_synapse/admin/v2/users/${enc(u.name)}/delete_devices`, null, { devices: (devs.devices || []).map((d) => d.device_id) });
      }
      toast(t('Listo'));
      load();
    } catch (e) { toast(e.message, 'err'); }
  };
  return html`<div class="col">
    <div class="row"><input class="input grow" placeholder=${t('Buscar usuarios')} value=${q} onInput=${(e) => setQ(e.currentTarget.value)} onKeyDown=${(e) => e.key === 'Enter' && load()} />
      <button class="btn" onClick=${load}><${Icon} name="search" size=16 /></button><button class="btn primary" onClick=${create}><${Icon} name="userPlus" size=16 /> ${t('Crear')}</button></div>
    <${ErrorText} err=${err} />
    ${!data ? html`<${Spinner} />` : html`<div class="small muted">${t('{n} usuarios', { n: data.total })}</div>
      ${(data.users || []).map((u) => html`<div class="setting">
        <${Avatar} a=${state.cur} mxc=${u.avatar_url} name=${u.displayname || localpart(u.name)} id=${u.name} size=${34} />
        <div class="st-text"><div class="st-title">${u.displayname || localpart(u.name)} ${u.admin ? html`<span class="chip on tiny">admin</span>` : null}
          ${u.deactivated ? html`<span class="chip tiny">${t('desactivado')}</span>` : null}${u.shadow_banned ? html`<span class="chip tiny warn">${t('silenciado')}</span>` : null}</div>
          <div class="st-sub selectable">${u.name} · ${t('creado')} ${formatShort(u.creation_ts * (u.creation_ts < 1e12 ? 1000 : 1))}</div></div>
        ${!u.deactivated ? html`<button class="icon-btn" title=${t('Cambiar contraseña')} onClick=${() => act(u, 'password')}><${Icon} name="key" size=18 /></button>
          <button class="icon-btn" title=${u.admin ? t('Quitar admin') : t('Hacer admin')} onClick=${() => act(u, 'admin')}><${Icon} name="crown" size=18 /></button>
          ${u.name !== state.cur ? html`<button class="icon-btn" title=${t('Moderación')} onClick=${(e) => import('./ui.js').then((m) => m.openMenu(e.currentTarget, [
            { icon: 'eyeOff', label: u.shadow_banned ? t('Quitar silencio secreto') : t('Silenciar en secreto (spam)'), onClick: () => act(u, 'shadow') },
            { icon: 'trash', label: t('Borrar todos sus mensajes'), danger: true, onClick: () => act(u, 'redact') },
            { icon: 'image', label: t('Cuarentena de sus archivos'), danger: true, onClick: () => act(u, 'media') },
            { icon: 'logout', label: t('Cerrar todas sus sesiones'), onClick: () => act(u, 'logout') },
            '-',
            { icon: 'ban', label: t('Desactivar cuenta'), danger: true, onClick: () => act(u, 'deactivate') },
          ]))}><${Icon} name="moreV" size=18 /></button>` : null}` : null}
      </div>`)}`}
  </div>`;
}

// ---------------- salas ----------------
function Rooms() {
  const [q, setQ] = useState('');
  const [data, setData] = useState(null);
  const [err, setErr] = useState(null);
  const load = () => req('GET', '/_synapse/admin/v1/rooms', { limit: '100', search_term: q, order_by: 'joined_members' }).then(setData).catch(setErr);
  useEffect(() => { load(); }, []);
  const del = async (r) => {
    if (!(await confirmDialog({ title: t('Eliminar sala'), text: t('Se expulsará a todos y se borrará del servidor: {r}', { r: r.name || r.room_id }), danger: true, ok: t('Eliminar') }))) return;
    const block = await confirmDialog({ title: t('¿Bloquearla también?'), text: t('Así nadie de tu servidor puede volver a entrar.'), ok: t('Bloquear') });
    try { await req('DELETE', `/_synapse/admin/v2/rooms/${enc(r.room_id)}`, null, { block: !!block, purge: true }); toast(t('Eliminación en curso')); load(); } catch (e) { toast(e.message, 'err'); }
  };
  const block = async (r) => {
    try {
      const st = await req('GET', `/_synapse/admin/v1/rooms/${enc(r.room_id)}/block`);
      await req('PUT', `/_synapse/admin/v1/rooms/${enc(r.room_id)}/block`, null, { block: !st.block });
      toast(st.block ? t('Sala desbloqueada') : t('Sala bloqueada'));
    } catch (e) { toast(e.message, 'err'); }
  };
  const takeover = async (r) => {
    try { await req('POST', `/_synapse/admin/v1/rooms/${enc(r.room_id)}/make_room_admin`, null, { user_id: state.cur }); toast(t('Ahora sos administrador de la sala')); } catch (e) { toast(e.message, 'err'); }
  };
  return html`<div class="col">
    <div class="row"><input class="input grow" placeholder=${t('Buscar salas')} value=${q} onInput=${(e) => setQ(e.currentTarget.value)} onKeyDown=${(e) => e.key === 'Enter' && load()} />
      <button class="btn" onClick=${load}><${Icon} name="search" size=16 /></button></div>
    <${ErrorText} err=${err} />
    ${!data ? html`<${Spinner} />` : (data.rooms || []).map((r) => html`<div class="setting">
      <div class="st-text"><div class="st-title">${r.name || r.canonical_alias || r.room_id}</div>
        <div class="st-sub selectable">${r.room_id} · ${t('{n} miembro|{n} miembros', { n: r.joined_members })}${r.encryption ? ' · 🔒' : ''}${r.public ? ` · ${t('pública')}` : ''}${r.federatable === false ? ` · ${t('sin federación')}` : ''}</div></div>
      <button class="icon-btn" title=${t('Hacerme administrador')} onClick=${() => takeover(r)}><${Icon} name="crown" size=18 /></button>
      <button class="icon-btn" title=${t('Bloquear o desbloquear')} onClick=${() => block(r)}><${Icon} name="ban" size=18 /></button>
      <button class="icon-btn" title=${t('Eliminar')} onClick=${() => del(r)}><${Icon} name="trash" size=18 /></button></div>`)}
  </div>`;
}

// ---------------- incorporación masiva ----------------
function Onboarding() {
  const server = userServer(state.cur);
  const [users, setUsers] = useState('');
  const [rooms, setRooms] = useState('');
  const [csv, setCsv] = useState('');
  const [log, setLog] = useState([]);
  const [busy, setBusy] = useState(false);
  const myRooms = Object.values((state.acct[state.cur] || {}).rooms || {}).filter((r) => r.membership === 'join' && (r.type === 'm.space' || !r.dm));
  const add = (line) => setLog((l) => [...l, line]);
  const lines = (s) => s.split(/[\n,;]/).map((x) => x.trim()).filter(Boolean);
  const fullUser = (u) => (u.startsWith('@') ? u : `@${u}:${server}`);
  const joinAll = async () => {
    setBusy(true); setLog([]);
    const us = lines(users).map(fullUser);
    for (const room of lines(rooms)) {
      // Si es un espacio, también sus salas hijas.
      let targets = [room];
      try {
        const h = await rpc.call('space.hierarchy', { a: state.cur, room });
        if (h && h.rooms && h.rooms.length > 1) targets = h.rooms.map((r) => r.room_id);
      } catch { /* no es un espacio */ }
      for (const target of targets) {
        for (const u of us) {
          try { await req('POST', `/_synapse/admin/v1/join/${enc(target)}`, null, { user_id: u }); add(`✓ ${u} → ${target}`); } catch (e) { add(`✗ ${u} → ${target}: ${e.message}`); }
        }
      }
    }
    setBusy(false);
  };
  const createAll = async () => {
    setBusy(true); setLog([]);
    for (const row of csv.split('\n').map((l) => l.trim()).filter(Boolean)) {
      const [name, pass, display, email] = row.split(',').map((x) => (x || '').trim());
      if (!name || !pass) { add(`✗ ${row}: ${t('falta usuario o contraseña')}`); continue; }
      const body = { password: pass, displayname: display || name };
      if (email) body.threepids = [{ medium: 'email', address: email }];
      try { await req('PUT', `/_synapse/admin/v2/users/${enc(fullUser(name))}`, null, body); add(`✓ ${fullUser(name)}`); } catch (e) { add(`✗ ${name}: ${e.message}`); }
    }
    setBusy(false);
  };
  return html`<div class="col">
    <h4>${t('Agregar personas a salas y espacios')}</h4>
    <div class="small muted">${t('Los espacios incluyen todas sus salas. Para salas privadas, tu cuenta de administrador tiene que estar adentro.')}</div>
    <div class="field"><label>${t('Usuarios (uno por línea; @usuario:servidor o solo el nombre)')}</label><textarea class="textarea mono" value=${users} onInput=${(e) => setUsers(e.currentTarget.value)}></textarea></div>
    <div class="field"><label>${t('Salas o espacios (ID o #dirección)')}</label><textarea class="textarea mono" value=${rooms} onInput=${(e) => setRooms(e.currentTarget.value)}></textarea>
      <div class="row wrap small">${myRooms.slice(0, 12).map((r) => html`<button class="chip" onClick=${() => setRooms((x) => (x ? `${x}\n` : '') + r.id)}>${r.type === 'm.space' ? '◈ ' : '# '}${r.name || r.id}</button>`)}</div></div>
    <button class="btn primary" disabled=${busy || !users.trim() || !rooms.trim()} onClick=${joinAll}>${busy ? html`<${Spinner} sm />` : t('Agregar')}</button>
    <h4>${t('Crear cuentas en lote')}</h4>
    <div class="field"><label>${t('Una por línea: usuario,contraseña,nombre visible,correo (los dos últimos son opcionales)')}</label>
      <textarea class="textarea mono" placeholder="ana,ClaveSegura123,Ana Pérez,ana@empresa.com" value=${csv} onInput=${(e) => setCsv(e.currentTarget.value)}></textarea></div>
    <button class="btn" disabled=${busy || !csv.trim()} onClick=${createAll}>${t('Crear cuentas')}</button>
    ${log.length ? html`<pre class="admin-log">${log.join('\n')}</pre>` : null}
  </div>`;
}

// ---------------- registro ----------------
function Tokens() {
  const [data, setData] = useState(null);
  const [err, setErr] = useState(null);
  const load = () => req('GET', '/_synapse/admin/v1/registration_tokens').then(setData).catch(setErr);
  useEffect(() => { load(); }, []);
  const create = async () => {
    const uses = await confirmDialog({ title: t('Nuevo código de registro'), text: t('¿Cuántos usos? (vacío = ilimitado)'), input: 'text', ok: t('Crear') });
    if (uses === null) return;
    try {
      const body = {};
      if (uses.trim()) body.uses_allowed = parseInt(uses, 10);
      const r = await req('POST', '/_synapse/admin/v1/registration_tokens/new', null, body);
      toast(t('Código: {c}', { c: r.token }));
      load();
    } catch (e) { toast(e.message, 'err'); }
  };
  return html`<div class="col">
    <div class="row"><span class="grow muted small">${t('Códigos para que otras personas puedan registrarse cuando el registro está cerrado.')}</span>
      <button class="btn primary" onClick=${create}><${Icon} name="plus" size=16 /> ${t('Crear código')}</button></div>
    <${ErrorText} err=${err} />
    ${!data ? html`<${Spinner} />` : (data.registration_tokens || []).map((tk) => html`<div class="setting">
      <div class="st-text"><div class="st-title mono selectable">${tk.token}</div>
        <div class="st-sub">${t('Usos')}: ${tk.completed}${tk.uses_allowed ? ` / ${tk.uses_allowed}` : ''}${tk.expiry_time ? ` · ${t('vence')} ${formatShort(tk.expiry_time)}` : ''}</div></div>
      <button class="icon-btn" onClick=${() => req('DELETE', `/_synapse/admin/v1/registration_tokens/${enc(tk.token)}`).then(load)}><${Icon} name="trash" size=18 /></button></div>`)}
  </div>`;
}

// ---------------- denuncias ----------------
function Reports() {
  const [data, setData] = useState(null);
  const [err, setErr] = useState(null);
  const [open, setOpen] = useState(null);
  const load = () => req('GET', '/_synapse/admin/v1/event_reports', { limit: '100', dir: 'b' }).then(setData).catch(setErr);
  useEffect(() => { load(); }, []);
  const detail = async (r) => {
    if (open && open.id === r.id) { setOpen(null); return; }
    try { setOpen(await req('GET', `/_synapse/admin/v1/event_reports/${r.id}`)); } catch (e) { toast(e.message, 'err'); }
  };
  const dismiss = async (r) => { try { await req('DELETE', `/_synapse/admin/v1/event_reports/${r.id}`); load(); } catch (e) { toast(e.message, 'err'); } };
  const removeMsg = async (r) => {
    if (!(await confirmDialog({ title: t('Borrar el mensaje denunciado'), text: t('Se borra para todos.'), ok: t('Borrar'), danger: true }))) return;
    try { await rpc.call('room.redact', { a: state.cur, room: r.room_id, id: r.event_id, reason: t('Denunciado: {r}', { r: r.reason || '' }) }); toast(t('Mensaje borrado')); dismiss(r); } catch (e) { toast(e.message, 'err'); }
  };
  return html`<div class="col"><${ErrorText} err=${err} />
    ${!data ? html`<${Spinner} />` : !(data.event_reports || []).length ? html`<div class="empty">${t('No hay denuncias')}</div>` : data.event_reports.map((r) => html`<div class="card col" style="gap:4px">
      <div class="row"><b class="grow">${r.reason || t('(sin motivo)')}</b><span class="small muted">${formatShort(r.received_ts)}</span></div>
      <div class="small muted selectable">${t('Denunció')} ${r.user_id} · ${t('autor')} ${r.sender} · ${r.name || r.room_id}</div>
      ${open && open.id === r.id ? html`<pre class="admin-log">${JSON.stringify((open.event_json || {}).content || open.event_json, null, 2)}</pre>` : null}
      <div class="row wrap"><button class="btn sm" onClick=${() => detail(r)}>${open && open.id === r.id ? t('Ocultar') : t('Ver mensaje')}</button>
        <button class="btn sm danger" onClick=${() => removeMsg(r)}>${t('Borrar mensaje')}</button>
        <button class="btn sm" onClick=${() => dismiss(r)}>${t('Descartar')}</button></div>
    </div>`)}
  </div>`;
}

// ---------------- avisos del servidor ----------------
function Notices() {
  const [to, setTo] = useState('');
  const [all, setAll] = useState(false);
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const send = async () => {
    setBusy(true);
    try {
      let targets = to.split(/[\n,]/).map((x) => x.trim()).filter(Boolean);
      if (all) {
        const r = await req('GET', '/_synapse/admin/v2/users', { limit: '10000', guests: 'false', deactivated: 'false' });
        targets = (r.users || []).map((u) => u.name);
      }
      let n = 0;
      for (const u of targets) {
        await req('POST', '/_synapse/admin/v1/send_server_notice', null, { user_id: u, content: { msgtype: 'm.text', body: text } });
        n++;
      }
      toast(t('Aviso enviado a {n} personas', { n }));
      setText('');
    } catch (e) { toast(e.message.includes('notices') ? t('El servidor no tiene configurados los avisos del servidor (server_notices).') : e.message, 'err'); }
    setBusy(false);
  };
  return html`<div class="col">
    <div class="small muted">${t('Mensajes oficiales que llegan como "Aviso del servidor": mantenimiento, políticas, incidentes.')}</div>
    <${Setting2} title=${t('Enviar a todas las personas del servidor')}><${Switch} checked=${all} onChange=${setAll} /><//>
    ${all ? null : html`<div class="field"><label>${t('Para (uno por línea)')}</label><textarea class="textarea mono" value=${to} onInput=${(e) => setTo(e.currentTarget.value)}></textarea></div>`}
    <div class="field"><label>${t('Mensaje')}</label><textarea class="textarea" value=${text} onInput=${(e) => setText(e.currentTarget.value)}></textarea></div>
    <button class="btn primary" disabled=${busy || !text.trim() || (!all && !to.trim())} onClick=${send}>${busy ? html`<${Spinner} sm />` : t('Enviar aviso')}</button>
  </div>`;
}

function Setting2({ title, children }) {
  return html`<div class="setting"><div class="st-text"><div class="st-title">${title}</div></div>${children}</div>`;
}

// ---------------- avisos de seguridad ----------------
function Security() {
  const [data, setData] = useState(null);
  const [err, setErr] = useState(null);
  useEffect(() => { rpc.call('admin.advisories', { a: state.cur }).then(setData).catch(setErr); }, []);
  if (err) return html`<${ErrorText} err=${err} />`;
  if (!data) return html`<${Spinner} />`;
  const advs = data.advisories || [];
  return html`<div class="col">
    <div class=${`card col ${data.outdated || advs.length ? 'warn' : 'ok'}`} style="gap:4px">
      <div class="row"><${Icon} name=${data.outdated || advs.length ? 'shieldWarn' : 'shieldOk'} />
        <b>${data.software || 'Synapse'} ${data.server_version}</b></div>
      ${data.offline ? html`<div class="small muted">${t('Sin conexión a internet: no se pudo comparar con la última versión publicada.')}</div>`
        : html`<div class="small">${data.outdated ? t('Hay una versión más nueva: {v}. Las versiones nuevas suelen traer correcciones de seguridad.', { v: data.latest }) : t('Es la última versión publicada ({v}).', { v: data.latest })}</div>`}
    </div>
    ${advs.length ? html`<h4>${t('Avisos de seguridad que afectan a tu versión')}</h4>
      ${advs.map((a) => html`<div class="setting"><div class="st-text"><div class="st-title"><span class=${`chip tiny ${a.severity === 'critical' || a.severity === 'high' ? 'danger' : 'warn'}`}>${a.severity}</span> ${a.summary}</div>
        <div class="st-sub">${a.id} · ${t('corregido en')} ${a.patched || '?'}</div></div>
        ${a.url ? html`<a class="icon-btn" href=${a.url} target="_blank" rel="noopener noreferrer" data-ext="1"><${Icon} name="globe" size=18 /></a>` : null}</div>`)}`
      : data.offline ? null : html`<div class="small muted">${t('No hay avisos de seguridad publicados que afecten a esta versión.')}</div>`}
  </div>`;
}

// ---------------- servidor y federación ----------------
function Server() {
  const [data, setData] = useState(null);
  const [fed, setFed] = useState(null);
  const [media, setMedia] = useState(null);
  const [err, setErr] = useState(null);
  const loadFed = () => req('GET', '/_synapse/admin/v1/federation/destinations', { limit: '100', order_by: 'failure_ts', dir: 'b' }).then(setFed).catch(() => setFed({ destinations: [] }));
  useEffect(() => {
    req('GET', '/_synapse/admin/v1/server_version').then(setData).catch(setErr);
    loadFed();
    req('GET', '/_synapse/admin/v1/statistics/users/media', { limit: '10', order_by: 'media_length', dir: 'b' }).then(setMedia).catch(() => {});
  }, []);
  const reset = async (d) => { try { await req('POST', `/_synapse/admin/v1/federation/destinations/${enc(d.destination)}/reset_connection`); toast(t('Conexión reiniciada')); loadFed(); } catch (e) { toast(e.message, 'err'); } };
  const mb = (n) => `${(n / 1048576).toFixed(1)} MB`;
  return html`<div class="col"><${ErrorText} err=${err} />
    ${data ? html`<div class="card col" style="gap:4px"><div><b>Synapse</b> ${data.server_version}</div>${data.python_version ? html`<div class="small muted">Python ${data.python_version}</div>` : null}
      <div class="small muted">${userServer(state.cur)}</div></div>` : html`<${Spinner} />`}
    <h4>${t('Federación')}</h4>
    ${!fed ? html`<${Spinner} sm />` : !(fed.destinations || []).length ? html`<div class="small muted">${t('Sin servidores remotos conocidos.')}</div>`
      : fed.destinations.map((d) => html`<div class="setting"><div class="st-text"><div class="st-title">${d.destination}</div>
        <div class="st-sub">${d.failure_ts ? t('Falla desde {t}', { t: formatShort(d.failure_ts) }) : t('Funciona')}${d.retry_interval ? ` · ${t('reintento cada {s} s', { s: Math.round(d.retry_interval / 1000) })}` : ''}</div></div>
        ${d.failure_ts ? html`<button class="btn sm" onClick=${() => reset(d)}>${t('Reintentar ya')}</button>` : null}</div>`)}
    ${media && (media.users || []).length ? html`<h4>${t('Quiénes ocupan más espacio con archivos')}</h4>
      ${media.users.map((u) => html`<div class="setting"><div class="st-text"><div class="st-title">${u.displayname || u.user_id}</div><div class="st-sub">${u.user_id}</div></div>
        <span class="small mono">${mb(u.media_length)} · ${u.media_count}</span></div>`)}` : null}
  </div>`;
}
