// Seguridad: aviso de verificación, clave de recuperación y verificación por emojis.
import { html, useState, useEffect } from '../vendor/preact-htm.js';
import { useStore, state, update, toast, openModal, closeModal, acct } from './store.js';
import { t } from './i18n.js';
import { Icon } from './icons.js';
import { Modal, Spinner, ErrorText, confirmDialog } from './ui.js';
import { rpc } from './rpc.js';
import { copyText, localpart } from './util.js';
import { openExternal, saveTextFile } from './message.js';

const SNOOZE = 'hv.cryptoSnooze';

// Aviso discreto: solo si este dispositivo realmente necesita acción.
export function CryptoBanner() {
  const cur = useStore((s) => s.cur);
  const crypto = useStore((s) => (s.cur && s.acct[s.cur] ? s.acct[s.cur].crypto : null));
  const ready = useStore((s) => (s.cur && s.acct[s.cur] ? s.acct[s.cur].ready : false));
  const [, force] = useState(0);
  if (!crypto || !ready || !crypto.action) return null;
  let snooze = {};
  try { snooze = JSON.parse(localStorage.getItem(SNOOZE) || '{}'); } catch { /* vacío */ }
  const key = `${cur}|${crypto.action}`;
  if (snooze[key] && snooze[key] > Date.now()) return null;
  const later = () => {
    snooze[key] = Date.now() + 7 * 86400000;
    try { localStorage.setItem(SNOOZE, JSON.stringify(snooze)); } catch { /* sin almacenamiento */ }
    force((x) => x + 1);
  };
  const texts = {
    setup: [t('Protegé tus mensajes'), t('Creá una clave de recuperación para no perder tu historial cifrado si cambiás de dispositivo.'), t('Configurar')],
    verify: [t('Verificá este dispositivo'), t('Confirmá que sos vos para leer tus mensajes cifrados y que tus contactos confíen en este dispositivo.'), t('Verificar')],
    backup: [t('Recuperá tu historial'), t('Ingresá tu clave de recuperación para descifrar mensajes anteriores.'), t('Ingresar clave')],
  }[crypto.action];
  if (!texts) return null;
  return html`<div class="list-banner">
    <div class="row"><${Icon} name=${crypto.action === 'setup' ? 'key' : 'shieldWarn'} size=18 /><b>${texts[0]}</b></div>
    <div class="muted">${texts[1]}</div>
    <div class="row"><button class="btn primary" style="height:32px" onClick=${() => openModal('security', { step: crypto.action })}>${texts[2]}</button>
      <button class="btn ghost" style="height:32px" onClick=${later}>${t('Más tarde')}</button></div>
  </div>`;
}

