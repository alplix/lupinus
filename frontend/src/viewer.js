// The VNC viewer: owns the framebuffer WebSocket (opened directly against
// the loopback wsbridge server, bypassing Wails' JSON binding bridge — see
// internal/wsbridge's package doc for why) and renders it to a canvas.
//
// Binary frame protocol — keep in sync with internal/wsbridge/session.go:
//   Server -> client: 0x01 Init(w,h) · 0x02 Update(x,y,w,h,rgba) ·
//                      0x03 CopyRect(dstX,dstY,w,h,srcX,srcY) ·
//                      0x04 Resize(w,h) · 0x05 Cursor(hotX,hotY,w,h,rgba) ·
//                      0x06 CutText(text)
//   Client -> server: 0x10 Pointer(x,y,buttonMask) ·
//                      0x11 Key(keysym,down) · 0x12 Clipboard(text)

import { keysymFor, SPECIAL_COMBOS as VNC_SPECIAL_COMBOS } from './keysym.js';
import { scancodeFor, SPECIAL_COMBOS as RDP_SPECIAL_COMBOS } from './rdp-scancode.js';

const FRAME_INIT = 0x01;
const FRAME_UPDATE = 0x02;
const FRAME_COPYRECT = 0x03;
const FRAME_RESIZE = 0x04;
const FRAME_CURSOR = 0x05;
const FRAME_CUTTEXT = 0x06;

const FRAME_IN_POINTER = 0x10;
const FRAME_IN_KEY = 0x11;
const FRAME_IN_CLIPBOARD = 0x12;

/**
 * Opens a viewer session.
 * @param {Object} opts
 * @param {HTMLElement} opts.container - element the canvas is mounted into.
 * @param {string} opts.bridgeUrl - ws://127.0.0.1:PORT/fb?session=ID
 * @param {'vnc'|'rdp'} [opts.protocol] - selects the keyboard mapping table
 *   (X11 keysyms vs. PC/AT scancodes); defaults to 'vnc'.
 * @param {(state: 'open'|'closed'|'error', detail?: string) => void} [opts.onSocketState]
 * @param {(text: string) => void} [opts.onCutText] - server pushed clipboard text.
 * @returns {{ setScalingMode: (mode: 'fit'|'actual'|'stretch') => void,
 *             sendClipboard: (text: string) => void,
 *             sendSpecialCombo: (name: string) => void,
 *             requestFullscreen: () => void,
 *             close: () => void }}
 */
