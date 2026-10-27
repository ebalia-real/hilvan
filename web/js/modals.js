// Diálogos de la aplicación.
import { html, useState, useEffect, useMemo, useRef } from '../vendor/preact-htm.js';
import { useStore, state, update, setView, toast, closeModal, openModal, tlKey } from './store.js';
import { t } from './i18n.js';
import { Icon } from './icons.js';
import { Modal, Spinner, ErrorText, Avatar, Switch, Setting, useAsync, Seg, confirmDialog } from './ui.js';
import { rpc, upload } from './rpc.js';
import { localpart, formatSize, debounce, userServer, mediaURL } from './util.js';
import { roomName } from './sidebar.js';
import { openExternal } from './message.js';
import { policy, off } from './policy.js';

const A = () => state.cur;

// guessUser convierte "ana" o "@ana" en "@ana:mi-servidor"; deja intactos los IDs completos.
export function guessUser(q) {
  const v = q.trim();
  if (/^@[^:\s]+:[^\s]+$/.test(v)) return v;
  if (/^@?[a-z0-9._=\-/+]+$/i.test(v) && v.replace('@', '').length >= 1) return `@${v.replace(/^@/, '').toLowerCase()}:${userServer(A())}`;
  return '';
}

// --- crear sala o espacio ---
export function CreateModal({ space = false, parent = '' }) {
  const [name, setName] = useState('');
  const [topic, setTopic] = useState('');
  const [pub, setPub] = useState(false);
  const [alias, setAlias] = useState('');
  const [enc, setEnc] = useState(true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const [localOnly, setLocalOnly] = useState(false);
  const [retention, setRetention] = useState(policy().retention_default_days || 0);
  const forceEnc = !!policy().force_encryption;
  const noPublic = off('public_rooms');
  const parentRoom = parent && state.acct[A()].rooms[parent];
  const create = async (e) => {
    e && e.preventDefault();
    setBusy(true); setErr(null);
    try {
      const r = await rpc.call('room.create', { a: A(), name, topic, public: pub, alias: pub ? alias : '', encrypted: !pub && (enc || forceEnc), space, parent,
        federate: !localOnly, retention_days: space ? 0 : Number(retention) || 0 });
      closeModal();
      if (space) setView({ space: r.room }); else setView({ room: r.room });
    } catch (e2) { setErr(e2); } finally { setBusy(false); }
  };
  return html`<${Modal} title=${space ? t('Crear espacio') : t('Nueva sala')} onClose=${closeModal}
    foot=${html`<button class="btn" onClick=${closeModal}>${t('Cancelar')}</button><button class="btn primary" disabled=${busy || !name.trim()} onClick=${create}>${busy ? html`<${Spinner} sm />` : t('Crear')}</button>`}>
    <form class="modal-body" onSubmit=${create}>
      ${parentRoom ? html`<div class="small muted">${t('Dentro de {s}', { s: roomName(parentRoom) })}</div>` : null}
      <div class="field"><label>${t('Nombre')}</label><input class="input" autofocus value=${name} onInput=${(e) => setName(e.currentTarget.value)} /></div>
      <div class="field"><label>${t('Tema (opcional)')}</label><input class="input" value=${topic} onInput=${(e) => setTopic(e.currentTarget.value)} /></div>
      ${noPublic ? null : html`<${Setting} title=${t('Pública')} sub=${t('Cualquiera puede encontrarla y unirse.')}><${Switch} checked=${pub} onChange=${setPub} /><//>`}
      ${pub ? html`<div class="field"><label>${t('Dirección')}</label><div class="row"><span class="muted">#</span>
        <input class="input" value=${alias} onInput=${(e) => setAlias(e.currentTarget.value.replace(/[^a-z0-9._=-]/gi, '').toLowerCase())} />
        <span class="muted">:${userServer(A())}</span></div></div>` : null}
      ${!pub && !space ? html`<${Setting} title=${t('Cifrado de extremo a extremo')} sub=${forceEnc ? t('Obligatorio en tu organización.') : t('Recomendado. No se puede desactivar después.')}><${Switch} checked=${enc || forceEnc} disabled=${forceEnc} onChange=${setEnc} /><//>` : null}
      <${Setting} title=${t('Solo usuarios de mi servidor')} sub=${t('Sin federación: nadie de otros servidores puede unirse. No se puede cambiar después.')}><${Switch} checked=${localOnly} onChange=${setLocalOnly} /><//>
      ${space ? null : html`<${Setting} title=${t('Borrar mensajes automáticamente')} sub=${t('Política de retención de la sala')}>
        <select class="input sm" value=${retention} onChange=${(e) => setRetention(e.currentTarget.value)}>
          ${RETENTION_OPTS().map(([v, l]) => html`<option value=${v}>${l}</option>`)}
        </select><//>`}
      <${ErrorText} err=${err} />
    </form><//>`;
}

export const RETENTION_OPTS = () => [[0, t('Nunca')], [1, t('Después de 1 día')], [7, t('Después de 1 semana')], [30, t('Después de 30 días')],
  [90, t('Después de 90 días')], [365, t('Después de 1 año')], [1825, t('Después de 5 años')]];

// --- nuevo chat directo ---
export function NewDMModal() {
  const [q, setQ] = useState('');
  const [res, setRes] = useState([]);
  const [busy, setBusy] = useState(false);
  const search = useMemo(() => debounce(async (v) => {
    if (v.length < 2) { setRes([]); return; }
    try { const r = await rpc.call('user.search', { a: A(), query: v }); setRes(r.results || []); } catch { setRes([]); }
  }, 300), []);
  const open = async (uid) => {
    setBusy(true);
    try { const r = await rpc.call('dm.open', { a: A(), user: uid }); closeModal(); setView({ room: r.room }); } catch (e) { toast(e.message, 'err'); } finally { setBusy(false); }
  };
  const guess = guessUser(q);
  const direct = !!guess;
  return html`<${Modal} title=${t('Nuevo chat directo')} onClose=${closeModal}>
    <div class="modal-body">
      <input class="input" autofocus placeholder=${t('Nombre o @usuario:servidor')} value=${q} onInput=${(e) => { setQ(e.currentTarget.value); search(e.currentTarget.value); }}
        onKeyDown=${(e) => { if (e.key === 'Enter' && direct) open(guess); }} />
      ${direct && !res.find((r) => r.user_id === guess) ? html`<div class="member" onClick=${() => open(guess)}><${Avatar} a=${A()} name=${localpart(guess)} id=${guess} size=${34} /><div class="mn">${guess}</div></div>` : null}
      ${res.map((u) => html`<div class="member" onClick=${() => open(u.user_id)}>
        <${Avatar} a=${A()} mxc=${u.avatar_url} name=${u.display_name || localpart(u.user_id)} id=${u.user_id} size=${34} />
        <div class="mn">${u.display_name || localpart(u.user_id)}<div class="small muted">${u.user_id}</div></div></div>`)}
      ${busy ? html`<${Spinner} />` : null}
      <div class="small muted"><${Icon} name="lock" size=13 /> ${t('Los chats directos se cifran de extremo a extremo.')}</div>
    </div><//>`;
}

// --- explorar / unirse ---
export function ExploreModal() {
  const [server, setServer] = useState('');
  const [q, setQ] = useState('');
  const [data, setData] = useState(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const load = async (more) => {
    setBusy(true); setErr(null);
    try {
      const r = await rpc.call('directory.public', { a: A(), server, query: q, since: more && data ? data.next_batch : '' });
      setData(more && data ? { ...r, chunk: [...data.chunk, ...(r.chunk || [])] } : r);
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  useEffect(() => { load(false); }, []);
  const join = async (r) => {
    try {
      const res = await rpc.call('room.join', { a: A(), target: r.canonical_alias || r.room_id, via: server ? [server] : [] });
      closeModal(); setView({ room: res.room });
    } catch (e) { toast(e.message, 'err'); }
  };
  const isTarget = /^[#!][^:]+:.+$/.test(q.trim()) || q.startsWith('https://matrix.to/');
  return html`<${Modal} title=${t('Explorar salas')} cls="wide" onClose=${closeModal}>
    <div class="modal-body" style="flex:1">
      <form class="row" onSubmit=${(e) => { e.preventDefault(); if (isTarget) openModal('join', { target: q.trim() }); else load(false); }}>
        <input class="input grow" autofocus placeholder=${t('Buscar salas o pegar #sala:servidor')} value=${q} onInput=${(e) => setQ(e.currentTarget.value)} />
        <input class="input" style="width:180px" placeholder=${userServer(A())} value=${server} onInput=${(e) => setServer(e.currentTarget.value)} />
        <button class="btn primary">${isTarget ? t('Unirse') : t('Buscar')}</button>
      </form>
      <${ErrorText} err=${err} />
      ${(data && data.chunk || []).map((r) => html`<div class="member" style="align-items:flex-start">
        <${Avatar} a=${A()} mxc=${r.avatar_url} name=${r.name || r.canonical_alias || r.room_id} id=${r.room_id} size=${40} />
        <div class="mn" style="white-space:normal"><b>${r.name || r.canonical_alias || r.room_id}</b>
          <div class="small muted">${r.canonical_alias || ''} · ${t('{n} miembro|{n} miembros', { n: r.num_joined_members || 0 })}</div>
          ${r.topic ? html`<div class="small" style="max-height:3em;overflow:hidden">${r.topic}</div>` : null}</div>
        <button class="btn" onClick=${() => join(r)}>${t('Unirse')}</button></div>`)}
      ${busy ? html`<div class="tl-loader"><${Spinner} /></div>` : data && data.next_batch ? html`<button class="btn" onClick=${() => load(true)}>${t('Cargar más')}</button>` : null}
      ${data && !(data.chunk || []).length && !busy ? html`<div class="empty">${t('Sin resultados')}</div>` : null}
    </div><//>`;
}

export function JoinModal({ target, via = [] }) {
  const { loading, data, error } = useAsync(() => rpc.call('room.preview', { a: A(), target, via }), [target]);
  const [busy, setBusy] = useState(false);
  const join = async (knock) => {
    setBusy(true);
    try {
      if (knock) { await rpc.call('room.knock', { a: A(), target, via }); toast(t('Solicitud enviada')); closeModal(); return; }
      const r = await rpc.call('room.join', { a: A(), target, via });
      closeModal(); setView({ room: r.room });
    } catch (e) { toast(e.message, 'err'); } finally { setBusy(false); }
  };
  const knock = data && data.join_rule === 'knock';
  return html`<${Modal} title=${t('Unirse a una sala')} onClose=${closeModal}
    foot=${html`<button class="btn" onClick=${closeModal}>${t('Cancelar')}</button>
      <button class="btn primary" disabled=${busy} onClick=${() => join(knock)}>${knock ? t('Pedir acceso') : t('Unirse')}</button>`}>
    <div class="modal-body">
      ${loading ? html`<${Spinner} />` : data ? html`<div class="info-hero" style="padding:6px">
        <${Avatar} a=${A()} mxc=${data.avatar_url} name=${data.name || target} id=${data.room_id || target} size=${64} />
        <h2>${data.name || data.canonical_alias || target}</h2>
        <div class="sub">${t('{n} miembro|{n} miembros', { n: data.num_joined_members || 0 })}</div>
        ${data.topic ? html`<div class="topic">${data.topic}</div>` : null}</div>` : html`<div><b>${target}</b>${error ? html`<div class="small muted">${t('No hay vista previa disponible.')}</div>` : null}</div>`}
    </div><//>`;
}

// --- invitar ---
export function InviteModal({ room }) {
  const [q, setQ] = useState('');
  const [res, setRes] = useState([]);
  const [sel, setSel] = useState([]);
  const [busy, setBusy] = useState(false);
  const search = useMemo(() => debounce(async (v) => {
    if (v.length < 2) { setRes([]); return; }
    try { const r = await rpc.call('user.search', { a: A(), query: v }); setRes(r.results || []); } catch { setRes([]); }
  }, 300), []);
  const add = (uid) => { if (!sel.includes(uid)) setSel([...sel, uid]); setQ(''); setRes([]); };
  const send = async () => {
    setBusy(true);
    let ok = 0;
    for (const u of sel) {
      try { await rpc.call('room.invite', { a: A(), room, user: u }); ok++; } catch (e) { toast(`${u}: ${e.message}`, 'err'); }
    }
    setBusy(false);
    if (ok) { toast(t('{n} invitación enviada|{n} invitaciones enviadas', { n: ok })); closeModal(); }
  };
  const guess = guessUser(q);
  const direct = !!guess;
  return html`<${Modal} title=${t('Invitar')} onClose=${closeModal}
    foot=${html`<button class="btn" onClick=${closeModal}>${t('Cancelar')}</button><button class="btn primary" disabled=${!sel.length || busy} onClick=${send}>${t('Invitar')}</button>`}>
    <div class="modal-body">
      <div class="row wrap">${sel.map((u) => html`<span class="chip on" onClick=${() => setSel(sel.filter((x) => x !== u))}>${u} ✕</span>`)}</div>
      <input class="input" autofocus placeholder=${t('Nombre o @usuario:servidor')} value=${q}
        onInput=${(e) => { setQ(e.currentTarget.value); search(e.currentTarget.value); }} onKeyDown=${(e) => { if (e.key === 'Enter' && direct) add(guess); }} />
      ${direct && !res.find((r) => r.user_id === guess) ? html`<div class="member" onClick=${() => add(guess)}><${Avatar} a=${A()} name=${localpart(guess)} id=${guess} size=${32} /><div class="mn">${guess}</div></div>` : null}
      ${res.map((u) => html`<div class="member" onClick=${() => add(u.user_id)}><${Avatar} a=${A()} mxc=${u.avatar_url} name=${u.display_name || localpart(u.user_id)} id=${u.user_id} size=${32} />
        <div class="mn">${u.display_name || localpart(u.user_id)}<div class="small muted">${u.user_id}</div></div></div>`)}
      ${state.settings.share_history_on_invite ? html`<div class="small muted"><${Icon} name="lock" size=13 /> ${t('Si el historial es visible para miembros, se compartirán las claves cifradas con los dispositivos verificados del invitado.')}</div>` : null}
    </div><//>`;
}

// --- subir archivos ---
export function UploadModal({ files, room, thread, reply }) {
  const [caption, setCaption] = useState('');
  const [progress, setProgress] = useState(null);
  const urls = useMemo(() => files.map((f) => (f.type.startsWith('image/') ? URL.createObjectURL(f) : null)), [files]);
  useEffect(() => () => urls.forEach((u) => u && URL.revokeObjectURL(u)), []);
  // Tope por archivo del servidor (no de Hilvan): se avisa antes de enviar.
  const [limit, setLimit] = useState(0);
  useEffect(() => { rpc.call('media.limits', { a: A() }).then((r) => setLimit(r.upload_size || 0)).catch(() => {}); }, []);
  const tooBig = limit ? files.filter((f) => f.size > limit) : [];
  const send = async () => {
    if (tooBig.length) return;
    setProgress(0);
    let i = 0;
    for (const f of files) {
      try {
        await upload({ a: A(), room, mode: 'send', name: f.name || 'archivo', mime: f.type || '', caption: files.length === 1 ? caption : (i === 0 ? caption : ''), thread: thread || '', reply: reply || '' }, f,
          (p) => setProgress((i + p) / files.length));
      } catch (e) { toast(`${f.name}: ${e.message}`, 'err'); }
      i++;
    }
    closeModal();
  };
  const total = files.reduce((s, f) => s + f.size, 0);
  return html`<${Modal} title=${files.length === 1 ? t('Enviar archivo') : t('Enviar {n} archivos', { n: files.length })} onClose=${closeModal}
    foot=${html`<button class="btn" onClick=${closeModal}>${t('Cancelar')}</button><button class="btn primary" disabled=${progress !== null || tooBig.length > 0} onClick=${send}>${progress !== null ? `${Math.round(progress * 100)}%` : t('Enviar')}</button>`}>
    <div class="modal-body">
      <div class="row wrap">${files.map((f, i) => urls[i] ? html`<img src=${urls[i]} style="max-width:100%;max-height:280px;border-radius:12px" />`
        : html`<div class="file-card"><${Icon} name="file" size=26 /><div><div class="fname">${f.name}</div><div class="fsize">${formatSize(f.size)}</div></div></div>`)}</div>
      <input class="input" placeholder=${t('Agregar un comentario…')} value=${caption} onInput=${(e) => setCaption(e.currentTarget.value)} onKeyDown=${(e) => { if (e.key === 'Enter') send(); }} autofocus />
      <div class="small muted">${formatSize(total)}${state.acct[A()].rooms[room] && state.acct[A()].rooms[room].encrypted ? ` · ${t('se cifrará antes de subir')}` : ''}</div>
      ${tooBig.length ? html`<div class="error-text">${t('{f} pesa {s} y tu servidor acepta hasta {l} por archivo. Es un límite del servidor, no de Hilvan: quien lo administra puede subirlo (max_upload_size en Synapse y el límite del proxy, p. ej. client_max_body_size en nginx).',
        { f: tooBig[0].name, s: formatSize(tooBig[0].size), l: formatSize(limit) })}</div>` : null}
    </div><//>`;
}

// --- encuesta ---
export function PollModal({ room, question: q0 = '' }) {
  const [q, setQ] = useState(q0);
  const [answers, setAnswers] = useState(['', '']);
  const [disclosed, setDisclosed] = useState(true);
  const [multi, setMulti] = useState(false);
  const valid = q.trim() && answers.filter((x) => x.trim()).length >= 2;
  const send = async () => {
    try {
      await rpc.call('poll.create', { a: A(), room, question: q.trim(), answers: answers.map((x) => x.trim()).filter(Boolean), disclosed, max: multi ? answers.length : 1 });
      closeModal();
    } catch (e) { toast(e.message, 'err'); }
  };
  return html`<${Modal} title=${t('Nueva encuesta')} onClose=${closeModal}
    foot=${html`<button class="btn" onClick=${closeModal}>${t('Cancelar')}</button><button class="btn primary" disabled=${!valid} onClick=${send}>${t('Crear')}</button>`}>
    <div class="modal-body">
      <div class="field"><label>${t('Pregunta')}</label><input class="input" autofocus value=${q} onInput=${(e) => setQ(e.currentTarget.value)} /></div>
      ${answers.map((ans, i) => html`<div class="row"><input class="input" placeholder=${t('Opción {n}', { n: i + 1 })} value=${ans}
        onInput=${(e) => { const n = [...answers]; n[i] = e.currentTarget.value; setAnswers(n); }} />
        ${answers.length > 2 ? html`<button class="icon-btn" onClick=${() => setAnswers(answers.filter((_, j) => j !== i))}><${Icon} name="close" /></button>` : null}</div>`)}
      ${answers.length < 20 ? html`<button class="btn ghost" onClick=${() => setAnswers([...answers, ''])}><${Icon} name="plus" size=16 /> ${t('Agregar opción')}</button>` : null}
      <${Setting} title=${t('Mostrar resultados mientras se vota')}><${Switch} checked=${disclosed} onChange=${setDisclosed} /><//>
      <${Setting} title=${t('Permitir varias respuestas')}><${Switch} checked=${multi} onChange=${setMulti} /><//>
    </div><//>`;
}

// --- programar envío ---
export function ScheduleModal({ room, params, onDone }) {
  const pad = (n) => String(n).padStart(2, '0');
  const d = new Date(Date.now() + 3600000);
  const [when, setWhen] = useState(`${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`);
  const save = async () => {
    try {
      await rpc.call('schedule.add', { a: A(), params, at: new Date(when).getTime() });
      toast(t('Mensaje programado'));
      onDone && onDone();
      closeModal();
    } catch (e) { toast(e.message, 'err'); }
  };
  return html`<${Modal} title=${t('Programar mensaje')} onClose=${closeModal}
    foot=${html`<button class="btn" onClick=${closeModal}>${t('Cancelar')}</button><button class="btn primary" onClick=${save}>${t('Programar')}</button>`}>
    <div class="modal-body"><div class="card small">${params.text}</div>
      <input class="input" type="datetime-local" value=${when} onInput=${(e) => setWhen(e.currentTarget.value)} />
      <div class="small muted">${t('Se enviará desde este dispositivo; Hilvan debe estar abierto en ese momento.')}</div></div><//>`;
}

// --- reenviar ---
export function ForwardModal({ item }) {
  const rooms = Object.values(state.acct[A()].rooms).filter((r) => r.membership === 'join' && r.type !== 'm.space').sort((x, y) => y.last_ts - x.last_ts);
  const [q, setQ] = useState('');
  const send = async (room) => {
    const c = { ...item.content };
    delete c['m.relates_to'];
    delete c['m.new_content'];
    try {
      if (c.file && !(state.acct[A()].rooms[room] || {}).encrypted) { toast(t('No se puede reenviar un archivo cifrado a una sala sin cifrar'), 'err'); return; }
      await rpc.call('room.send', { a: A(), room, type: item.type, content: c });
      toast(t('Mensaje reenviado'));
      closeModal();
    } catch (e) { toast(e.message, 'err'); }
  };
  return html`<${Modal} title=${t('Reenviar a…')} onClose=${closeModal}><div class="modal-body">
    <input class="input" autofocus placeholder=${t('Buscar')} value=${q} onInput=${(e) => setQ(e.currentTarget.value)} />
    ${rooms.filter((r) => !q || roomName(r).toLowerCase().includes(q.toLowerCase())).slice(0, 50).map((r) => html`<div class="member" onClick=${() => send(r.id)}>
      <${Avatar} a=${A()} mxc=${r.avatar} name=${roomName(r)} id=${r.dm || r.id} size=${32} /><div class="mn">${roomName(r)}</div></div>`)}
  </div><//>`;
}

export function SourceModal({ item }) {
  return html`<${Modal} title=${t('Código fuente')} cls="wide" onClose=${closeModal}><div class="modal-body">
    <pre class="selectable" style="font:12.5px/1.5 var(--mono);white-space:pre-wrap;word-break:break-all;background:var(--bg-2);padding:12px;border-radius:10px">${JSON.stringify(item, null, 2)}</pre></div><//>`;
}

export function ReportModal({ item }) {
  const [reason, setReason] = useState('');
  const send = async () => {
    try {
      await reportEvent(item, reason);
      toast(t('Denuncia enviada a los administradores del servidor'));
      closeModal();
    } catch (e) { toast(e.message, 'err'); }
  };
  return html`<${Modal} title=${t('Denunciar mensaje')} onClose=${closeModal}
    foot=${html`<button class="btn" onClick=${closeModal}>${t('Cancelar')}</button><button class="btn danger solid" onClick=${send}>${t('Denunciar')}</button>`}>
    <div class="modal-body"><textarea class="textarea" placeholder=${t('Motivo')} value=${reason} onInput=${(e) => setReason(e.currentTarget.value)}></textarea></div><//>`;
}

async function reportEvent(item, reason) {
  return rpc.call('room.report', { a: A(), room: item.room, id: item.id, reason });
}

export function PowerModal({ room, user }) {
  const [level, setLevel] = useState(0);
  useEffect(() => {
    rpc.call('room.members', { a: A(), room }).then((ms) => { const m = ms.find((x) => x.id === user); if (m) setLevel(m.power); });
  }, []);
  const save = async () => {
    try { await rpc.call('room.power', { a: A(), room, user, level: Number(level) }); closeModal(); } catch (e) { toast(e.message, 'err'); }
  };
  return html`<${Modal} title=${t('Rol de {u}', { u: localpart(user) })} onClose=${closeModal}
    foot=${html`<button class="btn" onClick=${closeModal}>${t('Cancelar')}</button><button class="btn primary" onClick=${save}>${t('Guardar')}</button>`}>
    <div class="modal-body">
      <${Seg} value=${Number(level) >= 100 ? 100 : Number(level) >= 50 ? 50 : 0} options=${[[0, t('Miembro')], [50, t('Moderador')], [100, t('Administrador')]]} onChange=${setLevel} />
      <div class="field"><label>${t('Nivel personalizado')}</label><input class="input" type="number" value=${level} onInput=${(e) => setLevel(e.currentTarget.value)} /></div>
    </div><//>`;
}

// --- ajustes de sala ---
export function RoomSettingsModal({ room }) {
  const r = state.acct[A()].rooms[room] || {};
  const [name, setName] = useState(r.name || '');
  const [topic, setTopic] = useState(r.topic || '');
  const [busy, setBusy] = useState(false);
  const canEdit = (r.power || 0) >= 50;
  const set = (type, content) => rpc.call('room.setState', { a: A(), room, type, state_key: '', content });
  const save = async () => {
    setBusy(true);
    try {
      if (name !== (r.name || '')) await set('m.room.name', { name });
      if (topic !== (r.topic || '')) await set('m.room.topic', { topic });
      toast(t('Guardado'));
      closeModal();
    } catch (e) { toast(e.message, 'err'); } finally { setBusy(false); }
  };
  const pickAvatar = () => {
    const inp = document.createElement('input');
    inp.type = 'file'; inp.accept = 'image/*';
    inp.onchange = async () => {
      try {
        const res = await upload({ a: A(), mode: 'plain', name: inp.files[0].name, mime: inp.files[0].type }, inp.files[0]);
        await set('m.room.avatar', { url: res.url });
      } catch (e) { toast(e.message, 'err'); }
    };
    inp.click();
  };
  const enableEnc = async () => {
    if (await confirmDialog({ title: t('Activar cifrado'), text: t('Una vez activado no se puede desactivar. Los bots y puentes que no soporten cifrado dejarán de funcionar.'), ok: t('Activar') })) {
      set('m.room.encryption', { algorithm: 'm.megolm.v1.aes-sha2' }).catch((e) => toast(e.message, 'err'));
    }
  };
  return html`<${Modal} title=${t('Ajustes de la sala')} onClose=${closeModal}
    foot=${canEdit ? html`<button class="btn" onClick=${closeModal}>${t('Cancelar')}</button><button class="btn primary" disabled=${busy} onClick=${save}>${t('Guardar')}</button>` : null}>
    <div class="modal-body">
      <div class="row"><${Avatar} a=${A()} mxc=${r.avatar} name=${r.name || '?'} id=${room} size=${64} />
        ${canEdit ? html`<button class="btn" onClick=${pickAvatar}>${t('Cambiar imagen')}</button>` : null}</div>
      <div class="field"><label>${t('Nombre')}</label><input class="input" disabled=${!canEdit} value=${name} onInput=${(e) => setName(e.currentTarget.value)} /></div>
      <div class="field"><label>${t('Tema')}</label><textarea class="textarea" disabled=${!canEdit} value=${topic} onInput=${(e) => setTopic(e.currentTarget.value)}></textarea></div>
      ${canEdit ? html`
        <div class="field"><label>${t('Quién puede entrar')}</label>
          <${Seg} value=${r.join_rule || 'invite'} options=${[['invite', t('Por invitación')], ['knock', t('Pidiendo acceso')], ['public', t('Cualquiera')]]}
            onChange=${(v) => set('m.room.join_rules', { join_rule: v }).catch((e) => toast(e.message, 'err'))} /></div>
        <div class="field"><label>${t('Quién puede leer el historial')}</label>
          <${Seg} value=${r.history_visibility || 'shared'} options=${[['shared', t('Miembros')], ['invited', t('Desde la invitación')], ['joined', t('Desde que se unen')], ['world_readable', t('Cualquiera')]]}
            onChange=${(v) => set('m.room.history_visibility', { history_visibility: v }).catch((e) => toast(e.message, 'err'))} /></div>
        ${!r.encrypted ? html`<button class="btn" onClick=${enableEnc}><${Icon} name="lock" size=16 /> ${t('Activar cifrado')}</button>` : null}
        <div class="field"><label>${t('Borrar mensajes automáticamente')}</label>
          <select class="input" value=${Math.round((r.retention_ms || 0) / 86400000)}
            onChange=${(e) => { const d = Number(e.currentTarget.value); set('m.room.retention', d ? { max_lifetime: d * 86400000 } : {}).then(() => toast(t('Retención actualizada'))).catch((er) => toast(er.message, 'err')); }}>
            ${RETENTION_OPTS().map(([v, l]) => html`<option value=${v}>${l}</option>`)}
          </select>
          <div class="hint">${t('Los mensajes más viejos se borran de este equipo; el servidor los borra si tiene la retención activada.')}</div></div>
        ${(r.power || 0) >= 100 ? html`<${ServerACLEditor} room=${room} />` : null}` : html`
        ${r.retention_ms ? html`<div class="small muted">${t('Los mensajes se borran automáticamente después de {d} días.', { d: Math.round(r.retention_ms / 86400000) })}</div>` : null}`}
      ${r.local_only ? html`<div class="small muted"><${Icon} name="server" size=14 /> ${t('Sala sin federación: solo usuarios de {s}.', { s: room.split(':')[1] })}</div>` : null}
      ${off('export') ? null : html`<button class="btn" onClick=${() => openModal('export', { room })}><${Icon} name="download" size=16 /> ${t('Exportar chat (auditoría)')}</button>`}
      <div class="small muted selectable">${t('ID de la sala')}: ${room} ${r.version ? `· v${r.version}` : ''}</div>
    </div><//>`;
}

// --- servidores permitidos y bloqueados (m.room.server_acl): control de federación por sala ---
function ServerACLEditor({ room }) {
  const own = room.split(':').slice(1).join(':') || userServer(A());
  const { data } = useAsync(() => rpc.call('room.state', { a: A(), room, types: ['m.room.server_acl'] }), [room]);
  const cur = (data && data['m.room.server_acl'] && data['m.room.server_acl'][0] && data['m.room.server_acl'][0].content) || null;
  const [allow, setAllow] = useState(null);
  const [deny, setDeny] = useState(null);
  const [ip, setIp] = useState(false);
  useEffect(() => {
    if (!data) return;
    setAllow(((cur && cur.allow) || ['*']).join('\n'));
    setDeny(((cur && cur.deny) || []).join('\n'));
    setIp(!!(cur && cur.allow_ip_literals));
  }, [data]);
  if (allow === null) return null;
  const lines = (s) => s.split(/[\n,]/).map((x) => x.trim()).filter(Boolean);
  const save = async () => {
    let al = lines(allow);
    if (!al.length) al = ['*'];
    // Nunca dejar afuera al propio servidor (sería quedarse sin acceso a la sala).
    const okOwn = al.some((p) => new RegExp(`^${p.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*').replace(/\?/g, '.')}$`).test(own));
    if (!okOwn) al.push(own);
    try {
      await rpc.call('room.setState', { a: A(), room, type: 'm.room.server_acl', state_key: '', content: { allow: al, deny: lines(deny), allow_ip_literals: ip } });
      toast(t('Servidores actualizados'));
    } catch (e) { toast(e.message, 'err'); }
  };
  return html`<details class="acl"><summary>${t('Servidores permitidos y bloqueados (federación)')}</summary>
    <div class="field"><label>${t('Permitidos (uno por línea, * = todos)')}</label><textarea class="textarea mono" value=${allow} onInput=${(e) => setAllow(e.currentTarget.value)}></textarea></div>
    <div class="field"><label>${t('Bloqueados')}</label><textarea class="textarea mono" placeholder="spam.example.org&#10;*.malo.net" value=${deny} onInput=${(e) => setDeny(e.currentTarget.value)}></textarea></div>
    <label class="row small"><input type="checkbox" checked=${ip} onChange=${(e) => setIp(e.currentTarget.checked)} /> ${t('Permitir servidores identificados solo por IP')}</label>
    <div class="row"><button class="btn primary sm" onClick=${save}>${t('Aplicar')}</button>
      <span class="small muted">${t('Tu servidor ({s}) siempre queda permitido.', { s: own })}</span></div>
  </details>`;
}

// --- exportar chat (auditoría y cumplimiento) ---
export function ExportModal({ room }) {
  const r = state.acct[A()].rooms[room] || {};
  const [format, setFormat] = useState('html');
  const [range, setRange] = useState('all');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [att, setAtt] = useState(false);
  const prog = useStore((s) => (s.exports || {})[room]);
  const start = () => {
    const f = range === 'custom' && from ? new Date(from).getTime() : range === '30' ? Date.now() - 30 * 86400000 : range === '365' ? Date.now() - 365 * 86400000 : 0;
    const tt = range === 'custom' && to ? new Date(to).getTime() + 86400000 - 1 : 0;
    update((s) => { s.exports = { ...(s.exports || {}), [room]: { state: 'fetching' } }; });
    rpc.call('room.export', { a: A(), room, format, from: f, to: tt, attachments: att }).catch((e) => toast(e.message, 'err'));
  };
  const running = prog && (prog.state === 'fetching' || prog.state === 'writing');
  return html`<${Modal} title=${t('Exportar chat')} onClose=${closeModal}
    foot=${html`<button class="btn" onClick=${closeModal}>${t('Cerrar')}</button>
      <button class="btn primary" disabled=${running} onClick=${start}>${running ? html`<${Spinner} sm />` : html`<${Icon} name="download" size=16 />`} ${t('Exportar')}</button>`}>
    <div class="modal-body">
      <div class="small muted">${t('Incluye la historia completa (también la cifrada), con ediciones, mensajes borrados, hilos y el estado de verificación. Se guarda en Descargas con un archivo .sha256 para comprobar que no fue alterada.')}</div>
      <div class="field"><label>${t('Formato')}</label><${Seg} value=${format} options=${[['html', t('Página (HTML)')], ['json', t('Datos (JSON)')], ['txt', t('Texto')]]} onChange=${setFormat} /></div>
      <div class="field"><label>${t('Período')}</label><${Seg} value=${range} options=${[['all', t('Todo')], ['30', t('30 días')], ['365', t('1 año')], ['custom', t('Elegir')]]} onChange=${setRange} /></div>
      ${range === 'custom' ? html`<div class="row"><input class="input" type="date" value=${from} onInput=${(e) => setFrom(e.currentTarget.value)} /><span>→</span>
        <input class="input" type="date" value=${to} onInput=${(e) => setTo(e.currentTarget.value)} /></div>` : null}
      <label class="row small"><input type="checkbox" checked=${att} onChange=${(e) => setAtt(e.currentTarget.checked)} /> ${t('Incluir los archivos adjuntos (en una carpeta aparte)')}</label>
      ${prog ? html`<div class=${`small ${prog.state === 'error' ? 'error-text' : 'muted'}`}>${
        prog.state === 'fetching' ? t('Trayendo la historia…') + (prog.oldest ? ` (${new Date(prog.oldest).toLocaleDateString()})` : '')
        : prog.state === 'writing' ? t('Escribiendo {n} de {m}…', { n: prog.done, m: prog.total })
        : prog.state === 'done' ? t('Listo: {p} ({n} eventos)', { p: prog.path, n: prog.events })
        : prog.error}</div>` : null}
      <div class="small muted">${roomName(r)}</div>
    </div><//>`;
}

// --- explorar un espacio (jerarquía) ---
export function SpaceModal({ space }) {
  const { loading, data, error } = useAsync(() => rpc.call('space.hierarchy', { a: A(), room: space }), [space]);
  const rooms = state.acct[A()].rooms;
  const join = async (r) => {
    try { const res = await rpc.call('room.join', { a: A(), target: r.room_id, via: (r.children_state && []) || [userServer(space)] }); setView({ room: res.room }); closeModal(); } catch (e) { toast(e.message, 'err'); }
  };
  const sp = rooms[space] || {};
  const canManage = (sp.power || 0) >= 50;
  const addExisting = async () => {
    const id = await confirmDialog({ title: t('Agregar sala existente'), input: 'text', placeholder: '!id:servidor o #alias:servidor', ok: t('Agregar') });
    if (!id) return;
    let target = id.trim();
    const local = Object.values(rooms).find((x) => x.alias === target || x.id === target);
    if (local) target = local.id;
    rpc.call('space.child', { a: A(), space, child: target, add: true }).then(() => toast(t('Sala agregada'))).catch((e) => toast(e.message, 'err'));
  };
  return html`<${Modal} title=${roomName(sp) || t('Espacio')} cls="wide" onClose=${closeModal}>
    <div class="modal-body">
      ${canManage ? html`<div class="row"><button class="btn" onClick=${() => openModal('create', { parent: space })}><${Icon} name="plus" size=16 /> ${t('Nueva sala')}</button>
        <button class="btn" onClick=${addExisting}>${t('Agregar sala existente')}</button></div>` : null}
      ${loading ? html`<${Spinner} />` : null}<${ErrorText} err=${error} />
      ${(data && data.rooms || []).filter((r) => r.room_id !== space).map((r) => {
        const joined = rooms[r.room_id] && rooms[r.room_id].membership === 'join';
        return html`<div class="member" style="align-items:flex-start">
          <${Avatar} a=${A()} mxc=${r.avatar_url} name=${r.name || r.canonical_alias || r.room_id} id=${r.room_id} size=${38} square=${r.room_type === 'm.space'} />
          <div class="mn" style="white-space:normal"><b>${r.name || r.canonical_alias || r.room_id}</b>
            <div class="small muted">${r.room_type === 'm.space' ? t('Espacio') : t('Sala')} · ${t('{n} miembro|{n} miembros', { n: r.num_joined_members || 0 })}</div>
            ${r.topic ? html`<div class="small">${r.topic}</div>` : null}</div>
          ${joined ? html`<button class="btn" onClick=${() => { setView({ room: r.room_id }); closeModal(); }}>${t('Abrir')}</button>` : html`<button class="btn primary" onClick=${() => join(r)}>${t('Unirse')}</button>`}
          ${canManage ? html`<button class="icon-btn" title=${t('Quitar del espacio')} onClick=${() => rpc.call('space.child', { a: A(), space, child: r.room_id, add: false }).then(() => toast(t('Quitada')))}><${Icon} name="close" /></button>` : null}
        </div>`;
      })}
    </div><//>`;
}

// --- puentes a otras redes ---
const BRIDGES = [
  ['whatsapp', 'WhatsApp', 'whatsappbot'], ['telegram', 'Telegram', 'telegrambot'], ['signal', 'Signal', 'signalbot'],
  ['discord', 'Discord', 'discordbot'], ['meta', 'Messenger / Instagram', 'metabot'], ['gmessages', 'Google Messages (SMS/RCS)', 'gmessagesbot'],
  ['slack', 'Slack', 'slackbot'], ['bluesky', 'Bluesky', 'blueskybot'], ['linkedin', 'LinkedIn', 'linkedinbot'], ['irc', 'IRC', 'ircbot'],
];
export function BridgesModal() {
  const server = userServer(A());
  const [custom, setCustom] = useState('');
  const open = async (bot) => {
    try {
      const r = await rpc.call('dm.open', { a: A(), user: bot });
      closeModal(); setView({ room: r.room });
      toast(t('Escribile "help" o "login" al bot para empezar'));
    } catch (e) { toast(e.message, 'err'); }
  };
  return html`<${Modal} title=${t('Conectar otras redes')} onClose=${closeModal}><div class="modal-body">
    <div class="muted small">${t('Matrix se conecta con otras redes mediante puentes. Si tu servidor tiene puentes instalados, abrí un chat con el bot correspondiente e iniciá sesión. Los chats puenteados aparecen con el ícono de la red.')}</div>
    ${BRIDGES.map(([id, name, bot]) => html`<div class="member" onClick=${() => open(`@${bot}:${server}`)}>
      <div class="avatar" style="width:34px;height:34px;background:var(--bg-3);color:var(--text-2)"><${Icon} name="bridge" size=18 /></div>
      <div class="mn">${name}<div class="small muted">@${bot}:${server}</div></div></div>`)}
    <div class="row"><input class="input grow" placeholder="@bot:servidor" value=${custom} onInput=${(e) => setCustom(e.currentTarget.value)} />
      <button class="btn" disabled=${!/^@.+:.+$/.test(custom)} onClick=${() => open(custom.trim())}>${t('Abrir')}</button></div>
  </div><//>`;
}
