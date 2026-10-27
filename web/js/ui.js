// Componentes compartidos: avatar, modal, menús emergentes, interruptores, diálogos.
import { html, Component, useEffect, useRef, useState, useLayoutEffect } from '../vendor/preact-htm.js';
import { Icon } from './icons.js';
import { mediaURL, colorFor, initials } from './util.js';
import { state, update, useStore } from './store.js';
import { t } from './i18n.js';

export function shallowEqual(a, b) {
  if (a === b) return true;
  const ka = Object.keys(a), kb = Object.keys(b);
  if (ka.length !== kb.length) return false;
  for (const k of ka) if (a[k] !== b[k]) return false;
  return true;
}

// memo: evita re-renderizar si las props no cambiaron (clave para el scroll fluido).
export function memo(Comp, eq = shallowEqual) {
  class Memo extends Component {
    shouldComponentUpdate(next) { return !eq(this.props, next); }
    render(p) { return html`<${Comp} ...${p} />`; }
  }
  Memo.displayName = `memo(${Comp.name})`;
  return Memo;
}

export const Avatar = memo(function Avatar({ a, mxc, name, id, size = 36, square = false, presence }) {
  const [failed, setFailed] = useState(false);
  const src = mxc && !failed ? mediaURL(a, mxc, { w: size * 1.5, h: size * 1.5, crop: true }) : '';
  const style = `width:${size}px;height:${size}px;font-size:${Math.round(size * 0.42)}px;background:${colorFor(id || name || '')}`;
  return html`<div class=${`avatar${square ? ' square' : ''}`} style=${style} title=${name}>
    ${src ? html`<img src=${src} alt="" loading="lazy" decoding="async" onError=${() => setFailed(true)} />` : initials(name || id)}
    ${presence && presence !== 'none' ? html`<span class=${`presence ${presenceClass(presence)}`}></span>` : null}
  </div>`;
});

// presenceClass traduce presencia Matrix o estado propio a una clase visual.
export function presenceClass(p) {
  switch (p) {
    case 'online': return 'online';
    case 'unavailable': case 'away': return 'away';
    case 'dnd': case 'busy': return 'dnd';
    default: return 'offline';
  }
}

// presenceOf combina presencia y mensaje de estado de otro usuario.
export function presenceOf(pr) {
  if (!pr) return null;
  if (pr.status_msg && pr.status_msg.startsWith('⛔')) return 'dnd';
  return pr.presence === 'offline' ? null : pr.presence;
}

export function Spinner({ sm }) { return html`<div class=${`spinner${sm ? ' sm' : ''}`}></div>`; }

export function Switch({ checked, onChange, disabled }) {
  return html`<label class="switch"><input type="checkbox" checked=${!!checked} disabled=${disabled}
    onChange=${(e) => onChange(e.currentTarget.checked)} /><span></span></label>`;
}

export function Seg({ value, options, onChange }) {
  return html`<div class="seg">${options.map(([v, label]) => html`<button class=${v === value ? 'on' : ''}
    onClick=${() => onChange(v)}>${label}</button>`)}</div>`;
}

export function Setting({ title, sub, children }) {
  return html`<div class="setting"><div class="st-text"><div class="st-title">${title}</div>${sub ? html`<div class="st-sub">${sub}</div>` : null}</div>${children}</div>`;
}

export function Modal({ title, onClose, children, foot, cls = '', noHead }) {
  useEffect(() => {
    const onKey = (e) => { if (e.key === 'Escape') { e.stopPropagation(); onClose && onClose(); } };
    window.addEventListener('keydown', onKey, true);
    return () => window.removeEventListener('keydown', onKey, true);
  }, [onClose]);
  return html`<div class="overlay" onMouseDown=${(e) => { if (e.target === e.currentTarget) onClose && onClose(); }}>
    <div class=${`modal ${cls}`} role="dialog" aria-modal="true">
      ${noHead ? null : html`<div class="modal-head"><h2>${title}</h2>
        ${onClose ? html`<button class="icon-btn" onClick=${onClose} aria-label=${t('Cerrar')}><${Icon} name="close" /></button>` : null}</div>`}
      ${children}
      ${foot ? html`<div class="modal-foot">${foot}</div>` : null}
    </div></div>`;
}

// --- menús y popovers globales (fuera de contenedores con "contain") ---
export function openMenu(anchor, items, opts = {}) {
  const rect = anchor && anchor.getBoundingClientRect ? anchor.getBoundingClientRect()
    : anchor && anchor.clientX !== undefined ? { left: anchor.clientX, right: anchor.clientX, top: anchor.clientY, bottom: anchor.clientY } : anchor;
  update((s) => { s.popover = { rect, items, render: opts.render, align: opts.align || 'start', id: Math.random() }; });
}
export function closeMenu() { update((s) => { s.popover = null; }); }