export function openViewer({ container, bridgeUrl, protocol = 'vnc', onSocketState, onCutText }) {
	const keyFor = protocol === 'rdp' ? scancodeFor : keysymFor;
	const SPECIAL_COMBOS = protocol === 'rdp' ? RDP_SPECIAL_COMBOS : VNC_SPECIAL_COMBOS;
	const canvas = document.createElement('canvas');
	canvas.className = 'lp-viewer-canvas';
	canvas.tabIndex = 0;
	container.appendChild(canvas);
	const ctx = canvas.getContext('2d', { alpha: false });

	let scalingMode = 'fit';
	let fbWidth = 0;
	let fbHeight = 0;
	let closed = false;

	const ws = new WebSocket(bridgeUrl);
	ws.binaryType = 'arraybuffer';

	ws.addEventListener('open', () => onSocketState?.('open'));
	ws.addEventListener('close', () => {
		if (!closed) onSocketState?.('closed');
	});
	ws.addEventListener('error', () => onSocketState?.('error'));
	ws.addEventListener('message', (ev) => handleFrame(ev.data));

	function handleFrame(buf) {
		const view = new DataView(buf);
		const type = view.getUint8(0);

		switch (type) {
			case FRAME_INIT: {
				fbWidth = view.getUint16(1);
				fbHeight = view.getUint16(3);
				canvas.width = fbWidth;
				canvas.height = fbHeight;
				applyScaling();
				break;
			}
			case FRAME_UPDATE: {
				const x = view.getUint16(1);
				const y = view.getUint16(3);
				const w = view.getUint16(5);
				const h = view.getUint16(7);
				const pixels = new Uint8ClampedArray(buf, 9, w * h * 4);
				ctx.putImageData(new ImageData(pixels, w, h), x, y);
				break;
			}
			case FRAME_COPYRECT: {
				const dstX = view.getUint16(1);
				const dstY = view.getUint16(3);
				const w = view.getUint16(5);
				const h = view.getUint16(7);
				const srcX = view.getUint16(9);
				const srcY = view.getUint16(11);
				ctx.drawImage(canvas, srcX, srcY, w, h, dstX, dstY, w, h);
				break;
			}
			case FRAME_RESIZE: {
				fbWidth = view.getUint16(1);
				fbHeight = view.getUint16(3);
				const snapshot = ctx.getImageData(0, 0, canvas.width, canvas.height);
				canvas.width = fbWidth;
				canvas.height = fbHeight;
				ctx.putImageData(snapshot, 0, 0);
				applyScaling();
				break;
			}
			case FRAME_CURSOR: {
				const hotX = view.getUint16(1);
				const hotY = view.getUint16(3);
				const w = view.getUint16(5);
				const h = view.getUint16(7);
				applyCursor(buf, hotX, hotY, w, h);
				break;
			}
			case FRAME_CUTTEXT: {
				const len = view.getUint32(1);
				const text = new TextDecoder().decode(new Uint8Array(buf, 5, len));
				onCutText?.(text);
				break;
			}
		}
	}

	function applyCursor(buf, hotX, hotY, w, h) {
		if (w === 0 || h === 0) {
			canvas.style.cursor = 'default';
			return;
		}
		const off = document.createElement('canvas');
		off.width = w;
		off.height = h;
		const octx = off.getContext('2d');
		const pixels = new Uint8ClampedArray(buf, 9, w * h * 4);
		octx.putImageData(new ImageData(pixels, w, h), 0, 0);
		off.toBlob((blob) => {
			if (!blob) return;
			const url = URL.createObjectURL(blob);
			canvas.style.cursor = `url(${url}) ${hotX} ${hotY}, auto`;
			// Revoke shortly after the browser has had a chance to fetch it.
			setTimeout(() => URL.revokeObjectURL(url), 2000);
		});
	}

	// --- scaling ---

	function applyScaling() {
		if (!fbWidth || !fbHeight) return;
		if (scalingMode === 'actual') {
			canvas.style.width = `${fbWidth}px`;
			canvas.style.height = `${fbHeight}px`;
			return;
		}
		if (scalingMode === 'stretch') {
			canvas.style.width = '100%';
			canvas.style.height = '100%';
			return;
		}
		// 'fit': scale to the container while preserving aspect ratio. This
		// is the default specifically so a server with a huge/Retina
		// framebuffer (common connecting to macOS, which reports the
		// physical pixel size — e.g. 2880x1800 on a 13" MacBook Pro, not
		// the logical 1440x900) never shows "gigantic" the way many older
		// VNC clients do when they default to 1:1 actual size.
		const cw = container.clientWidth;
		const ch = container.clientHeight;
		if (cw <= 0 || ch <= 0) {
			// Container isn't laid out yet (e.g. this ran before the
			// first paint). Bail without touching canvas size — falling
			// back to a bogus scale of 1 here would briefly render at
			// full native resolution, exactly the bug this mode exists
			// to avoid. The ResizeObserver below re-invokes this as soon
			// as the container gets a real size.
			return;
		}
		const scale = Math.min(cw / fbWidth, ch / fbHeight, 1);
		canvas.style.width = `${Math.round(fbWidth * scale)}px`;
		canvas.style.height = `${Math.round(fbHeight * scale)}px`;
	}

	const resizeObserver = new ResizeObserver(() => {
		if (scalingMode === 'fit') applyScaling();
	});
	resizeObserver.observe(container);

	function setScalingMode(mode) {
		scalingMode = mode;
		applyScaling();
	}

	// --- outgoing: pointer / keyboard / clipboard ---

	function sendBinary(buf) {
		if (ws.readyState === WebSocket.OPEN) ws.send(buf);
	}

	function sendPointer(x, y, buttonMask) {
		const buf = new ArrayBuffer(6);
		const v = new DataView(buf);
		v.setUint8(0, FRAME_IN_POINTER);
		v.setUint16(1, Math.max(0, Math.min(0xffff, Math.round(x))));
		v.setUint16(3, Math.max(0, Math.min(0xffff, Math.round(y))));
		v.setUint8(5, buttonMask);
		sendBinary(buf);
	}

	function sendKey(keysym, down) {
		const buf = new ArrayBuffer(6);
		const v = new DataView(buf);
		v.setUint8(0, FRAME_IN_KEY);
		v.setUint32(1, keysym >>> 0);
		v.setUint8(5, down ? 1 : 0);
		sendBinary(buf);
	}

	function sendClipboard(text) {
		const body = new TextEncoder().encode(text);
		const buf = new ArrayBuffer(5 + body.length);
		const v = new DataView(buf);
		v.setUint8(0, FRAME_IN_CLIPBOARD);
		v.setUint32(1, body.length);
		new Uint8Array(buf, 5).set(body);
		sendBinary(buf);
	}

	function sendSpecialCombo(name) {
		const keysyms = SPECIAL_COMBOS[name];
		if (!keysyms) return;
		for (const ks of keysyms) sendKey(ks, true);
		for (const ks of [...keysyms].reverse()) sendKey(ks, false);
	}

	function toFramebufferCoords(clientX, clientY) {
		const rect = canvas.getBoundingClientRect();
		const scaleX = canvas.width / rect.width;
		const scaleY = canvas.height / rect.height;
		return [(clientX - rect.left) * scaleX, (clientY - rect.top) * scaleY];
	}

	let buttonMask = 0;
	const BUTTON_BITS = { 0: 1, 1: 2, 2: 4 };

	// A mouse (or a high-polling-rate one) fires mousemove far faster than a
	// remote screen can use it — measured well over 200/s — and every event
	// is a wire message the server has to read and act on. Coalesce moves to
	// at most one per animation frame, always carrying the newest position;
	// anything with a button change flushes immediately so clicks never land
	// at a stale position or get reordered against a queued move.
	let pendingMove = null;
	let moveFrame = 0;

	function flushMove() {
		moveFrame = 0;
		if (pendingMove) {
			const [x, y] = pendingMove;
			pendingMove = null;
			sendPointer(x, y, buttonMask);
		}
	}
	function dropPendingMove() {
		pendingMove = null;
		if (moveFrame) {
			cancelAnimationFrame(moveFrame);
			moveFrame = 0;
		}
	}

	function onPointerMove(ev) {
		pendingMove = toFramebufferCoords(ev.clientX, ev.clientY);
		if (!moveFrame) moveFrame = requestAnimationFrame(flushMove);
	}
	function onPointerDown(ev) {
		canvas.focus();
		dropPendingMove();
		buttonMask |= BUTTON_BITS[ev.button] ?? 0;
		const [x, y] = toFramebufferCoords(ev.clientX, ev.clientY);
		sendPointer(x, y, buttonMask);
		ev.preventDefault();
	}
	function onPointerUp(ev) {
		dropPendingMove();
		buttonMask &= ~(BUTTON_BITS[ev.button] ?? 0);
		const [x, y] = toFramebufferCoords(ev.clientX, ev.clientY);
		sendPointer(x, y, buttonMask);
		ev.preventDefault();
	}
	function onWheel(ev) {
		dropPendingMove();
		const [x, y] = toFramebufferCoords(ev.clientX, ev.clientY);
		const bit = ev.deltaY < 0 ? 8 : 16; // wheel up / wheel down
		sendPointer(x, y, buttonMask | bit);
		sendPointer(x, y, buttonMask);
		ev.preventDefault();
	}
	function onContextMenu(ev) {
		ev.preventDefault();
	}
	function onKeyDown(ev) {
		const ks = keyFor(ev);
		if (ks !== null) {
			sendKey(ks, true);
			ev.preventDefault();
		}
	}
	function onKeyUp(ev) {
		const ks = keyFor(ev);
		if (ks !== null) {
			sendKey(ks, false);
			ev.preventDefault();
		}
	}

	canvas.addEventListener('mousemove', onPointerMove);
	canvas.addEventListener('mousedown', onPointerDown);
	canvas.addEventListener('mouseup', onPointerUp);
	canvas.addEventListener('wheel', onWheel, { passive: false });
	canvas.addEventListener('contextmenu', onContextMenu);
	canvas.addEventListener('keydown', onKeyDown);
	canvas.addEventListener('keyup', onKeyUp);

	function requestFullscreen() {
		container.requestFullscreen?.();
	}

	function close() {
		if (closed) return;
		closed = true;
		dropPendingMove();
		resizeObserver.disconnect();
		canvas.removeEventListener('mousemove', onPointerMove);
		canvas.removeEventListener('mousedown', onPointerDown);
		canvas.removeEventListener('mouseup', onPointerUp);
		canvas.removeEventListener('wheel', onWheel);
		canvas.removeEventListener('contextmenu', onContextMenu);
		canvas.removeEventListener('keydown', onKeyDown);
		canvas.removeEventListener('keyup', onKeyUp);
		try {
			ws.close();
		} catch {
			/* already closed */
		}
		canvas.remove();
	}

	return { setScalingMode, sendClipboard, sendSpecialCombo, requestFullscreen, close };
}
