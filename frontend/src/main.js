import './style.css'
import { openViewer } from './viewer.js'
import { t, tn, setLocale, detectLocale, currentLocaleCode, AVAILABLE_LOCALES } from './i18n.js'
import {
  GetAbout,
  ListConnections,
  SaveConnection,
  DeleteConnection,
  GetTheme,
  SetTheme,
  GetVNCQuality,
  SetVNCQuality,
  GetLanguage,
  SetLanguage,
  Connect,
  QuickConnect,
  Disconnect,
  OpenSupportLink,
  OpenGitHub,
  ExportConnections,
  ImportConnections,
  ListTrustedCertificates,
  ForgetCertificate,
} from '../wailsjs/go/main/App'
import { EventsOn } from '../wailsjs/runtime/runtime'

// ==========================================================================
// tiny DOM helpers
// ==========================================================================

const $ = (sel, root = document) => root.querySelector(sel)

function esc(s) {
  const d = document.createElement('div')
  d.textContent = s ?? ''
  return d.innerHTML
}

// Escapes a value for safe use inside a double-quoted HTML attribute.
function attr(s) {
  return esc(s).replace(/"/g, '&quot;').replace(/'/g, '&#39;')
}

// ==========================================================================
// icons (small inline SVGs, currentColor-based so they theme automatically)
// ==========================================================================

// Five-petal sakura mark, used as the Lupinus wordmark glyph across the
// header, empty state and About modal — small, geometric, not illustrative.
// No fixed size/class on the <svg> itself — it's sized by whatever wrapper
// (.brand-mark / .about-mark) embeds it, via the .lp-mark 100%/100% rule.
const BRAND_MARK_SVG = `<svg class="lp-mark" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
  <g fill="currentColor">
    <ellipse cx="12" cy="6.2" rx="2.1" ry="3.6"/>
    <ellipse cx="7.4" cy="8.6" rx="2.1" ry="3.6" transform="rotate(-72 7.4 8.6)"/>
    <ellipse cx="8.6" cy="14.3" rx="2.1" ry="3.6" transform="rotate(-144 8.6 14.3)"/>
    <ellipse cx="15.4" cy="14.3" rx="2.1" ry="3.6" transform="rotate(144 15.4 14.3)"/>
    <ellipse cx="16.6" cy="8.6" rx="2.1" ry="3.6" transform="rotate(72 16.6 8.6)"/>
  </g>
  <circle cx="12" cy="11" r="1.5" style="fill:var(--surface)"/>
</svg>`

const INFO_ICON = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" xmlns="http://www.w3.org/2000/svg"><circle cx="12" cy="12" r="9"/><line x1="12" y1="11" x2="12" y2="16"/><circle cx="12" cy="7.6" r="0.9" fill="currentColor" stroke="none"/></svg>`

const CLOSE_ICON = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" xmlns="http://www.w3.org/2000/svg"><line x1="5" y1="5" x2="19" y2="19"/><line x1="19" y1="5" x2="5" y2="19"/></svg>`

const BACK_ICON = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" xmlns="http://www.w3.org/2000/svg" style="width:14px;height:14px;vertical-align:-2px;margin-right:3px;"><line x1="19" y1="12" x2="5" y2="12"/><polyline points="12 19 5 12 12 5"/></svg>`

// Fixed, low-contrast star positions for the connection-manager backdrop —
// "a handful", not a particle system. Pure CSS keyframes drive the twinkle
// so prefers-reduced-motion (handled globally in style.css) turns it off.
const COSMOS_STARS = [
  { top: 8, left: 12, size: 2, delay: 0 },
  { top: 15, left: 82, size: 1.5, delay: 1.2 },
  { top: 30, left: 45, size: 1.8, delay: 2.4 },
  { top: 55, left: 90, size: 1.4, delay: 0.6 },
  { top: 70, left: 8, size: 2.2, delay: 3.1 },
  { top: 85, left: 60, size: 1.6, delay: 1.8 },
  { top: 42, left: 25, size: 1.3, delay: 2.9 },
]

// ==========================================================================
// state
// ==========================================================================

const state = {
  page: 'connections', // 'connections' | 'settings' | 'viewer'
  connections: [],
  theme: 'system',
  vncQuality: 'balanced', // 'balanced' | 'quality' | 'bandwidth' — see internal/rfb's qualityPresetLevels
  about: null,
  modal: null, // { type: 'about' | 'connForm' | 'quickConnect', ...fields }
  toasts: [],
  // Every simultaneously open session, not just one — the backend already
  // supports concurrent sessions (app.go's `live` map), this is what makes
  // that reachable from the UI. Each entry: { localId, sessionId, connId,
  // name, protocol, host, port, bridgeUrl, status, message, scalingMode,
  // cancelled }. `localId` is assigned client-side the instant a connect
  // starts (before the backend has handed back a sessionId) and is stable
  // for the entry's lifetime; `sessionId` fills in once Connect()/
  // QuickConnect() resolves.
  viewers: [],
  activeViewerId: null, // localId of the viewer tab currently shown
  trustedCerts: [], // "host:port" addresses with a pinned RDP TLS certificate
}

// Per-session canvas/WebSocket handles, keyed by localId — deliberately
// kept outside `state` since openViewer()'s return value and the DOM
// container it owns aren't serializable render state. A session's entry
// here persists for its whole lifetime so switching tabs never tears down
// or reconnects a WebSocket, only shows/hides its container.
const viewerHandles = new Map() // localId -> { handle, container }
let pendingViewerCounter = 0

// ==========================================================================
// formatting helpers
// ==========================================================================

function fmtLastUsed(iso) {
  if (!iso) return t('connections.lastUsed.never')
  const ts = new Date(iso).getTime()
  if (Number.isNaN(ts)) return t('connections.lastUsed.never')
  const diff = (Date.now() - ts) / 1000
  if (diff < 60) return t('connections.lastUsed.justNow')
  if (diff < 3600) return t('connections.lastUsed.minutes', { n: Math.floor(diff / 60) })
  if (diff < 86400) return t('connections.lastUsed.hours', { n: Math.floor(diff / 3600) })
  if (diff < 604800) return t('connections.lastUsed.days', { n: Math.floor(diff / 86400) })
  return new Date(iso).toLocaleDateString()
}

function statusLabel(s) {
  return (
    {
      connected: t('connections.status.connected'),
      connecting: t('connections.status.connecting'),
      reconnecting: t('connections.status.reconnecting'),
      error: t('connections.status.error'),
      disconnected: t('connections.status.disconnected'),
    }[s] || s
  )
}

// ==========================================================================
// toasts
// ==========================================================================

function toast(msg, type = 'info') {
  const id = Date.now() + Math.random()
  state.toasts.push({ id, msg, type })
  renderToasts()
  setTimeout(() => {
    state.toasts = state.toasts.filter((t) => t.id !== id)
    renderToasts()
  }, 3500)
}

function renderToasts() {
  const wrap = $('#toast-wrap')
  if (!wrap) return
  wrap.innerHTML = state.toasts.map((t) => `<div class="toast ${esc(t.type)}">${esc(t.msg)}</div>`).join('')
}

// ==========================================================================
// theme
// ==========================================================================

function applyTheme(theme) {
  if (theme === 'light' || theme === 'dark') {
    document.documentElement.setAttribute('data-theme', theme)
  } else {
    // 'system': no override — the CSS prefers-color-scheme media query
    // takes it from here, including live OS changes, with no listener.
    document.documentElement.removeAttribute('data-theme')
  }
}

// ==========================================================================
// top-level render dispatch
// ==========================================================================

function render() {
  if (state.page === 'viewer' && state.viewers.length > 0) {
    renderViewerPage()
  } else {
    detachCanvasHost()
    renderShellPage()
  }
  renderModal()
  renderToasts()
}

// The persistent canvas host (see the `state.viewers`/`viewerHandles` doc
// comment above) must be moved out of #view-root before renderShellPage()
// overwrites its innerHTML — otherwise that rewrite would destroy every
// open session's canvas and WebSocket along with it.
function detachCanvasHost() {
  const host = document.getElementById('viewer-canvas-host')
  if (!host) return
  host.style.display = 'none'
  if (host.parentElement !== document.body) document.body.appendChild(host)
}

function attachCanvasHost(beforeEl) {
  const host = document.getElementById('viewer-canvas-host')
  if (!host || !beforeEl) return
  beforeEl.parentElement.insertBefore(host, beforeEl)
  host.style.display = ''
}

function navigate(page) {
  if (page === state.page) return
  state.page = page
  render()
}
window._nav = navigate

// ==========================================================================
// app shell (connections / settings)
// ==========================================================================

function renderShellPage() {
  const root = $('#view-root')
  root.innerHTML = `
    <div class="app-shell">
      <header class="app-header">
        <div class="brand">
          <span class="brand-mark">${BRAND_MARK_SVG}</span>
          <span class="brand-name">Lupinus</span>
        </div>
        <nav class="app-nav">
          <button class="${state.page === 'connections' ? 'active' : ''}" onclick="window._nav('connections')">${t('nav.connections')}</button>
          <button class="${state.page === 'settings' ? 'active' : ''}" onclick="window._nav('settings')">${t('nav.settings')}</button>
        </nav>
        <div class="header-spacer"></div>
        ${state.viewers.length > 0 ? `<button class="btn sm" onclick="window._viewerReturn()">${tn('nav.activeSession', state.viewers.length)}</button>` : ''}
        <button class="icon-btn" title="${attr(t('nav.aboutTooltip'))}" onclick="window._openAbout()">${INFO_ICON}</button>
      </header>
      <div class="page-root">
        ${state.page === 'connections' ? '<div class="cosmos-bg" id="cosmos-bg"></div>' : ''}
        <div class="page-inner">
          ${state.page === 'connections' ? renderConnectionsPage() : renderSettingsPage()}
        </div>
      </div>
    </div>
  `
  if (state.page === 'connections') mountCosmosStars()
}

function mountCosmosStars() {
  const bg = $('#cosmos-bg')
  if (!bg) return
  bg.innerHTML = COSMOS_STARS.map(
    (s) => `<span class="cosmos-star" style="top:${s.top}%;left:${s.left}%;width:${s.size}px;height:${s.size}px;animation-delay:${s.delay}s;"></span>`
  ).join('')
}

// ---- connections page ----

function statusClassFor(conn) {
  const v = state.viewers.find((v) => v.connId === conn.id)
  return v ? v.status : 'disconnected'
}

function renderConnCard(c) {
  const statusClass = statusClassFor(c)
  const isLive = statusClass === 'connected' || statusClass === 'connecting' || statusClass === 'reconnecting'
  return `
    <div class="conn-card" style="${colorTagHex(c.colorTag) ? `--tag-color:${colorTagHex(c.colorTag)};` : ''}">
      <span class="status-dot ${esc(statusClass)}" title="${attr(statusLabel(statusClass))}"></span>
      <div class="conn-info">
        <div class="conn-name trunc">${esc(c.name || c.host)}</div>
        <div class="conn-meta">
          <span class="protocol-tag">${esc((c.protocol || 'vnc').toUpperCase())}</span>
          <span class="num">${esc(c.host)}:${esc(String(c.port))}</span>
          <span>${esc(fmtLastUsed(c.lastUsed))}</span>
        </div>
      </div>
      <div class="conn-actions">
        <button class="btn sm primary" onclick="window._connect('${attr(c.id)}')">${isLive ? t('connections.view') : t('connections.connect')}</button>
        <button class="btn sm ghost" onclick="window._openEditConnection('${attr(c.id)}')">${t('connections.edit')}</button>
        <button class="btn sm ghost danger" onclick="window._deleteConnection('${attr(c.id)}')">${t('connections.delete')}</button>
      </div>
    </div>
  `
}

function renderEmptyState() {
  return `
    <div class="empty-state">
      <div class="brand-mark">${BRAND_MARK_SVG}</div>
      <div class="empty-wordmark">Lupinus</div>
      <h2>${t('connections.empty.title')}</h2>
      <p>${t('connections.empty.body')}</p>
      <button class="btn primary" onclick="window._openAddConnection()">${t('connections.empty.cta')}</button>
    </div>
  `
}

function renderConnectionsPage() {
  const list = state.connections
  return `
    <div class="page-heading">
      <h1>${t('connections.heading')}</h1>
      <p>${tn('connections.savedCount', list.length)}</p>
    </div>
    <div class="row" style="justify-content:flex-end;">
      <button class="btn" onclick="window._openQuickConnect()">${t('connections.quickConnect')}</button>
      <button class="btn primary" onclick="window._openAddConnection()">${t('connections.newConnection')}</button>
    </div>
    ${list.length === 0 ? renderEmptyState() : `<div class="conn-list">${list.map(renderConnCard).join('')}</div>`}
    <footer class="app-footer">
      <span class="footer-credit"><b>Lupinus</b> · ${t('connections.footer.credit')}</span>
      <a class="link" href="#" onclick="window._openSupport();return false;">${t('connections.footer.support')}</a>
    </footer>
  `
}

// ---- settings page ----

function renderSettingsPage() {
  const theme = state.theme
  const vncQuality = state.vncQuality
  return `
    <div class="page-heading">
      <h1>${t('settings.heading')}</h1>
      <p>${t('settings.subheading')}</p>
    </div>
    <div class="settings-card">
      <h2>${t('settings.appearance.title')}</h2>
      <div class="theme-options">
        <button class="${theme === 'light' ? 'active' : ''}" onclick="window._setTheme('light')">${t('settings.appearance.light')}</button>
        <button class="${theme === 'dark' ? 'active' : ''}" onclick="window._setTheme('dark')">${t('settings.appearance.dark')}</button>
        <button class="${theme === 'system' ? 'active' : ''}" onclick="window._setTheme('system')">${t('settings.appearance.system')}</button>
      </div>
    </div>
    <div class="settings-card">
      <h2>${t('settings.language.title')}</h2>
      <select id="language-select" onchange="window._setLanguage(this.value)">
        ${AVAILABLE_LOCALES.map((l) => `<option value="${attr(l.code)}" ${l.code === currentLocaleCode() ? 'selected' : ''}>${esc(l.name)}</option>`).join('')}
      </select>
    </div>
    <div class="settings-card">
      <h2>${t('settings.quality.title')}</h2>
      <p class="soft" style="margin:0 0 8px;font-size:13px;">${t('settings.quality.desc')}</p>
      <div class="theme-options">
        <button class="${vncQuality === 'quality' ? 'active' : ''}" onclick="window._setVNCQuality('quality')">${t('settings.quality.high')}</button>
        <button class="${vncQuality === 'balanced' ? 'active' : ''}" onclick="window._setVNCQuality('balanced')">${t('settings.quality.balanced')}</button>
        <button class="${vncQuality === 'bandwidth' ? 'active' : ''}" onclick="window._setVNCQuality('bandwidth')">${t('settings.quality.low')}</button>
      </div>
      <p class="soft" style="margin:8px 0 0;font-size:12px;">${t('settings.quality.hint')}</p>
    </div>
    <div class="settings-card">
      <h2>${t('settings.connections.title')}</h2>
      <p class="soft" style="margin:0;font-size:13px;">${t('settings.connections.desc')}</p>
      <div class="row">
        <button class="btn sm" onclick="window._exportConnections()">${t('settings.connections.export')}</button>
        <button class="btn sm ghost" onclick="window._importConnections()">${t('settings.connections.import')}</button>
      </div>
    </div>
    ${renderTrustedCertsCard()}
    <div class="settings-card">
      <h2>${t('settings.about.title')}</h2>
      <p class="soft" style="margin:0;font-size:13px;">${t('settings.about.desc')}</p>
      <div class="row">
        <button class="btn sm" onclick="window._openAbout()">${t('settings.about.aboutBtn')}</button>
        <button class="btn sm ghost" onclick="window._openGitHub()">${t('settings.about.github')}</button>
      </div>
    </div>
    <div class="settings-card support-card">
      <div class="support-wordmark">Lupinus</div>
      <div class="support-credit">${t('about.credit')}</div>
      <div class="support-prompt">${t('settings.support.prompt')}</div>
      <button class="btn primary sm" onclick="window._openSupport()">${t('settings.support.btn')}</button>
    </div>
  `
}

// RDP's TLS layer is trust-on-first-use (see internal/store's doc
// comment) — the first certificate seen for a server is pinned, and a
// later mismatch is treated as a hard error rather than silently
// re-trusting it. This card is only shown once at least one server has
// been pinned, and lets the user clear a stale pin (e.g. after a
// legitimate server reinstall) without editing the config file by hand.
function renderTrustedCertsCard() {
  if (state.trustedCerts.length === 0) return ''
  return `
    <div class="settings-card">
      <h2>${t('settings.certs.title')}</h2>
      <p class="soft" style="margin:0;font-size:13px;">${t('settings.certs.desc')}</p>
      <div class="trusted-cert-list">
        ${state.trustedCerts
          .map(
            (addr) => `
          <div class="trusted-cert-row">
            <span class="num trunc">${esc(addr)}</span>
            <button class="btn sm ghost danger" onclick="window._forgetCertificate('${attr(addr)}')">${t('settings.certs.forget')}</button>
          </div>
        `
          )
          .join('')}
      </div>
    </div>
  `
}

// ==========================================================================
// modal layer (About / connection form / quick connect)
// ==========================================================================

function renderModal() {
  const root = $('#modal-root')
  if (!root) return
  if (!state.modal) {
    root.innerHTML = ''
    return
  }
  root.innerHTML = renderModalContent(state.modal)
  const overlay = $('.modal-overlay', root)
  if (overlay) {
    overlay.addEventListener('click', (e) => {
      if (e.target === overlay) window._closeModal()
    })
  }
}

function renderModalContent(m) {
  if (m.type === 'about') return renderAboutModal()
  if (m.type === 'connForm') return renderConnFormModal(m)
  if (m.type === 'quickConnect') return renderQuickConnectModal(m)
  return ''
}

function renderAboutModal() {
  const a = state.about || {}
  return `
    <div class="modal-overlay">
      <div class="modal about-modal">
        <div class="about-motif"></div>
        <button class="icon-btn" style="position:absolute;top:10px;right:10px;z-index:2;" onclick="window._closeModal()">${CLOSE_ICON}</button>
        <div class="about-content">
          <div class="about-mark">${BRAND_MARK_SVG}</div>
          <div class="about-name">${esc(a.name || 'Lupinus')}</div>
          <div class="about-subtitle">${t('about.subtitle')}</div>
          <div class="about-line">${t('about.credit')}</div>
          <div class="about-version">${a.version ? `v${esc(a.version)}` : ''}</div>
          <div class="about-actions">
            <button class="btn sm" onclick="window._openGitHub()">${t('settings.about.github')}</button>
            <button class="btn sm primary" onclick="window._openSupport()">${t('settings.support.btn')}</button>
          </div>
          <div class="about-copyright">${t('about.copyright')}</div>
        </div>
      </div>
    </div>
  `
}

// Preset palette for organizing saved connections by color — purely a
// user-facing label, no semantic meaning. '' is "no tag".
const COLOR_TAGS = [
  { key: '', hex: null },
  { key: 'violet', hex: '#7c5cff' },
  { key: 'sakura', hex: '#f29bcb' },
  { key: 'teal', hex: '#4fd1c5' },
  { key: 'amber', hex: '#f6ad55' },
  { key: 'rose', hex: '#fc8181' },
  { key: 'slate', hex: '#94a3b8' },
]

function colorTagHex(key) {
  return COLOR_TAGS.find((t) => t.key === key)?.hex || null
}

function renderColorTagPicker(prefix, selected) {
  return `
    <div class="field">
      <label>${t('form.colorTag')}</label>
      <div class="colortag-picker" id="${prefix}-colortag-picker" data-value="${attr(selected)}">
        ${COLOR_TAGS.map(
          (tag) => `
          <button type="button" class="colortag-swatch ${tag.key === selected ? 'selected' : ''} ${tag.hex ? '' : 'none'}"
            data-key="${attr(tag.key)}" style="${tag.hex ? `background:${tag.hex};` : ''}"
            onclick="window._pickColorTag('${prefix}', '${attr(tag.key)}')" title="${attr(tag.key || t('form.colorTagNone'))}"></button>
        `
        ).join('')}
      </div>
    </div>
  `
}

window._pickColorTag = (prefix, key) => {
  const picker = document.getElementById(`${prefix}-colortag-picker`)
  if (!picker) return
  picker.dataset.value = key
  for (const el of picker.children) el.classList.toggle('selected', el.dataset.key === key)
}

function renderConnFormModal(m) {
  const editing = !!m.id
  const c = editing ? state.connections.find((x) => x.id === m.id) : null
  // On a validation error we re-render this modal from scratch — pull the
  // user's in-progress input back in instead of resetting to saved values.
  const d = m.draft
  const protocol = d ? d.protocol : c?.protocol || 'vnc'
  const nameVal = d ? d.name : c?.name || ''
  const hostVal = d ? d.host : c?.host || ''
  const portVal = d ? d.port : String(c?.port || defaultPortFor(protocol))
  const userVal = d ? d.username : c?.username || ''
  const passVal = d ? d.password : ''
  const colorTagVal = d ? d.colorTag || '' : c?.colorTag || ''
  return `
    <div class="modal-overlay">
      <div class="modal">
        <div class="modal-head">
          <h3>${editing ? t('form.editTitle') : t('form.newTitle')}</h3>
          <button class="icon-btn" onclick="window._closeModal()">${CLOSE_ICON}</button>
        </div>
        ${m.error ? `<div class="modal-error">${esc(m.error)}</div>` : ''}
        ${renderProtocolSelector('f', protocol)}
        <div class="field">
          <label>${t('form.name')}</label>
          <input id="f-name" value="${attr(nameVal)}" placeholder="${attr(t('form.namePlaceholder'))}">
        </div>
        <div class="field-row">
          <div class="field">
            <label>${t('form.host')}</label>
            <input id="f-host" value="${attr(hostVal)}" placeholder="${attr(t('form.hostPlaceholder'))}">
          </div>
          <div class="field" style="max-width:110px;">
            <label>${t('form.port')}</label>
            <input id="f-port" value="${attr(portVal)}" inputmode="numeric">
          </div>
        </div>
        <div class="field">
          <label>${t('form.username')}</label>
          <input id="f-user" value="${attr(userVal)}" placeholder="${attr(usernamePlaceholderFor(protocol))}">
          ${protocol === 'vnc' ? `<div class="field-hint">${t('form.usernameHintVnc')}</div>` : ''}
        </div>
        <div class="field">
          <label>${t('form.password')}</label>
          <input id="f-pass" type="password" value="${attr(passVal)}" placeholder="${attr(editing ? t('form.passwordPlaceholderEdit') : t('form.passwordPlaceholderNew'))}">
          ${editing ? `<div class="field-hint">${t('form.passwordHintEdit')}</div>` : ''}
        </div>
        ${renderColorTagPicker('f', colorTagVal)}
        <div class="modal-foot">
          <button class="btn" onclick="window._closeModal()">${t('form.cancel')}</button>
          <button class="btn primary" onclick="window._saveConnection('${editing ? attr(m.id) : ''}')">${editing ? t('form.save') : t('form.add')}</button>
        </div>
      </div>
    </div>
  `
}

function usernamePlaceholderFor(protocol) {
  return protocol === 'rdp' ? t('form.usernamePlaceholderRdp') : t('form.usernamePlaceholderVnc')
}

function defaultPortFor(protocol) {
  return protocol === 'rdp' ? 3389 : 5900
}

// A small VNC/RDP segmented control shared by the Add/Edit and Quick
// Connect modals. Switching it re-renders the modal so the port default and
// (for RDP) the username field update live.
function renderProtocolSelector(prefix, protocol) {
  return `
    <div class="field">
      <label>${t('form.protocol')}</label>
      <div class="theme-options">
        <button type="button" class="${protocol === 'vnc' ? 'active' : ''}" onclick="window._setModalProtocol('${prefix}', 'vnc')">VNC</button>
        <button type="button" class="${protocol === 'rdp' ? 'active' : ''}" onclick="window._setModalProtocol('${prefix}', 'rdp')">RDP</button>
      </div>
    </div>
  `
}

function renderQuickConnectModal(m) {
  const d = m.draft
  const protocol = d ? d.protocol : 'vnc'
  const hostVal = d ? d.host : ''
  const portVal = d ? d.port : String(defaultPortFor(protocol))
  const userVal = d ? d.username : ''
  const passVal = d ? d.password : ''
  return `
    <div class="modal-overlay">
      <div class="modal">
        <div class="modal-head">
          <h3>${t('form.quickConnectTitle')}</h3>
          <button class="icon-btn" onclick="window._closeModal()">${CLOSE_ICON}</button>
        </div>
        ${m.error ? `<div class="modal-error">${esc(m.error)}</div>` : ''}
        ${renderProtocolSelector('qc', protocol)}
        <div class="field-row">
          <div class="field">
            <label>${t('form.host')}</label>
            <input id="qc-host" value="${attr(hostVal)}" placeholder="${attr(t('form.hostPlaceholder'))}">
          </div>
          <div class="field" style="max-width:110px;">
            <label>${t('form.port')}</label>
            <input id="qc-port" value="${attr(portVal)}" inputmode="numeric">
          </div>
        </div>
        <div class="field">
          <label>${t('form.username')}</label>
          <input id="qc-user" value="${attr(userVal)}" placeholder="${attr(usernamePlaceholderFor(protocol))}">
        </div>
        <div class="field">
          <label>${t('form.password')}</label>
          <input id="qc-pass" type="password" value="${attr(passVal)}" placeholder="${attr(t('form.passwordPlaceholderNew'))}">
        </div>
        <div class="modal-foot">
          <button class="btn" onclick="window._closeModal()">${t('form.cancel')}</button>
          <button class="btn primary" onclick="window._quickConnect()">${t('form.connect')}</button>
        </div>
      </div>
    </div>
  `
}

// ==========================================================================
// viewer page — supports multiple simultaneously open sessions. Each
// session gets a persistent DOM slot (canvas + its own overlay) inside the
// shared #viewer-canvas-host; only the active one's slot is visible, but
// every open session keeps running (and its overlay stays accurate) in the
// background. See the `state.viewers`/`viewerHandles` doc comment near the
// top of this file for the full design.
// ==========================================================================

function activeViewer() {
  return state.viewers.find((v) => v.localId === state.activeViewerId) || null
}

function createViewerSlot(v) {
  const host = document.getElementById('viewer-canvas-host')
  const slot = document.createElement('div')
  slot.className = 'viewer-canvas-slot'
  slot.dataset.localId = v.localId
  const overlay = document.createElement('div')
  overlay.className = 'viewer-overlay'
  slot.appendChild(overlay)
  host.appendChild(slot)
  viewerHandles.set(v.localId, { handle: null, container: slot, overlayEl: overlay })
}

function showActiveSlot() {
  const host = document.getElementById('viewer-canvas-host')
  if (!host) return
  for (const child of host.children) {
    child.classList.toggle('active', child.dataset.localId === state.activeViewerId)
  }
}

// Updates one session's overlay in place — safe to call for a background
// (non-active) session too, so its overlay is already correct the instant
// the user switches to it.
function updateSlotOverlay(v) {
  const entry = viewerHandles.get(v.localId)
  if (!entry) return
  const overlay = entry.overlayEl
  if (v.status === 'connected') {
    overlay.style.display = 'none'
    overlay.innerHTML = ''
    return
  }
  overlay.style.display = 'flex'
  if (v.status === 'error') {
    overlay.innerHTML = `
      <h2>${t('viewer.connectionFailed')}</h2>
      <p class="error-message">${esc(v.message || t('viewer.connectionFailedDefault'))}</p>
      <button class="btn primary sm" onclick="window._viewerClose('${attr(v.localId)}')">${t('viewer.close')}</button>
    `
  } else if (v.status === 'disconnected') {
    overlay.innerHTML = `
      <h2>${t('viewer.disconnected')}</h2>
      <p>${esc(v.message || t('viewer.disconnectedDefault'))}</p>
      <button class="btn primary sm" onclick="window._viewerClose('${attr(v.localId)}')">${t('viewer.close')}</button>
    `
  } else {
    overlay.innerHTML = `
      <div class="spinner"></div>
      <h2>${v.status === 'reconnecting' ? t('viewer.reconnecting') : t('viewer.connecting')}</h2>
      <p>${esc(`${v.host || ''}:${v.port || ''}`)}</p>
    `
  }
}

function renderViewerTabs() {
  if (state.viewers.length <= 1) return ''
  return `
    <div class="viewer-tabs">
      ${state.viewers
        .map(
          (v) => `
        <button class="viewer-tab ${v.localId === state.activeViewerId ? 'active' : ''}" onclick="window._viewerSwitch('${attr(v.localId)}')">
          <span class="status-dot ${esc(v.status)}"></span>
          <span class="trunc">${esc(v.name || `${v.host}:${v.port}`)}</span>
          <span class="viewer-tab-close" onclick="event.stopPropagation();window._viewerClose('${attr(v.localId)}')">${CLOSE_ICON}</span>
        </button>
      `
        )
        .join('')}
    </div>
  `
}

function renderViewerPage() {
  const v = activeViewer()
  if (!v) {
    // Shouldn't happen (render() only takes this branch when
    // state.viewers is non-empty), but fall back safely rather than
    // render a broken shell.
    detachCanvasHost()
    renderShellPage()
    return
  }
  // This function is called both via the top-level render() dispatch and
  // directly (connect resolution, status events) while already on the
  // viewer page — in the latter case the canvas host is already reparented
  // inside the .viewer-shell markup about to be replaced below. Always
  // pull it back out first so `root.innerHTML =` below never destroys a
  // live session's canvas/WebSocket along with the chrome around it.
  detachCanvasHost()
  const root = $('#view-root')
  root.innerHTML = `
    <div class="viewer-shell">
      <div class="viewer-toolbar">
        <button class="btn sm ghost" onclick="window._viewerBack()">${BACK_ICON}${t('viewer.back')}</button>
        <span class="viewer-title trunc">${esc(v.name || `${v.host}:${v.port}`)}</span>
        <div class="spacer"></div>
        <select id="viewer-scaling" onchange="window._viewerSetScaling(this.value)">
          <option value="fit" ${v.scalingMode === 'fit' ? 'selected' : ''}>${t('viewer.fit')}</option>
          <option value="actual" ${v.scalingMode === 'actual' ? 'selected' : ''}>${t('viewer.actual')}</option>
          <option value="stretch" ${v.scalingMode === 'stretch' ? 'selected' : ''}>${t('viewer.stretch')}</option>
        </select>
        <button class="btn sm" onclick="window._viewerClipboardSync()">${t('viewer.syncClipboard')}</button>
        <button class="btn sm" onclick="window._viewerCtrlAltDel()">${t('viewer.ctrlAltDel')}</button>
        <button class="btn sm" onclick="window._viewerFullscreen()">${t('viewer.fullscreen')}</button>
        <button class="btn sm danger" onclick="window._viewerClose('${attr(v.localId)}')">${t('viewer.disconnect')}</button>
      </div>
      ${renderViewerTabs()}
      <div class="viewer-statusbar" id="viewer-statusbar"></div>
    </div>
  `
  // Reinsert the persistent canvas host right before the status bar — a
  // DOM move, not a rebuild, so every open session's canvas/WebSocket
  // (including ones not currently active) survives untouched.
  attachCanvasHost($('#viewer-statusbar'))
  showActiveSlot()

  const statusbar = $('#viewer-statusbar')
  statusbar.innerHTML = `
    <span class="status-dot ${esc(v.status)}"></span>
    <span>${esc(statusLabel(v.status))}</span>
    <span class="faint">· ${esc(`${v.host || ''}:${v.port || ''}`)}</span>
  `
}

function mountViewerCanvasFor(v) {
  const entry = viewerHandles.get(v.localId)
  if (!entry || entry.handle || !v.bridgeUrl) return
  entry.handle = openViewer({
    container: entry.container,
    bridgeUrl: v.bridgeUrl,
    protocol: v.protocol,
    onSocketState: () => {
      // Raw WebSocket open/close/error — the RFB-level lupinus:status event
      // is the authoritative source for the overlay/status bar, so this is
      // intentionally a no-op beyond what that event already drives.
    },
    onCutText: (text) => {
      navigator.clipboard?.writeText?.(text).catch(() => {})
    },
  })
}

// Starts a new session: creates its viewer-state entry and DOM slot
// immediately (so "Connecting…" shows right away), then wires up
// connectPromise's resolution without blocking on it — multiple of these
// can be in flight at once, each independent.
function startConnect(info, connectPromise) {
  const localId = 'v' + ++pendingViewerCounter
  const v = {
    localId,
    sessionId: null,
    connId: info.connId || null,
    name: info.name || '',
    protocol: info.protocol || 'vnc',
    host: info.host,
    port: info.port,
    bridgeUrl: null,
    status: 'connecting',
    message: '',
    scalingMode: 'fit',
    cancelled: false,
  }
  state.viewers.push(v)
  state.activeViewerId = localId
  state.page = 'viewer'
  createViewerSlot(v)
  updateSlotOverlay(v)
  render()

  connectPromise.then(
    (result) => {
      if (v.cancelled) {
        Disconnect(result.sessionId).catch(() => {})
        return
      }
      v.sessionId = result.sessionId
      v.bridgeUrl = result.bridgeUrl
      if (v.status !== 'error') {
        v.status = 'connected'
        v.message = ''
      }
      mountViewerCanvasFor(v)
      updateSlotOverlay(v)
      if (state.page === 'viewer') renderViewerPage()
    },
    (e) => {
      if (v.cancelled) return
      v.status = 'error'
      v.message = (e && (e.message || e.toString())) || t('toast.connectionFailedGeneric')
      updateSlotOverlay(v)
      if (state.page === 'viewer') renderViewerPage()
    }
  )

  return v
}

async function closeViewer(localId) {
  const v = state.viewers.find((x) => x.localId === localId)
  if (!v) return
  v.cancelled = true
  const entry = viewerHandles.get(localId)
  if (entry) {
    try {
      entry.handle?.close()
    } catch {
      /* already closed */
    }
    entry.container.remove()
    viewerHandles.delete(localId)
  }
  if (v.sessionId) {
    try {
      await Disconnect(v.sessionId)
    } catch {
      /* best effort */
    }
  }
  state.viewers = state.viewers.filter((x) => x.localId !== localId)
  if (state.activeViewerId === localId) {
    state.activeViewerId = state.viewers.length ? state.viewers[state.viewers.length - 1].localId : null
  }
}

window._viewerSwitch = (localId) => {
  if (!state.viewers.find((v) => v.localId === localId)) return
  state.activeViewerId = localId
  render()
}

window._viewerClose = async (localId) => {
  await closeViewer(localId)
  if (state.viewers.length === 0) {
    state.page = 'connections'
  }
  try {
    state.connections = (await ListConnections()) || []
  } catch {
    /* keep the existing list on failure */
  }
  render()
}

// Leaves the viewer page WITHOUT disconnecting anything — open sessions
// keep running in the background, reachable again via the header's
// "N active sessions" button or by clicking a now-live connection's "View".
window._viewerBack = async () => {
  state.page = 'connections'
  try {
    state.connections = (await ListConnections()) || []
  } catch {
    /* keep the existing list on failure */
  }
  render()
}

window._viewerReturn = () => {
  if (!state.viewers.length) return
  if (!state.viewers.find((v) => v.localId === state.activeViewerId)) {
    state.activeViewerId = state.viewers[state.viewers.length - 1].localId
  }
  state.page = 'viewer'
  render()
}

window._viewerSetScaling = (mode) => {
  const v = activeViewer()
  if (!v) return
  v.scalingMode = mode
  viewerHandles.get(v.localId)?.handle?.setScalingMode(mode)
}

window._viewerFullscreen = () => {
  viewerHandles.get(state.activeViewerId)?.handle?.requestFullscreen()
}

window._viewerCtrlAltDel = () => {
  viewerHandles.get(state.activeViewerId)?.handle?.sendSpecialCombo('ctrlAltDel')
}

window._viewerClipboardSync = async () => {
  try {
    const text = await navigator.clipboard.readText()
    viewerHandles.get(state.activeViewerId)?.handle?.sendClipboard(text || '')
    toast(t('toast.clipboardSynced'), 'ok')
  } catch {
    toast(t('toast.clipboardDenied'), 'err')
  }
}

// ==========================================================================
// connect / quick connect
// ==========================================================================

window._connect = async (id) => {
  const conn = state.connections.find((c) => c.id === id)
  if (!conn) return
  // Already have a live (or connecting) session for this saved connection?
  // Focus its tab instead of starting a duplicate.
  const existing = state.viewers.find((v) => v.connId === id && !v.cancelled)
  if (existing) {
    state.activeViewerId = existing.localId
    state.page = 'viewer'
    render()
    return
  }
  startConnect(
    { connId: id, name: conn.name || conn.host, protocol: conn.protocol || 'vnc', host: conn.host, port: conn.port },
    Connect(id)
  )
  try {
    state.connections = (await ListConnections()) || []
    if (state.page !== 'viewer') render()
  } catch {
    /* non-fatal */
  }
}

window._openQuickConnect = () => {
  state.modal = { type: 'quickConnect' }
  renderModal()
}

// Shared by both modals: switching the VNC/RDP segmented control snapshots
// the currently-typed fields, flips the protocol, and — only if the port
// still matches the previous protocol's default — swaps it to the new
// protocol's default too, then re-renders (which updates the Username
// field's placeholder/hint for the new protocol — the field itself is
// always shown, since VNC needs it too for macOS Screen Sharing's
// account-login mode, see usernamePlaceholderFor).
window._setModalProtocol = (prefix, protocol) => {
  const host = $(`#${prefix}-host`)?.value.trim() || ''
  const portRaw = $(`#${prefix}-port`)?.value.trim() || ''
  const username = $(`#${prefix}-user`)?.value.trim() || ''
  const password = $(`#${prefix}-pass`)?.value || ''
  const prevProtocol = state.modal.draft?.protocol || (prefix === 'f' && state.modal.id ? state.connections.find((c) => c.id === state.modal.id)?.protocol : null) || 'vnc'
  const port = portRaw === '' || parseInt(portRaw, 10) === defaultPortFor(prevProtocol) ? String(defaultPortFor(protocol)) : portRaw
  const name = prefix === 'f' ? $('#f-name')?.value.trim() || '' : undefined
  const colorTag = prefix === 'f' ? document.getElementById('f-colortag-picker')?.dataset.value || '' : undefined
  state.modal.draft = { protocol, name, host, port, username, password, colorTag }
  renderModal()
}

window._quickConnect = async () => {
  const protocol = state.modal.draft?.protocol || 'vnc'
  const host = $('#qc-host').value.trim()
  const portRaw = $('#qc-port').value.trim()
  const port = parseInt(portRaw, 10)
  const username = $('#qc-user').value.trim()
  const password = $('#qc-pass').value
  if (!host || !Number.isFinite(port) || port <= 0) {
    state.modal.error = t('form.validationHostPort')
    state.modal.draft = { protocol, host, port: portRaw, username, password }
    renderModal()
    return
  }
  state.modal = null
  startConnect({ connId: null, name: host, protocol, host, port }, QuickConnect(protocol, host, port, username, password))
}

// ==========================================================================
// connection manager CRUD
// ==========================================================================

window._openAddConnection = () => {
  state.modal = { type: 'connForm', id: null }
  renderModal()
}

window._openEditConnection = (id) => {
  state.modal = { type: 'connForm', id }
  renderModal()
}

window._saveConnection = async (id) => {
  const existing = id ? state.connections.find((c) => c.id === id) : null
  const protocol = state.modal.draft?.protocol || existing?.protocol || 'vnc'
  const name = $('#f-name').value.trim()
  const host = $('#f-host').value.trim()
  const portRaw = $('#f-port').value.trim()
  const port = parseInt(portRaw, 10)
  const username = $('#f-user').value.trim()
  const password = $('#f-pass').value
  const colorTag = document.getElementById('f-colortag-picker')?.dataset.value || ''
  if (!host || !Number.isFinite(port) || port <= 0) {
    state.modal.error = t('form.validationHostPort')
    state.modal.draft = { protocol, name, host, port: portRaw, username, password, colorTag }
    renderModal()
    return
  }
  const conn = {
    id: id || '',
    name: name || host,
    protocol,
    host,
    port,
    username,
    colorTag,
    lastUsed: existing?.lastUsed || '',
  }
  try {
    await SaveConnection(conn, password)
    state.connections = (await ListConnections()) || []
    state.modal = null
    render()
    toast(id ? t('toast.connectionUpdated') : t('toast.connectionAdded'), 'ok')
  } catch (e) {
    state.modal.error = (e && (e.message || e.toString())) || t('toast.saveConnectionFailed')
    renderModal()
  }
}

window._deleteConnection = async (id) => {
  if (!confirm(t('toast.deleteConfirm'))) return
  try {
    await DeleteConnection(id)
    state.connections = state.connections.filter((c) => c.id !== id)
    render()
    toast(t('toast.connectionDeleted'), 'ok')
  } catch (e) {
    toast((e && (e.message || e.toString())) || t('toast.deleteConnectionFailed'), 'err')
  }
}

// ==========================================================================
// settings / about / external links
// ==========================================================================

window._setTheme = async (theme) => {
  state.theme = theme
  applyTheme(theme)
  render()
  try {
    await SetTheme(theme)
  } catch {
    toast(t('toast.themeSaveFailed'), 'err')
  }
}

// Only affects VNC sessions started after this changes — see
// internal/rfb's qualityPresetLevels for what each preset actually asks
// the server for.
window._setVNCQuality = async (quality) => {
  state.vncQuality = quality
  render()
  try {
    await SetVNCQuality(quality)
  } catch {
    toast(t('toast.qualitySaveFailed'), 'err')
  }
}

window._setLanguage = async (code) => {
  setLocale(code)
  render()
  try {
    await SetLanguage(code)
  } catch {
    toast(t('toast.languageSaveFailed'), 'err')
  }
}

window._exportConnections = async () => {
  try {
    const path = await ExportConnections()
    if (path) toast(t('toast.exportedTo', { path }), 'ok')
  } catch (e) {
    toast((e && (e.message || e.toString())) || t('toast.exportFailed'), 'err')
  }
}

window._importConnections = async () => {
  try {
    const count = await ImportConnections()
    if (count > 0) {
      state.connections = (await ListConnections()) || []
      render()
      toast(tn('toast.imported', count), 'ok')
    }
  } catch (e) {
    toast((e && (e.message || e.toString())) || t('toast.importFailed'), 'err')
  }
}

window._forgetCertificate = async (addr) => {
  if (!confirm(t('toast.forgetCertConfirm', { addr }))) return
  try {
    await ForgetCertificate(addr)
    state.trustedCerts = (await ListTrustedCertificates()) || []
    render()
    toast(t('toast.certForgotten'), 'ok')
  } catch (e) {
    toast((e && (e.message || e.toString())) || t('toast.forgetCertFailed'), 'err')
  }
}

window._openAbout = () => {
  state.modal = { type: 'about' }
  renderModal()
}

window._closeModal = () => {
  state.modal = null
  renderModal()
}

window._openGitHub = async () => {
  try {
    await OpenGitHub()
  } catch {
    /* best effort */
  }
}

window._openSupport = async () => {
  try {
    await OpenSupportLink()
  } catch {
    /* best effort */
  }
}

// ==========================================================================
// bootstrap
// ==========================================================================

function onStatusEvent(payload) {
  if (!payload) return
  const v = state.viewers.find((v) => v.sessionId === payload.sessionId)
  if (!v) return
  v.status = payload.state
  v.message = payload.message || ''
  updateSlotOverlay(v)
  if (state.page === 'viewer') renderViewerPage()
}

async function init() {
  document.getElementById('app').innerHTML = `
    <div id="view-root"></div>
    <div id="modal-root"></div>
    <div id="toast-wrap" class="toast-wrap"></div>
  `
  // Lives outside #view-root for the whole app lifetime — see the
  // `state.viewers` doc comment: this is what lets a session's canvas and
  // WebSocket survive both tab-switching and navigating away from the
  // viewer page.
  const canvasHost = document.createElement('div')
  canvasHost.id = 'viewer-canvas-host'
  canvasHost.className = 'viewer-canvas-host'
  canvasHost.style.display = 'none'
  document.body.appendChild(canvasHost)

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && state.modal) window._closeModal()
  })

  EventsOn('lupinus:status', onStatusEvent)

  // Set before the first render so nothing flashes in the wrong language:
  // detectLocale() reads the OS/webview language synchronously (no Go
  // round-trip needed), and the real saved preference — which wins once
  // it arrives — is usually already cached from a prior launch anyway.
  setLocale(detectLocale())

  // First paint follows the OS preference purely through CSS
  // (prefers-color-scheme) — no theme flash while GetTheme() is in flight.
  render()

  try {
    state.theme = await GetTheme()
  } catch {
    state.theme = 'system'
  }
  applyTheme(state.theme)

  try {
    state.vncQuality = await GetVNCQuality()
  } catch {
    state.vncQuality = 'balanced'
  }

  try {
    const lang = await GetLanguage()
    if (lang) setLocale(lang)
  } catch {
    /* keep the OS-detected locale */
  }
  render()

  try {
    state.connections = (await ListConnections()) || []
  } catch {
    toast(t('toast.loadConnectionsFailed'), 'err')
  }
  try {
    state.trustedCerts = (await ListTrustedCertificates()) || []
  } catch {
    /* non-fatal: the card just stays hidden */
  }
  if (state.page !== 'viewer') render()

  try {
    state.about = await GetAbout()
  } catch {
    state.about = { name: 'Lupinus', version: '', author: 'Alperen Yavuz', repo: '', support: '' }
  }
  if (state.modal?.type === 'about') renderModal()
}

init()
