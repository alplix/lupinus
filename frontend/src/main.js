import './style.css'
import { openViewer } from './viewer.js'
import {
  GetAbout,
  ListConnections,
  SaveConnection,
  DeleteConnection,
  GetTheme,
  SetTheme,
  Connect,
  QuickConnect,
  Disconnect,
  OpenSupportLink,
  OpenGitHub,
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
  about: null,
  modal: null, // { type: 'about' | 'connForm' | 'quickConnect', ...fields }
  toasts: [],
  viewer: null, // populated while page === 'viewer', see goToViewer()
}

let viewerHandle = null // handle returned by openViewer(), while mounted
let viewerMounted = false // true once the canvas is live for state.viewer
let viewerToken = 0 // guards against a stale Connect()/QuickConnect() reply
// landing after the user has already navigated away from the viewer page

// ==========================================================================
// formatting helpers
// ==========================================================================

function fmtLastUsed(iso) {
  if (!iso) return 'Never connected'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return 'Never connected'
  const diff = (Date.now() - t) / 1000
  if (diff < 60) return 'Just now'
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  if (diff < 604800) return `${Math.floor(diff / 86400)}d ago`
  return new Date(iso).toLocaleDateString()
}

function statusLabel(s) {
  return (
    {
      connected: 'Connected',
      connecting: 'Connecting…',
      reconnecting: 'Reconnecting…',
      error: 'Connection error',
      disconnected: 'Not connected',
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
  if (state.page === 'viewer' && state.viewer) {
    renderViewerPage()
  } else {
    viewerMounted = false
    renderShellPage()
  }
  renderModal()
  renderToasts()
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
          <button class="${state.page === 'connections' ? 'active' : ''}" onclick="window._nav('connections')">Connections</button>
          <button class="${state.page === 'settings' ? 'active' : ''}" onclick="window._nav('settings')">Settings</button>
        </nav>
        <div class="header-spacer"></div>
        <button class="icon-btn" title="About Lupinus" onclick="window._openAbout()">${INFO_ICON}</button>
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
  if (state.viewer && state.viewer.connId === conn.id) return state.viewer.status
  return 'disconnected'
}

function renderConnCard(c) {
  const statusClass = statusClassFor(c)
  return `
    <div class="conn-card">
      <span class="status-dot ${esc(statusClass)}" title="${attr(statusLabel(statusClass))}"></span>
      <div class="conn-info">
        <div class="conn-name trunc">${esc(c.name || c.host)}</div>
        <div class="conn-meta">
          <span class="num">${esc(c.host)}:${esc(String(c.port))}</span>
          <span>${esc(fmtLastUsed(c.lastUsed))}</span>
        </div>
      </div>
      <div class="conn-actions">
        <button class="btn sm primary" onclick="window._connect('${attr(c.id)}')">Connect</button>
        <button class="btn sm ghost" onclick="window._openEditConnection('${attr(c.id)}')">Edit</button>
        <button class="btn sm ghost danger" onclick="window._deleteConnection('${attr(c.id)}')">Delete</button>
      </div>
    </div>
  `
}

function renderEmptyState() {
  return `
    <div class="empty-state">
      <div class="brand-mark">${BRAND_MARK_SVG}</div>
      <div class="empty-wordmark">Lupinus</div>
      <h2>No connections yet</h2>
      <p>Connect to a VNC server to get started.</p>
      <button class="btn primary" onclick="window._openAddConnection()">New Connection</button>
    </div>
  `
}

function renderConnectionsPage() {
  const list = state.connections
  return `
    <div class="page-heading">
      <h1>Connections</h1>
      <p>${list.length} saved connection${list.length === 1 ? '' : 's'}</p>
    </div>
    <div class="row" style="justify-content:flex-end;">
      <button class="btn" onclick="window._openQuickConnect()">Quick Connect</button>
      <button class="btn primary" onclick="window._openAddConnection()">+ New Connection</button>
    </div>
    ${list.length === 0 ? renderEmptyState() : `<div class="conn-list">${list.map(renderConnCard).join('')}</div>`}
    <footer class="app-footer">
      <span class="footer-credit"><b>Lupinus</b> · Coded by Alperen Yavuz</span>
      <a class="link" href="#" onclick="window._openSupport();return false;">Support Lupinus</a>
    </footer>
  `
}

// ---- settings page ----

function renderSettingsPage() {
  const theme = state.theme
  return `
    <div class="page-heading">
      <h1>Settings</h1>
      <p>Preferences for Lupinus</p>
    </div>
    <div class="settings-card">
      <h2>Appearance</h2>
      <div class="theme-options">
        <button class="${theme === 'light' ? 'active' : ''}" onclick="window._setTheme('light')">Light</button>
        <button class="${theme === 'dark' ? 'active' : ''}" onclick="window._setTheme('dark')">Dark</button>
        <button class="${theme === 'system' ? 'active' : ''}" onclick="window._setTheme('system')">System</button>
      </div>
    </div>
    <div class="settings-card">
      <h2>About</h2>
      <p class="soft" style="margin:0;font-size:13px;">Lupinus — Native VNC Client</p>
      <div class="row">
        <button class="btn sm" onclick="window._openAbout()">About Lupinus</button>
        <button class="btn sm ghost" onclick="window._openGitHub()">GitHub</button>
      </div>
    </div>
    <div class="settings-card support-card">
      <div class="support-wordmark">Lupinus</div>
      <div class="support-credit">Coded by Alperen Yavuz</div>
      <div class="support-prompt">Enjoying Lupinus?<br>Support the project</div>
      <button class="btn primary sm" onclick="window._openSupport()">Support Lupinus</button>
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
          <div class="about-subtitle">Native VNC Client</div>
          <div class="about-line">Coded by Alperen Yavuz</div>
          <div class="about-version">${a.version ? `v${esc(a.version)}` : ''}</div>
          <div class="about-actions">
            <button class="btn sm" onclick="window._openGitHub()">GitHub</button>
            <button class="btn sm primary" onclick="window._openSupport()">Support Lupinus</button>
          </div>
          <div class="about-copyright">© 2026 Alperen Yavuz</div>
        </div>
      </div>
    </div>
  `
}

function renderConnFormModal(m) {
  const editing = !!m.id
  const c = editing ? state.connections.find((x) => x.id === m.id) : null
  // On a validation error we re-render this modal from scratch — pull the
  // user's in-progress input back in instead of resetting to saved values.
  const d = m.draft
  const nameVal = d ? d.name : c?.name || ''
  const hostVal = d ? d.host : c?.host || ''
  const portVal = d ? d.port : String(c?.port || 5900)
  const passVal = d ? d.password : ''
  return `
    <div class="modal-overlay">
      <div class="modal">
        <div class="modal-head">
          <h3>${editing ? 'Edit Connection' : 'New Connection'}</h3>
          <button class="icon-btn" onclick="window._closeModal()">${CLOSE_ICON}</button>
        </div>
        ${m.error ? `<div class="modal-error">${esc(m.error)}</div>` : ''}
        <div class="field">
          <label>Name</label>
          <input id="f-name" value="${attr(nameVal)}" placeholder="My server">
        </div>
        <div class="field-row">
          <div class="field">
            <label>Host</label>
            <input id="f-host" value="${attr(hostVal)}" placeholder="192.168.1.10">
          </div>
          <div class="field" style="max-width:110px;">
            <label>Port</label>
            <input id="f-port" value="${attr(portVal)}" inputmode="numeric">
          </div>
        </div>
        <div class="field">
          <label>Password</label>
          <input id="f-pass" type="password" value="${attr(passVal)}" placeholder="${editing ? 'Leave blank to keep current' : 'Optional'}">
          ${editing ? '<div class="field-hint">Leave blank to keep the stored credential.</div>' : ''}
        </div>
        <div class="modal-foot">
          <button class="btn" onclick="window._closeModal()">Cancel</button>
          <button class="btn primary" onclick="window._saveConnection('${editing ? attr(m.id) : ''}')">${editing ? 'Save' : 'Add'}</button>
        </div>
      </div>
    </div>
  `
}

function renderQuickConnectModal(m) {
  const d = m.draft
  const hostVal = d ? d.host : ''
  const portVal = d ? d.port : '5900'
  const passVal = d ? d.password : ''
  return `
    <div class="modal-overlay">
      <div class="modal">
        <div class="modal-head">
          <h3>Quick Connect</h3>
          <button class="icon-btn" onclick="window._closeModal()">${CLOSE_ICON}</button>
        </div>
        ${m.error ? `<div class="modal-error">${esc(m.error)}</div>` : ''}
        <div class="field-row">
          <div class="field">
            <label>Host</label>
            <input id="qc-host" value="${attr(hostVal)}" placeholder="192.168.1.10">
          </div>
          <div class="field" style="max-width:110px;">
            <label>Port</label>
            <input id="qc-port" value="${attr(portVal)}" inputmode="numeric">
          </div>
        </div>
        <div class="field">
          <label>Password</label>
          <input id="qc-pass" type="password" value="${attr(passVal)}" placeholder="Optional">
        </div>
        <div class="modal-foot">
          <button class="btn" onclick="window._closeModal()">Cancel</button>
          <button class="btn primary" onclick="window._quickConnect()">Connect</button>
        </div>
      </div>
    </div>
  `
}

// ==========================================================================
// viewer page
// ==========================================================================

function goToViewer(info) {
  const token = ++viewerToken
  state.viewer = {
    token,
    connId: info.connId || null,
    name: info.name || '',
    host: info.host,
    port: info.port,
    sessionId: null,
    bridgeUrl: null,
    status: 'connecting',
    message: '',
    scalingMode: 'fit',
  }
  viewerMounted = false
  viewerHandle = null
  state.page = 'viewer'
  render()
  return token
}

function renderViewerPage() {
  if (viewerMounted) {
    // The canvas + its live WebSocket already live inside #viewer-canvas-area
    // — never rewrite the shell's innerHTML while mounted, only patch chrome.
    updateViewerChrome()
    return
  }
  const root = $('#view-root')
  const v = state.viewer
  root.innerHTML = `
    <div class="viewer-shell">
      <div class="viewer-toolbar">
        <button class="btn sm ghost" onclick="window._viewerLeave()">${BACK_ICON}Back</button>
        <span class="viewer-title trunc">${esc(v.name || `${v.host}:${v.port}`)}</span>
        <div class="spacer"></div>
        <select id="viewer-scaling" onchange="window._viewerSetScaling(this.value)">
          <option value="fit" ${v.scalingMode === 'fit' ? 'selected' : ''}>Fit</option>
          <option value="actual" ${v.scalingMode === 'actual' ? 'selected' : ''}>Actual size</option>
          <option value="stretch" ${v.scalingMode === 'stretch' ? 'selected' : ''}>Stretch</option>
        </select>
        <button class="btn sm" onclick="window._viewerClipboardSync()">Sync Clipboard</button>
        <button class="btn sm" onclick="window._viewerCtrlAltDel()">Ctrl+Alt+Del</button>
        <button class="btn sm" onclick="window._viewerFullscreen()">Fullscreen</button>
        <button class="btn sm danger" onclick="window._viewerLeave()">Disconnect</button>
      </div>
      <div class="viewer-canvas-area" id="viewer-canvas-area">
        <div class="viewer-overlay" id="viewer-overlay"></div>
      </div>
      <div class="viewer-statusbar" id="viewer-statusbar"></div>
    </div>
  `
  updateViewerChrome()
}

// Patches the overlay + status bar in place, without touching the mounted
// canvas. Called on every lupinus:status event and connect resolution.
function updateViewerChrome() {
  const v = state.viewer
  if (!v || state.page !== 'viewer') return

  const overlay = $('#viewer-overlay')
  if (overlay) {
    if (v.status === 'connected') {
      overlay.style.display = 'none'
      overlay.innerHTML = ''
    } else {
      overlay.style.display = 'flex'
      if (v.status === 'error') {
        overlay.innerHTML = `
          <h2>Connection failed</h2>
          <p class="error-message">${esc(v.message || 'Could not connect to the server.')}</p>
          <button class="btn primary sm" onclick="window._viewerLeave()">Back to Connections</button>
        `
      } else if (v.status === 'disconnected') {
        overlay.innerHTML = `
          <h2>Disconnected</h2>
          <p>${esc(v.message || 'The session has ended.')}</p>
          <button class="btn primary sm" onclick="window._viewerLeave()">Back to Connections</button>
        `
      } else {
        overlay.innerHTML = `
          <div class="spinner"></div>
          <h2>${v.status === 'reconnecting' ? 'Reconnecting…' : 'Connecting…'}</h2>
          <p>${esc(`${v.host || ''}:${v.port || ''}`)}</p>
        `
      }
    }
  }

  const statusbar = $('#viewer-statusbar')
  if (statusbar) {
    statusbar.innerHTML = `
      <span class="status-dot ${esc(v.status)}"></span>
      <span>${esc(statusLabel(v.status))}</span>
      <span class="faint">· ${esc(`${v.host || ''}:${v.port || ''}`)}</span>
    `
  }
}

function mountViewerCanvas() {
  if (viewerMounted || !state.viewer?.bridgeUrl) return
  const container = $('#viewer-canvas-area')
  if (!container) return
  viewerHandle = openViewer({
    container,
    bridgeUrl: state.viewer.bridgeUrl,
    onSocketState: () => {
      // Raw WebSocket open/close/error — the RFB-level lupinus:status event
      // is the authoritative source for the overlay/status bar, so this is
      // intentionally a no-op beyond what that event already drives.
    },
    onCutText: (text) => {
      navigator.clipboard?.writeText?.(text).catch(() => {})
    },
  })
  viewerMounted = true
}

function onConnectResolved(result) {
  if (!state.viewer) return
  state.viewer.sessionId = result.sessionId
  state.viewer.bridgeUrl = result.bridgeUrl
  if (state.viewer.status !== 'error') {
    state.viewer.status = 'connected'
    state.viewer.message = ''
  }
  if (state.page === 'viewer') {
    render() // first render after bridgeUrl is known — creates #viewer-canvas-area
    mountViewerCanvas()
    updateViewerChrome()
  }
}

function onConnectFailed(e) {
  if (!state.viewer) return
  state.viewer.status = 'error'
  state.viewer.message = (e && (e.message || e.toString())) || 'Connection failed'
  updateViewerChrome()
}

async function teardownViewer() {
  const sessionId = state.viewer?.sessionId
  try {
    viewerHandle?.close()
  } catch {
    /* already closed */
  }
  viewerHandle = null
  viewerMounted = false
  if (sessionId) {
    try {
      await Disconnect(sessionId)
    } catch {
      /* best effort */
    }
  }
}

window._viewerLeave = async () => {
  await teardownViewer()
  state.viewer = null
  state.page = 'connections'
  try {
    state.connections = (await ListConnections()) || []
  } catch {
    /* keep the existing list on failure */
  }
  render()
}

window._viewerSetScaling = (mode) => {
  if (state.viewer) state.viewer.scalingMode = mode
  viewerHandle?.setScalingMode(mode)
}

window._viewerFullscreen = () => {
  viewerHandle?.requestFullscreen()
}

window._viewerCtrlAltDel = () => {
  viewerHandle?.sendSpecialCombo('ctrlAltDel')
}

window._viewerClipboardSync = async () => {
  try {
    const text = await navigator.clipboard.readText()
    viewerHandle?.sendClipboard(text || '')
    toast('Clipboard synced', 'ok')
  } catch {
    toast('Clipboard access was denied', 'err')
  }
}

// ==========================================================================
// connect / quick connect
// ==========================================================================

window._connect = async (id) => {
  const conn = state.connections.find((c) => c.id === id)
  if (!conn) return
  const token = goToViewer({ connId: id, name: conn.name || conn.host, host: conn.host, port: conn.port })
  try {
    const result = await Connect(id)
    if (state.viewer?.token !== token) {
      // User navigated away while this was in flight — clean up the
      // orphaned session instead of surfacing it.
      try {
        await Disconnect(result.sessionId)
      } catch {
        /* best effort */
      }
      return
    }
    onConnectResolved(result)
    try {
      state.connections = (await ListConnections()) || []
    } catch {
      /* non-fatal */
    }
  } catch (e) {
    if (state.viewer?.token === token) onConnectFailed(e)
  }
}

window._openQuickConnect = () => {
  state.modal = { type: 'quickConnect' }
  renderModal()
}

window._quickConnect = async () => {
  const host = $('#qc-host').value.trim()
  const portRaw = $('#qc-port').value.trim()
  const port = parseInt(portRaw, 10)
  const password = $('#qc-pass').value
  if (!host || !Number.isFinite(port) || port <= 0) {
    state.modal.error = 'Host and a valid port are required.'
    state.modal.draft = { host, port: portRaw, password }
    renderModal()
    return
  }
  state.modal = null
  const token = goToViewer({ connId: null, name: host, host, port })
  try {
    const result = await QuickConnect(host, port, password)
    if (state.viewer?.token !== token) {
      try {
        await Disconnect(result.sessionId)
      } catch {
        /* best effort */
      }
      return
    }
    onConnectResolved(result)
  } catch (e) {
    if (state.viewer?.token === token) onConnectFailed(e)
  }
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
  const name = $('#f-name').value.trim()
  const host = $('#f-host').value.trim()
  const portRaw = $('#f-port').value.trim()
  const port = parseInt(portRaw, 10)
  const password = $('#f-pass').value
  if (!host || !Number.isFinite(port) || port <= 0) {
    state.modal.error = 'Host and a valid port are required.'
    state.modal.draft = { name, host, port: portRaw, password }
    renderModal()
    return
  }
  const existing = id ? state.connections.find((c) => c.id === id) : null
  const conn = {
    id: id || '',
    name: name || host,
    host,
    port,
    colorTag: existing?.colorTag || '',
    lastUsed: existing?.lastUsed || '',
  }
  try {
    await SaveConnection(conn, password)
    state.connections = (await ListConnections()) || []
    state.modal = null
    render()
    toast(id ? 'Connection updated' : 'Connection added', 'ok')
  } catch (e) {
    state.modal.error = (e && (e.message || e.toString())) || 'Could not save connection'
    renderModal()
  }
}

window._deleteConnection = async (id) => {
  if (!confirm('Delete this connection?')) return
  try {
    await DeleteConnection(id)
    state.connections = state.connections.filter((c) => c.id !== id)
    render()
    toast('Connection deleted', 'ok')
  } catch (e) {
    toast((e && (e.message || e.toString())) || 'Failed to delete connection', 'err')
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
    toast('Could not save the theme preference', 'err')
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
  if (!payload || !state.viewer || payload.sessionId !== state.viewer.sessionId) return
  state.viewer.status = payload.state
  state.viewer.message = payload.message || ''
  updateViewerChrome()
}

async function init() {
  document.getElementById('app').innerHTML = `
    <div id="view-root"></div>
    <div id="modal-root"></div>
    <div id="toast-wrap" class="toast-wrap"></div>
  `

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && state.modal) window._closeModal()
  })

  EventsOn('lupinus:status', onStatusEvent)

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
    state.connections = (await ListConnections()) || []
  } catch {
    toast('Could not load saved connections', 'err')
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
