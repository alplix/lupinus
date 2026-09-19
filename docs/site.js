// Lupinus website behaviour. Every language is its own pre-rendered page
// (see scripts/build-site.mjs), so this only adds the small dynamic touches:
// an OS-aware download button, the latest release number, and remembering an
// explicit language choice. The page is complete and crawlable without it.
(() => {
  'use strict';

  const REPO = 'alplix/lupinus';
  const DOWNLOAD_BASE = `https://github.com/${REPO}/releases/latest/download/`;

  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

  // Per-visitor convenience only; the page works with storage blocked.
  const store = {
    set(v) { try { localStorage.setItem('lupinus-lang', v); } catch { /* private mode */ } },
  };

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

  // Hero button: name the visitor's OS and link straight to its download.
  // Without a desktop OS it keeps its generic label and scrolls to the list.
  const os = detectOS();
  if (os) {
    const cta = $('#cta');
    cta.href = DOWNLOAD_BASE + OS_FILE[os];
    $('#cta-text').textContent = cta.dataset.tplOs.replace('{os}', OS_LABEL[os]);
    const card = $(`.os[data-os="${os}"]`);
    if (card) card.classList.add('match');
  }

  // Language selector: go to that language's page and remember the choice
  // so the root page stops auto-redirecting.
  const select = $('#lang');
  select.addEventListener('change', () => {
    const opt = select.selectedOptions[0];
    store.set(opt.dataset.code);
    location.href = opt.value;
  });
  $$('.langs a').forEach((a) => a.addEventListener('click', () => store.set(a.getAttribute('hreflang'))));

  // Latest release number, filled in from the GitHub API (the page is fine
  // without it: offline, rate limited, or blocked).
  (async () => {
    try {
      const res = await fetch(`https://api.github.com/repos/${REPO}/releases/latest`, {
        headers: { Accept: 'application/vnd.github+json' },
      });
      if (!res.ok) return;
      const data = await res.json();
      if (!data || !data.tag_name) return;
      const el = $('#latest');
      el.textContent = el.dataset.tpl.replace('{v}', String(data.tag_name).replace(/^v/, ''));
      el.hidden = false;
    } catch { /* ignore */ }
  })();
})();
