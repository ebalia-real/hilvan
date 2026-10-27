// Inicio de sesión y registro: OAuth 2.0 (MSC3861), SSO, contraseña.
import { html, useState, useEffect, useRef } from '../vendor/preact-htm.js';
import { rpc } from './rpc.js';
import { t } from './i18n.js';
import { Icon } from './icons.js';
import { Spinner, ErrorText } from './ui.js';
import { state, update, switchAccount } from './store.js';
import { policy, Logo, brandName } from './policy.js';

const LAST_SERVER = 'hv.lastServer';

export function Login({ onCancel }) {
  // Con política de la organización: su servidor por defecto y, si hay uno solo permitido, fijo.
  const allowed = policy().allowed_homeservers || [];
  const lockedServer = allowed.length === 1 ? allowed[0] : '';
  const [server, setServer] = useState(() => lockedServer || policy().default_homeserver || localStorage.getItem(LAST_SERVER) || 'matrix.org');
  const [editing, setEditing] = useState(false);
  const [disc, setDisc] = useState(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const [mode, setMode] = useState('login'); // login | register
  const [user, setUser] = useState('');
  const [pass, setPass] = useState('');
  const [pass2, setPass2] = useState('');
  const [pending, setPending] = useState(null);
  const [uia, setUia] = useState(null);
  const [regToken, setRegToken] = useState('');
  const [terms, setTerms] = useState(false);
  const srvRef = useRef();

  const discover = async (srv = server) => {
    setBusy(true); setErr(null); setDisc(null);
    try {
      const d = await rpc.call('auth.discover', { server: srv });
      setDisc(d);
      setEditing(false);
      localStorage.setItem(LAST_SERVER, srv);
    } catch (e) {
      setErr(e);
      setEditing(true);
    } finally { setBusy(false); }
  };
  useEffect(() => { discover(); }, []);
  useEffect(() => { if (editing) setTimeout(() => srvRef.current && srvRef.current.focus(), 30); }, [editing]);

  // Esperamos el resultado del navegador externo (SSO/OAuth).
  useEffect(() => {
    if (!pending) return;
    const off1 = rpc.on('auth.done', (m) => {
      if (m.d.pending !== pending) return;
      setPending(null);
      if (m.d.account) switchAccount(m.d.account.user_id);
      onCancel && onCancel();
    });
    const off2 = rpc.on('auth.error', (m) => {
      if (m.d.pending !== pending) return;
      setPending(null); setErr(new Error(m.d.error));
    });
    return () => { off1(); off2(); };
  }, [pending]);

  const openExternal = async (res) => {
    setPending(res.pending);
    if (res.opened !== '1') window.open(res.url, '_blank', 'noopener');
  };

  const oauth = async (register = false) => {
    setBusy(true); setErr(null);
    try { openExternal(await rpc.call('auth.oauth', { homeserver: disc.homeserver, register })); } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const sso = async (idp = '') => {
    setBusy(true); setErr(null);
    try { openExternal(await rpc.call('auth.sso', { homeserver: disc.homeserver, idp })); } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const password = async (e) => {
    e && e.preventDefault();
    if (!user || !pass) return;
    setBusy(true); setErr(null);
    try {
      const info = await rpc.call('auth.password', { homeserver: disc.homeserver, user, password: pass });
      switchAccount(info.user_id);
      onCancel && onCancel();
    } catch (e2) { setErr(e2); } finally { setBusy(false); }
  };

  const register = async (e, auth) => {
    e && e.preventDefault();
    if (!auth && pass !== pass2) { setErr(new Error(t('Las contraseñas no coinciden'))); return; }
    setBusy(true); setErr(null);
    try {
      const res = await rpc.call('auth.register', { homeserver: disc.homeserver, username: user, password: pass, auth });
      if (res && res.uia) {
        setUia(res);
        // Etapas automáticas.
        const flows = res.uia.flows || [];
        const done = new Set(res.uia.completed || []);
        const flow = flows.find((f) => f.stages.every((s) => ['m.login.dummy', 'm.login.terms', 'm.login.registration_token'].includes(s))) || flows[0];
        const next = flow && flow.stages.find((s) => !done.has(s));
        if (next === 'm.login.dummy') return register(null, { type: 'm.login.dummy', session: res.uia.session });
        return;
      }
      if (res && res.user_id) { switchAccount(res.user_id); onCancel && onCancel(); }
    } catch (e2) { setErr(e2); } finally { setBusy(false); }
  };

  const uiaNext = () => {
    if (!uia) return null;
    const flows = uia.uia.flows || [];
    const done = new Set(uia.uia.completed || []);
    const flow = flows.find((f) => f.stages.every((s) => ['m.login.dummy', 'm.login.terms', 'm.login.registration_token'].includes(s))) || flows[0];
    return flow && flow.stages.find((s) => !done.has(s));
  };

  const next = uiaNext();
  const policies = uia && uia.uia.params && uia.uia.params['m.login.terms'] ? Object.values(uia.uia.params['m.login.terms'].policies || {}) : [];

  return html`<div class="login"><div class="login-card">
    <div class="login-logo">
      <${Logo} />
      <h1>${brandName()}</h1>${policy().organization ? html`<div class="small muted">${policy().organization}</div>` : null}
      <p>${mode === 'login' ? t('Tus conversaciones, unidas y seguras.') : t('Crear una cuenta nueva')}</p>
    </div>

    ${editing || !disc ? html`<form class="col" onSubmit=${(e) => { e.preventDefault(); discover(server); }}>
      <div class="field"><label>${t('Servidor')}</label>
        <input class="input" ref=${srvRef} value=${server} placeholder="matrix.org" readOnly=${!!lockedServer} onInput=${(e) => setServer(e.currentTarget.value)} autocapitalize="off" spellcheck="false" />
        <div class="hint">${allowed.length ? t('{org} solo permite cuentas de: {s}', { org: policy().organization || t('Tu organización'), s: allowed.join(', ') })
          : t('Escribí el servidor de tu cuenta, por ejemplo matrix.org, o tu ID @usuario:servidor.')}</div></div>
      <button class="btn primary lg block" disabled=${busy}>${busy ? html`<${Spinner} sm />` : t('Continuar')}</button>
    </form>` : html`<div class="server-line">
      <${Icon} name="server" size=18 /><span class="srv">${disc.server_name || disc.homeserver}</span>
      ${lockedServer ? null : html`<button class="btn ghost" onClick=${() => setEditing(true)}>${t('Cambiar')}</button>`}
    </div>`}

    ${disc && !editing && !pending ? html`<div class="col">
      ${disc.oauth ? html`
        <button class="btn primary lg block" onClick=${() => oauth(mode === 'register')} disabled=${busy}>
          ${mode === 'register' ? t('Crear cuenta en {s}', { s: disc.server_name }) : t('Continuar con {s}', { s: disc.server_name })}</button>
        <div class="small muted center">${t('Se abrirá tu navegador para iniciar sesión de forma segura (OAuth 2.0).')}</div>
      ` : null}

      ${!disc.oauth && mode === 'login' && disc.sso && disc.sso.length ? html`<div class="col" style="gap:8px">
        ${disc.sso.map((p) => html`<button class="btn lg block idp-btn" onClick=${() => sso(p.id)} disabled=${busy}>${t('Continuar con {s}', { s: p.name })}</button>`)}
      </div>` : null}
      ${!disc.oauth && mode === 'login' && disc.sso_generic ? html`<button class="btn lg block" onClick=${() => sso('')} disabled=${busy}>${t('Inicio de sesión único (SSO)')}</button>` : null}

      ${!disc.oauth && disc.password && mode === 'login' ? html`
        ${(disc.sso && disc.sso.length) || disc.sso_generic ? html`<div class="divider">${t('o')}</div>` : null}
        <form class="col" onSubmit=${password}>
          <div class="field"><label>${t('Usuario o correo')}</label>
            <input class="input" value=${user} onInput=${(e) => setUser(e.currentTarget.value)} autocomplete="username" autocapitalize="off" spellcheck="false" /></div>
          <div class="field"><label>${t('Contraseña')}</label>
            <input class="input" type="password" value=${pass} onInput=${(e) => setPass(e.currentTarget.value)} autocomplete="current-password" /></div>
          <button class="btn primary lg block" disabled=${busy || !user || !pass}>${busy ? html`<${Spinner} sm />` : t('Iniciar sesión')}</button>
        </form>` : null}

      ${!disc.oauth && mode === 'register' && !uia ? html`<form class="col" onSubmit=${register}>
        <div class="field"><label>${t('Nombre de usuario')}</label>
          <input class="input" value=${user} onInput=${(e) => setUser(e.currentTarget.value.toLowerCase())} autocapitalize="off" spellcheck="false" />
          <div class="hint">${user ? `@${user}:${disc.server_name}` : ''}</div></div>
        <div class="field"><label>${t('Contraseña')}</label>
          <input class="input" type="password" value=${pass} onInput=${(e) => setPass(e.currentTarget.value)} autocomplete="new-password" /></div>
        <div class="field"><label>${t('Repetir contraseña')}</label>
          <input class="input" type="password" value=${pass2} onInput=${(e) => setPass2(e.currentTarget.value)} autocomplete="new-password" /></div>
        <button class="btn primary lg block" disabled=${busy || !user || pass.length < 8}>${busy ? html`<${Spinner} sm />` : t('Crear cuenta')}</button>
        ${pass && pass.length < 8 ? html`<div class="small muted">${t('Usá al menos 8 caracteres.')}</div>` : null}
      </form>` : null}

      ${uia && next ? html`<div class="col">
        ${next === 'm.login.terms' ? html`<div class="col">
          <div class="muted small">${t('Aceptá las condiciones del servidor para continuar:')}</div>
          ${policies.map((p) => { const v = Object.values(p).find((x) => x && x.url) || {}; return html`<a href=${v.url} target="_blank" rel="noopener">${v.name || v.url}</a>`; })}
          <label class="row"><input type="checkbox" checked=${terms} onChange=${(e) => setTerms(e.currentTarget.checked)} /> ${t('Leí y acepto')}</label>
          <button class="btn primary block" disabled=${!terms || busy} onClick=${() => register(null, { type: 'm.login.terms', session: uia.uia.session })}>${t('Continuar')}</button>
        </div>` : next === 'm.login.registration_token' ? html`<div class="col">
          <div class="field"><label>${t('Código de registro')}</label><input class="input" value=${regToken} onInput=${(e) => setRegToken(e.currentTarget.value)} /></div>
          <button class="btn primary block" disabled=${!regToken || busy} onClick=${() => register(null, { type: 'm.login.registration_token', token: regToken, session: uia.uia.session })}>${t('Continuar')}</button>
        </div>` : html`<div class="col">
          <div class="muted small">${t('El servidor pide un paso adicional ({s}). Completalo en el navegador y luego tocá Continuar.', { s: next })}</div>
          <button class="btn block" onClick=${() => window.open(uia.fallback[next], '_blank', 'noopener')}>${t('Abrir en el navegador')}</button>
          <button class="btn primary block" onClick=${() => register(null, { session: uia.uia.session })}>${t('Continuar')}</button>
        </div>`}
      </div>` : null}

      ${!disc.oauth && !disc.password && !(disc.sso && disc.sso.length) && !disc.sso_generic ? html`<div class="error-text">${t('Este servidor no ofrece métodos de inicio de sesión compatibles.')}</div>` : null}

      <div class="center small">
        ${mode === 'login'
          ? (disc.registration !== 'closed' && !policy().disable_registration ? html`<span class="muted">${t('¿No tenés cuenta?')} </span><a href="#" onClick=${(e) => { e.preventDefault(); setMode('register'); setErr(null); }}>${t('Crear una')}</a>` : null)
          : html`<a href="#" onClick=${(e) => { e.preventDefault(); setMode('login'); setUia(null); setErr(null); }}>${t('Ya tengo cuenta')}</a>`}
      </div>
    </div>` : null}

    ${pending ? html`<div class="col center">
      <div class="row" style="justify-content:center"><${Spinner} /> <span>${t('Esperando la confirmación en el navegador…')}</span></div>
      <button class="btn ghost" onClick=${() => setPending(null)}>${t('Cancelar')}</button>
    </div>` : null}

    <${ErrorText} err=${err} />
    ${onCancel && state.accounts.length ? html`<button class="btn ghost" onClick=${onCancel}>${t('Volver')}</button>` : null}
  </div></div>`;
}
