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

import { keysymFor, SPECIAL_COMBOS } from './keysym.js';

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
 * @param {(state: 'open'|'closed'|'error', detail?: string) => void} [opts.onSocketState]
 * @param {(text: string) => void} [opts.onCutText] - server pushed clipboard text.
 * @returns {{ setScalingMode: (mode: 'fit'|'actual'|'stretch') => void,
 *             sendClipboard: (text: string) => void,
 *             sendSpecialCombo: (name: keyof typeof SPECIAL_COMBOS) => void,
 *             requestFullscreen: () => void,
 *             close: () => void }}
 */
export function openViewer({ container, bridgeUrl, onSocketState, onCutText }) {
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
		// 'fit': scale to the container while preserving aspect ratio.
		const cw = container.clientWidth;
		const ch = container.clientHeight;
		const scale = Math.min(cw / fbWidth, ch / fbHeight, 1) || 1;
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

	function onPointerMove(ev) {
		const [x, y] = toFramebufferCoords(ev.clientX, ev.clientY);
		sendPointer(x, y, buttonMask);
	}
	function onPointerDown(ev) {
		canvas.focus();
		buttonMask |= BUTTON_BITS[ev.button] ?? 0;
		const [x, y] = toFramebufferCoords(ev.clientX, ev.clientY);
		sendPointer(x, y, buttonMask);
		ev.preventDefault();
	}
	function onPointerUp(ev) {
		buttonMask &= ~(BUTTON_BITS[ev.button] ?? 0);
		const [x, y] = toFramebufferCoords(ev.clientX, ev.clientY);
		sendPointer(x, y, buttonMask);
		ev.preventDefault();
	}
	function onWheel(ev) {
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
		const ks = keysymFor(ev);
		if (ks !== null) {
			sendKey(ks, true);
			ev.preventDefault();
		}
	}
	function onKeyUp(ev) {
		const ks = keysymFor(ev);
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
