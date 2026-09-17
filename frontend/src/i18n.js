// Minimal i18n: flat dot-keyed JSON dictionaries per language (see
// src/locales/*.json), loaded eagerly via Vite's import.meta.glob so
// there's no async flash-of-untranslated-text on startup — every
// dictionary is already in memory by the time init() in main.js calls
// setLocale(). Deliberately not a general-purpose i18n library: Lupinus's
// whole UI is template strings, not a component tree, so all this needs
// to do is look up a key and substitute {placeholders}.
//
// Pluralization is simplified to two CLDR categories, "one" and "other"
// (English's own rule) — good enough for a straightforward "N items"
// label, but grammatically imperfect for languages with richer plural
// systems (Arabic's six categories, Russian/Polish's three, etc.). Full
// CLDR plural-rule support is a reasonable follow-up, not implemented
// here to keep the translation files themselves approachable to extend.
const modules = import.meta.glob('./locales/*.json', { eager: true })

const dictionaries = {}
for (const path in modules) {
  const match = path.match(/([a-zA-Z-]+)\.json$/)
  if (!match) continue
  dictionaries[match[1]] = modules[path].default || modules[path]
}

// Right-to-left languages, for document.dir — Lupinus's CSS itself isn't
// bidi-aware (no mirrored layout), so this gets correct text direction
// and paragraph flow but not a fully mirrored UI; a reasonable first step
// rather than a complete RTL layout pass.
const RTL_LOCALES = new Set(['ar', 'he', 'fa', 'ur'])

export const AVAILABLE_LOCALES = [
  { code: 'en', name: 'English' },
  { code: 'tr', name: 'Türkçe' },
  { code: 'es', name: 'Español' },
  { code: 'fr', name: 'Français' },
  { code: 'de', name: 'Deutsch' },
  { code: 'it', name: 'Italiano' },
  { code: 'pt', name: 'Português' },
  { code: 'ru', name: 'Русский' },
  { code: 'ar', name: 'العربية' },
  { code: 'zh', name: '中文' },
  { code: 'ja', name: '日本語' },
  { code: 'ko', name: '한국어' },
  { code: 'hi', name: 'हिन्दी' },
  { code: 'nl', name: 'Nederlands' },
  { code: 'pl', name: 'Polski' },
  { code: 'uk', name: 'Українська' },
  { code: 'el', name: 'Ελληνικά' },
  { code: 'sv', name: 'Svenska' },
  { code: 'no', name: 'Norsk' },
  { code: 'da', name: 'Dansk' },
  { code: 'fi', name: 'Suomi' },
  { code: 'cs', name: 'Čeština' },
  { code: 'ro', name: 'Română' },
  { code: 'hu', name: 'Magyar' },
  { code: 'bg', name: 'Български' },
  { code: 'hr', name: 'Hrvatski' },
  { code: 'sr', name: 'Српски' },
  { code: 'sk', name: 'Slovenčina' },
  { code: 'sl', name: 'Slovenščina' },
  { code: 'he', name: 'עברית' },
  { code: 'fa', name: 'فارسی' },
  { code: 'vi', name: 'Tiếng Việt' },
  { code: 'th', name: 'ไทย' },
  { code: 'id', name: 'Bahasa Indonesia' },
  { code: 'ms', name: 'Bahasa Melayu' },
  { code: 'sw', name: 'Kiswahili' },
  { code: 'ur', name: 'اردو' },
  { code: 'bn', name: 'বাংলা' },
].filter((l) => dictionaries[l.code]) // only list what actually has a file

let currentLocale = 'en'

export function currentLocaleCode() {
  return currentLocale
}

export function setLocale(code) {
  currentLocale = dictionaries[code] ? code : 'en'
  document.documentElement.lang = currentLocale
  document.documentElement.dir = RTL_LOCALES.has(currentLocale) ? 'rtl' : 'ltr'
}

// detectLocale reads the webview's UI language (navigator.language
// reflects the OS locale in both WebView2 and WebKitGTK) and maps it to
// a supported dictionary, ignoring region subtags ("pt-BR" -> "pt").
// Falls back to English if nothing matches.
export function detectLocale() {
  const nav = (navigator.language || 'en').toLowerCase()
  const short = nav.split('-')[0]
  return dictionaries[short] ? short : 'en'
}

export function t(key, params) {
  const dict = dictionaries[currentLocale] || dictionaries.en
  let str = dict[key] ?? dictionaries.en[key] ?? key
  if (params) {
    for (const k in params) {
      str = str.replaceAll(`{${k}}`, params[k])
    }
  }
  return str
}

// tn picks the "one"/"other" variant of `${key}.one` / `${key}.other`
// based on n, and substitutes {n} (plus any extra params) into it.
export function tn(key, n, params) {
  const suffix = n === 1 ? 'one' : 'other'
  return t(`${key}.${suffix}`, { n, ...params })
}