export function SecurityModal({ step: initial = 'verify' }) {
  const a = state.cur;
  const [step, setStep] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const [key, setKey] = useState('');
  const [recovery, setRecovery] = useState('');
  const [saved, setSaved] = useState(false);
  const [password, setPassword] = useState('');
  const [needPass, setNeedPass] = useState(false);
  const [approval, setApproval] = useState('');

  const setup = async () => {
    setBusy(true); setErr(null);
    try {
      const r = await rpc.call('crypto.setup', { a, password });
      if (r.approval_url) { setApproval(r.approval_url); return; }
      setRecovery(r.recovery_key);
      setStep('showkey');
    } catch (e) {
      if (e.message === 'password_required') setNeedPass(true);
      else setErr(e);
    } finally { setBusy(false); }
  };
  const restore = async (e) => {
    e && e.preventDefault();
    setBusy(true); setErr(null);
    try {
      const r = await rpc.call('crypto.restore', { a, key });
      toast(r.imported ? t('Listo: se recuperaron {n} claves', { n: r.imported }) : t('Dispositivo verificado'));
      closeModal();
    } catch (e2) { setErr(e2); } finally { setBusy(false); }
  };
  const verifyOther = async () => {
    setBusy(true); setErr(null);
    try {
      await rpc.call('verify.start', { a, user: a });
      closeModal();
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const resetIdentity = async () => {
    const ok = await confirmDialog({
      title: t('Restablecer identidad'), danger: true, ok: t('Restablecer'),
      text: t('Solo hacelo si perdiste todos tus otros dispositivos y la clave de recuperación. Tus contactos verán que tu identidad cambió y perderás el acceso a mensajes cifrados anteriores que no estén en este dispositivo.'),
    });
    if (ok) setStep('setup');
  };
  const download = async () => {
    try {
      if (await saveTextFile(`hilvan-clave-recuperacion-${localpart(a)}.txt`, `Clave de recuperación de Hilvan para ${a}\n\n${recovery}\n`)) setSaved(true);
    } catch (e) { toast(e.message, 'err'); }
  };

  let body;
  if (step === 'verify') {
    body = html`<div class="modal-body">
      <div class="muted">${t('Elegí cómo confirmar que este dispositivo es tuyo:')}</div>
      <button class="btn lg block" onClick=${verifyOther} disabled=${busy}><${Icon} name="device" /> ${t('Con otro dispositivo donde ya iniciaste sesión')}</button>
      <button class="btn lg block" onClick=${() => setStep('backup')}><${Icon} name="key" /> ${t('Con la clave de recuperación')}</button>
      <button class="btn ghost block" onClick=${resetIdentity}>${t('No tengo acceso a ninguno')}</button>
      <${ErrorText} err=${err} />
    </div>`;
  } else if (step === 'backup') {
    body = html`<form class="modal-body" onSubmit=${restore}>
      <div class="muted">${t('Ingresá la clave de recuperación (o la frase de seguridad) que guardaste al configurar el cifrado.')}</div>
      <input class="input mono" autofocus value=${key} onInput=${(e) => setKey(e.currentTarget.value)} placeholder="EsTc 1234 …" />
      <button class="btn primary lg block" disabled=${busy || !key.trim()}>${busy ? html`<${Spinner} sm />` : t('Continuar')}</button>
      <${ErrorText} err=${err} />
    </form>`;
  } else if (step === 'setup') {
    body = html`<div class="modal-body">
      <div class="muted">${t('Hilvan va a crear tu identidad criptográfica y una clave de recuperación. Con ella vas a poder leer tus mensajes cifrados en cualquier dispositivo.')}</div>
      ${needPass ? html`<div class="field"><label>${t('Tu contraseña')}</label>
        <input class="input" type="password" autofocus value=${password} onInput=${(e) => setPassword(e.currentTarget.value)} />
        <div class="hint">${t('El servidor pide confirmar tu contraseña para este cambio.')}</div></div>` : null}
      ${approval ? html`<div class="card warn col">
        <div>${t('Tu servidor pide aprobar este cambio en el navegador.')}</div>
        <button class="btn" onClick=${() => openExternal(approval)}>${t('Abrir para aprobar')}</button>
        <button class="btn primary" onClick=${() => { setApproval(''); setup(); }}>${t('Ya lo aprobé, continuar')}</button></div>` : html`
      <button class="btn primary lg block" onClick=${setup} disabled=${busy || (needPass && !password)}>${busy ? html`<${Spinner} sm />` : t('Crear clave de recuperación')}</button>`}
      <${ErrorText} err=${err} />
    </div>`;
  } else if (step === 'showkey') {
    body = html`<div class="modal-body">
      <div class="muted">${t('Esta es tu clave de recuperación. Guardala en un lugar seguro (por ejemplo, un gestor de contraseñas). Hilvan no puede recuperarla por vos.')}</div>
      <div class="key-box">${recovery}</div>
      <div class="row"><button class="btn grow" onClick=${() => copyText(recovery).then(() => { setSaved(true); toast(t('Copiada')); })}><${Icon} name="copy" size=16 /> ${t('Copiar')}</button>
        <button class="btn grow" onClick=${download}><${Icon} name="download" size=16 /> ${t('Descargar')}</button></div>
      <button class="btn primary lg block" disabled=${!saved} onClick=${closeModal}>${t('Ya la guardé')}</button>
    </div>`;
  }
  const titles = { verify: t('Verificar este dispositivo'), backup: t('Clave de recuperación'), setup: t('Configurar recuperación'), showkey: t('Guardá tu clave') };
  return html`<${Modal} title=${titles[step]} onClose=${step === 'showkey' && !saved ? null : closeModal}>${body}<//>`;
}

// Verificación de otra persona (en la sala o por DM).
export function VerifyUserModal({ user, room }) {
  const a = state.cur;
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const start = async () => {
    setBusy(true);
    try {
      let r = room;
      const summary = room && state.acct[a].rooms[room];
      if (!summary || summary.dm !== user) r = (await rpc.call('dm.open', { a, user })).room;
      await rpc.call('verify.start', { a, user, room: r });
      closeModal();
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  return html`<${Modal} title=${t('Verificar a {u}', { u: localpart(user) })} onClose=${closeModal}
    foot=${html`<button class="btn" onClick=${closeModal}>${t('Cancelar')}</button><button class="btn primary" disabled=${busy} onClick=${start}>${t('Iniciar verificación')}</button>`}>
    <div class="modal-body"><div class="muted">${t('Van a comparar unos emojis en sus pantallas. Si coinciden, sabrán que la conversación es segura y nadie se hace pasar por ustedes.')}</div><${ErrorText} err=${err} /></div>
  <//>`;
}

// Capa global de verificaciones activas.
export function VerifyLayer() {
  const verify = useStore((s) => s.verify);
  const active = Object.values(verify).filter((v) => !v.dismissed && ['requested', 'ready', 'sas', 'qr_scanned', 'done', 'cancelled'].includes(v.phase))
    .sort((x, y) => (x.incoming === y.incoming ? 0 : x.incoming ? -1 : 1));
  const v = active[0];
  const [waiting, setWaiting] = useState(false);
  useEffect(() => { setWaiting(false); }, [v && v.txn, v && v.phase]);
  useEffect(() => {
    if (v && (v.phase === 'done' || v.phase === 'cancelled')) {
      const tm = setTimeout(() => dismiss(v), v.phase === 'done' ? 3500 : 6000);
      return () => clearTimeout(tm);
    }
  }, [v && v.txn, v && v.phase]);
  if (!v) return null;
  const a = v.account;
  const act = (action) => rpc.call('verify.action', { a, txn: v.txn, action }).catch((e) => toast(e.message, 'err'));
  const who = v.self ? t('tu otro dispositivo') : localpart(v.user);
  let body, foot;
  if (v.phase === 'requested' && v.incoming) {
    body = html`<div class="muted">${v.self ? t('Otro de tus dispositivos ({d}) pide verificarse.', { d: v.device || '?' }) : t('{u} quiere verificar su identidad con vos.', { u: v.user })}</div>`;
    foot = html`<button class="btn" onClick=${() => act('cancel')}>${t('Rechazar')}</button><button class="btn primary" onClick=${() => { setWaiting(true); act('accept'); }}>${t('Aceptar')}</button>`;
  } else if (v.phase === 'requested' || v.phase === 'ready') {
    body = html`<div class="row"><${Spinner} /><span>${v.phase === 'requested' ? t('Esperando que {u} acepte…', { u: who }) : t('Preparando la comparación…')}</span></div>
      ${v.phase === 'ready' && v.incoming ? html`<button class="btn primary" onClick=${() => act('sas')}>${t('Comparar emojis')}</button>` : null}`;
    foot = html`<button class="btn" onClick=${() => act('cancel')}>${t('Cancelar')}</button>`;
  } else if (v.phase === 'sas') {
    body = html`<div class="muted">${t('Confirmá que estos emojis aparecen en el mismo orden en {u}:', { u: who })}</div>
      <div class="sas">${(v.emojis || []).map((e, i) => html`<div class="sas-e"><span class="g">${e}</span><span class="n">${t(`emoji.${(v.emoji_names || [])[i]}`) === `emoji.${(v.emoji_names || [])[i]}` ? (v.emoji_names || [])[i] : t(`emoji.${(v.emoji_names || [])[i]}`)}</span></div>`)}</div>
      ${waiting ? html`<div class="row"><${Spinner} sm /><span class="muted">${t('Esperando a {u}…', { u: who })}</span></div>` : null}`;
    foot = html`<button class="btn danger" onClick=${() => act('mismatch')}>${t('No coinciden')}</button><button class="btn primary" disabled=${waiting} onClick=${() => { setWaiting(true); act('confirm'); }}>${t('Coinciden')}</button>`;
  } else if (v.phase === 'done') {
    body = html`<div class="row shield-ok" style="font-size:16px"><${Icon} name="shieldOk" size=26 /> ${v.self ? t('¡Listo! Este dispositivo está verificado.') : t('¡Listo! {u} está verificado.', { u: v.user })}</div>`;
    foot = html`<button class="btn primary" onClick=${() => dismiss(v)}>${t('Cerrar')}</button>`;
  } else {
    body = html`<div class="error-text">${t('La verificación se canceló.')} <span class="small muted">${v.reason || ''}</span></div>`;
    foot = html`<button class="btn" onClick=${() => dismiss(v)}>${t('Cerrar')}</button>`;
  }
  return html`<${Modal} title=${v.self ? t('Verificar dispositivo') : t('Verificar a {u}', { u: localpart(v.user) })} onClose=${() => { if (!['done', 'cancelled'].includes(v.phase)) act('cancel'); dismiss(v); }} foot=${foot}>
    <div class="modal-body">${body}</div><//>`;
}

function dismiss(v) {
  update((s) => { s.verify = { ...s.verify, [v.txn]: { ...v, dismissed: true } }; });
}