export function PopoverLayer() {
  const pop = useStore((s) => s.popover);
  const ref = useRef();
  const [pos, setPos] = useState(null);
  useLayoutEffect(() => {
    if (!pop || !ref.current) { setPos(null); return; }
    const r = pop.rect;
    const el = ref.current;
    const w = el.offsetWidth, h = el.offsetHeight;
    let left = pop.align === 'end' ? r.right - w : r.left;
    let top = r.bottom + 6;
    if (top + h > window.innerHeight - 8) top = Math.max(8, r.top - h - 6);
    left = Math.max(8, Math.min(left, window.innerWidth - w - 8));
    setPos({ left, top });
  }, [pop && pop.id]);
  useEffect(() => {
    if (!pop) return;
    const onDown = (e) => { if (ref.current && !ref.current.contains(e.target)) closeMenu(); };
    const onKey = (e) => { if (e.key === 'Escape') { e.stopPropagation(); closeMenu(); } };
    setTimeout(() => window.addEventListener('mousedown', onDown), 0);
    window.addEventListener('keydown', onKey, true);
    window.addEventListener('resize', closeMenu);
    return () => {
      window.removeEventListener('mousedown', onDown);
      window.removeEventListener('keydown', onKey, true);
      window.removeEventListener('resize', closeMenu);
    };
  }, [pop && pop.id]);
  if (!pop) return null;
  const style = pos ? `left:${pos.left}px;top:${pos.top}px` : 'left:-9999px;top:0';
  return html`<div class=${`popover ${pop.render ? 'custom' : ''}`} ref=${ref} style=${style}>
    ${pop.render ? pop.render() : pop.items.filter(Boolean).map((it) => it === '-' ? html`<div class="rail-sep" style="width:auto;margin:4px 8px"></div>` : html`
      <button class=${`menu-item${it.danger ? ' danger' : ''}`} onClick=${() => { closeMenu(); it.onClick && it.onClick(); }}>
        ${it.icon ? html`<${Icon} name=${it.icon} size=18 />` : null}<span class="mi-text">${it.label}</span>
        ${it.check ? html`<${Icon} name="check" size=16 />` : null}
      </button>`)}
  </div>`;
}

// --- diálogos de confirmación / texto (basados en promesas) ---
export function confirmDialog({ title, text, ok = t('Aceptar'), danger = false, input, placeholder, value = '' }) {
  return new Promise((resolve) => {
    update((s) => { s.dialog = { title, text, ok, danger, input, placeholder, value, resolve, id: Math.random() }; });
  });
}

export function DialogLayer() {
  const d = useStore((s) => s.dialog);
  const [val, setVal] = useState('');
  const inputRef = useRef();
  useEffect(() => {
    if (d) {
      setVal(d.value || '');
      setTimeout(() => inputRef.current && inputRef.current.focus(), 30);
    }
  }, [d && d.id]);
  if (!d) return null;
  const done = (v) => { update((s) => { s.dialog = null; }); d.resolve(v); };
  return html`<${Modal} title=${d.title} onClose=${() => done(d.input ? null : false)}
    foot=${html`<button class="btn" onClick=${() => done(d.input ? null : false)}>${t('Cancelar')}</button>
      <button class=${`btn ${d.danger ? 'danger solid' : 'primary'}`} onClick=${() => done(d.input ? val : true)}>${d.ok}</button>`}>
    <div class="modal-body">
      ${d.text ? html`<div class="muted">${d.text}</div>` : null}
      ${d.input ? html`<input class="input" ref=${inputRef} type=${d.input === 'password' ? 'password' : 'text'} value=${val}
        placeholder=${d.placeholder || ''} onInput=${(e) => setVal(e.currentTarget.value)}
        onKeyDown=${(e) => { if (e.key === 'Enter') done(val); }} />` : null}
    </div>
  <//>`;
}

export function Toasts() {
  const toasts = useStore((s) => s.toasts);
  return html`<div class="toasts">${toasts.map((x) => html`<div key=${x.id} class=${`toast ${x.kind}`}>${x.text}</div>`)}</div>`;
}

export function useAsync(fn, deps) {
  const [st, setSt] = useState({ loading: true, data: null, error: null });
  useEffect(() => {
    let alive = true;
    setSt((s) => ({ ...s, loading: true, error: null }));
    fn().then((data) => alive && setSt({ loading: false, data, error: null }))
      .catch((error) => alive && setSt({ loading: false, data: null, error }));
    return () => { alive = false; };
  }, deps);
  return st;
}

export function Empty({ icon = 'sparkles', text }) {
  return html`<div class="empty"><${Icon} name=${icon} size=28 /><div style="margin-top:8px">${text}</div></div>`;
}

export function ErrorText({ err }) {
  if (!err) return null;
  return html`<div class="error-text">${err.message || String(err)}</div>`;
}

export function nowState() { return state; }
