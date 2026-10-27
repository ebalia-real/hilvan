// Traducciones: es (base), en, pt, fr, de, it.
export const LANGS = [
  ['es', 'Español'], ['en', 'English'], ['pt', 'Português'], ['fr', 'Français'], ['de', 'Deutsch'], ['it', 'Italiano'],
];

let dict = {};
let base = {};
export let lang = 'es';

export async function initI18n(pref) {
  const wanted = pref || (navigator.language || 'es').slice(0, 2).toLowerCase();
  lang = LANGS.some(([c]) => c === wanted) ? wanted : 'en';
  base = await fetch('/i18n/es.json').then((r) => r.json()).catch(() => ({}));
  dict = lang === 'es' ? base : await fetch(`/i18n/${lang}.json`).then((r) => r.json()).catch(() => ({}));
  document.documentElement.lang = lang;
}

// t('clave', {n: 3}) — interpola {var} y admite plurales "uno|varios".
export function t(key, vars) {
  let s = dict[key] ?? base[key] ?? key;
  if (vars) {
    if (s.includes('|') && typeof vars.n === 'number') {
      const [one, many] = s.split('|');
      s = vars.n === 1 ? one : many;
    }
    s = s.replace(/\{(\w+)\}/g, (_, k) => (vars[k] ?? `{${k}}`));
  }
  return s;
}

export function locale() {
  return { es: 'es', en: 'en', pt: 'pt', fr: 'fr', de: 'de', it: 'it' }[lang] || 'es';
}
