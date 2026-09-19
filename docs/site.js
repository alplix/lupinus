// Lupinus landing page: language switching, OS-aware download button and
// release links. English lives in index.html so the page reads fine without
// JavaScript; every other language is a small JSON file in i18n/ fetched on
// demand (same 38 languages as the app itself).
(() => {
  'use strict';

  const REPO = 'alplix/lupinus';
  const DOWNLOAD_BASE = `https://github.com/${REPO}/releases/latest/download/`;

  const LANGS = [
    ['en', 'English'], ['tr', 'Türkçe'], ['es', 'Español'], ['fr', 'Français'], ['de', 'Deutsch'],
    ['it', 'Italiano'], ['pt', 'Português'], ['ru', 'Русский'], ['ar', 'العربية'], ['zh', '中文'],
    ['ja', '日本語'], ['ko', '한국어'], ['hi', 'हिन्दी'], ['nl', 'Nederlands'], ['pl', 'Polski'],
    ['uk', 'Українська'], ['el', 'Ελληνικά'], ['sv', 'Svenska'], ['no', 'Norsk'], ['da', 'Dansk'],
    ['fi', 'Suomi'], ['cs', 'Čeština'], ['ro', 'Română'], ['hu', 'Magyar'], ['bg', 'Български'],
    ['hr', 'Hrvatski'], ['sr', 'Српски'], ['sk', 'Slovenčina'], ['sl', 'Slovenščina'], ['he', 'עברית'],
    ['fa', 'فارسی'], ['vi', 'Tiếng Việt'], ['th', 'ไทย'], ['id', 'Bahasa Indonesia'],
    ['ms', 'Bahasa Melayu'], ['sw', 'Kiswahili'], ['ur', 'اردو'], ['bn', 'বাংলা'],
  ];
  const RTL = new Set(['ar', 'he', 'fa', 'ur']);
  const codes = new Set(LANGS.map(([c]) => c));

  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

  // Per-visitor convenience only; the page works with storage blocked.
  const store = {
    get() { try { return localStorage.getItem('lupinus-lang'); } catch { return null; } },
    set(v) { try { localStorage.setItem('lupinus-lang', v); } catch { /* private mode */ }
    },
  };

  // ---------- OS detection ----------
  function detectOS() {
    const ua = navigator.userAgent || '';
    const platform = (navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || '';
    if (/android|iphone|ipad|ipod/i.test(ua)) return null; // no mobile build
    if (/win/i.test(platform) || /windows/i.test(ua)) return 'windows';
    if (/mac/i.test(platform) || /macintosh|mac os x/i.test(ua)) return 'macos';
    if (/linux|x11|cros/i.test(platform + ua)) return 'linux';
    return null;
  }
  const OS_LABEL = { windows: 'Windows', macos: 'macOS', linux: 'Linux' };
  const OS_FILE = {
    windows: 'lupinus-windows-amd64-setup.exe',
    macos: 'lupinus-darwin-universal.zip',
    linux: 'lupinus-linux-amd64.tar.gz',
  };
  const os = detectOS();

  // ---------- i18n ----------
  const english = {};
  const cache = { en: english };
  let current = 'en';
  let latestVersion = null;

  $$('[data-i18n]').forEach((el) => { english[el.dataset.i18n] = el.textContent; });
  $$('[data-i18n-alt]').forEach((el) => { english[el.dataset.i18nAlt] = el.getAttribute('alt'); });
  // Templates that the static HTML shows already filled in (or not at all).
  english['hero.cta'] = 'Download for {os}';
  english['dl.latest'] = 'Latest version: {v}';

  async function load(code) {
    if (cache[code]) return cache[code];
    const res = await fetch(`i18n/${code}.json`);
    if (!res.ok) throw new Error(`i18n/${code}.json: ${res.status}`);
    cache[code] = await res.json();
    return cache[code];
  }

  function fill(str) {
    return str
      .replace('{os}', os ? OS_LABEL[os] : '')
      .replace('{v}', latestVersion || '');
  }

  function apply(dict) {
    const t = (key) => fill(dict[key] ?? english[key] ?? '');
    $$('[data-i18n]').forEach((el) => {
      // The hero button names the visitor's OS; with no desktop OS it just
      // points at the download list.
      if (el.id === 'cta-text' && !os) { el.textContent = t('dl.title'); return; }
      el.textContent = t(el.dataset.i18n);
    });
    $$('[data-i18n-alt]').forEach((el) => el.setAttribute('alt', t(el.dataset.i18nAlt)));
    const sel = $('#lang');
    sel.setAttribute('aria-label', t('lang.label'));
    renderVersion(dict);
  }

  async function setLang(code, { persist = true } = {}) {
    if (!codes.has(code)) code = 'en';
    let dict;
    try { dict = await load(code); } catch { code = 'en'; dict = english; }
    current = code;
    document.documentElement.lang = code;
    document.documentElement.dir = RTL.has(code) ? 'rtl' : 'ltr';
    $('#lang').value = code;
    apply(dict);
    // Only an explicit choice is remembered (and put in the URL so the
    // link can be shared); merely auto-detecting the language changes nothing.
    if (!persist) return;
    store.set(code);
    try {
      const url = new URL(location.href);
      url.searchParams.set('lang', code);
      history.replaceState(null, '', url);
    } catch { /* file:// or sandboxed */ }
  }

  function pickInitialLang() {
    const fromUrl = new URLSearchParams(location.search).get('lang');
    if (fromUrl && codes.has(fromUrl)) return fromUrl;
    const saved = store.get();
    if (saved && codes.has(saved)) return saved;
    for (const l of navigator.languages || [navigator.language || 'en']) {
      const short = l.toLowerCase().split('-')[0];
      if (codes.has(short)) return short;
      if (short === 'nb' || short === 'nn') return 'no';
    }
    return 'en';
  }

  // ---------- downloads ----------
  function wireDownloads() {
    $$('[data-file]').forEach((a) => {
      a.href = DOWNLOAD_BASE + a.dataset.file;
      a.rel = 'noopener';
    });
    const cta = $('#cta');
    if (os) {
      cta.href = DOWNLOAD_BASE + OS_FILE[os];
      const card = $(`.os[data-os="${os}"]`);
      if (card) card.classList.add('match');
    }
  }

  function renderVersion(dict) {
    const el = $('#latest');
    if (!latestVersion) { el.hidden = true; return; }
    el.textContent = fill((dict && dict['dl.latest']) || english['dl.latest'] || 'v{v}');
    el.hidden = false;
  }

  async function fetchLatest() {
    try {
      const res = await fetch(`https://api.github.com/repos/${REPO}/releases/latest`, {
        headers: { Accept: 'application/vnd.github+json' },
      });
      if (!res.ok) return;
      const data = await res.json();
      if (data && data.tag_name) {
        latestVersion = String(data.tag_name).replace(/^v/, '');
        renderVersion(cache[current]);
      }
    } catch { /* offline or rate limited: the page is fine without it */ }
  }

  // ---------- boot ----------
  const select = $('#lang');
  for (const [code, name] of LANGS) {
    const opt = document.createElement('option');
    opt.value = code;
    opt.textContent = name;
    select.appendChild(opt);
  }
  select.addEventListener('change', () => setLang(select.value));

  wireDownloads();
  setLang(pickInitialLang(), { persist: false });
  fetchLatest();
})();
