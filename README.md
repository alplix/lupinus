# Lupinus

Native cross-platform VNC client.

Lupinus connects to VNC servers and gives you a fast, native-feeling remote desktop viewer —
no browser tab, no Electron, just a small native window built with **Go + [Wails v2](https://wails.io)**.

## Features

| Area | What you can do |
|---|---|
| **Protocol** | RFB 3.3 / 3.7 / 3.8 handshake, negotiated automatically against whatever the server offers |
| **Authentication** | None and VNC Authentication (DES-challenge) security types |
| **Encodings** | Raw, CopyRect and ZRLE framebuffer updates |
| **Saved connections** | Name, host, port and color tag stored locally; passwords are never written to disk — they live in the OS keychain (Windows Credential Manager, macOS Keychain, Linux Secret Service) |
| **Live viewer** | Fit / actual-size / stretch scaling, with the canvas following window resizes |
| **Clipboard sync** | Two-way clipboard text between your desktop and the remote session |
| **Input** | Full keyboard and pointer passthrough, plus a dedicated Ctrl+Alt+Del combo |
| **Server-driven resize** | Framebuffer size changes pushed by the server (DesktopSize / ExtendedDesktopSize) are picked up live |
| **Theme** | Light and dark UI themes |

## Platforms

| OS | Architectures | Packaging | Notes |
|---|---|---|---|
| Windows 10/11 | amd64 | NSIS installer + portable zip | WebView2 required at runtime (bundled by the installer) |
| macOS | amd64, arm64 | `.app` bundle (zip) | |
| Linux | amd64, arm64 | portable `.tar.gz` | GTK3 + WebKitGTK 4.1 required |

## Getting started

1. Open **Add Connection**, enter the server's host and port (default VNC port is `5900`), and a
   password if the server requires VNC Authentication.
2. Save it to reuse later — Lupinus stores the password in your OS keychain, never in a plaintext
   config file — or just **Quick Connect** without saving anything.
3. Once connected, use the viewer toolbar to switch scaling modes, sync your clipboard, send
   Ctrl+Alt+Del, or switch theme.

Connection metadata is stored in the OS config directory (`~/.config/lupinus` on Linux,
`%APPDATA%\lupinus` on Windows, `~/Library/Application Support/lupinus` on macOS).

## Building from source

Prerequisites:

- Go 1.26+
- [Wails CLI](https://wails.io/docs/gettingstarted/installation) (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`)
- Node.js 20+ (frontend tooling)

On Linux you also need the [Wails WebKit/GTK dependencies](https://wails.io/docs/guides/linux)
(`libgtk-3-dev libwebkit2gtk-4.1-dev libayatana-appindicator3-dev`). On Windows,
[WebView2](https://developer.microsoft.com/microsoft-edge/webview2/) is required at runtime
(bundled by the installer).

```
wails dev     # live development build
wails build   # production build for the current platform
```

Cross-compiling for release targets is handled by the CI pipelines:

- `.github/workflows/ci.yml` — vet, tests, frontend build and a Linux smoke build on every PR/push
- `.github/workflows/release.yml` — tagged releases (`vX.Y.Z`) for Windows, macOS and Linux with
  checksums

### Manual cross-build example (Linux host)

```bash
# Windows amd64 (+ NSIS installer)
wails build -platform windows/amd64 -nsis -webview2 download

# Linux arm64
wails build -platform linux/arm64
```

## Architecture

```
frontend/                 Vite + vanilla JS/CSS single-page UI
  src/viewer.js            canvas rendering, scaling, input capture, clipboard bridge
  src/keysym.js             DOM KeyboardEvent -> X11 keysym mapping
  wailsjs/                  generated bindings to the Go API

internal/rfb/               RFB 3.3/3.7/3.8 client: handshake, auth, framebuffer decoding
internal/store/              connection list + theme, persisted to config.json; passwords in the OS keyring
internal/wsbridge/           local WebSocket bridge carrying framebuffer/input between Go and the webview
internal/version/            single source of truth for the version number and branding strings
app.go                       Wails-bound API surface (connections, live sessions, settings)
main.go                      tray icon + window lifecycle, single-instance lock
```

## Releasing

`internal/version/version.go`'s `Version` constant is the **single source of truth** for the
Lupinus version number — unlike some older sibling projects that hand-edit the version in three
different places, everything else is derived from it.

1. Bump `Version` in `internal/version/version.go`.
2. Run `node scripts/sync-version.mjs` — it stamps `wails.json`'s `info.productVersion` and
   `frontend/package.json`'s `version` to match, and prints what it changed. CI re-runs this
   script and fails the build if anything is out of sync, so don't skip it.
3. Commit the changes.
4. `git tag v0.2.0 && git push origin v0.2.0` — this triggers `release.yml`, which builds all
   three platforms and publishes a GitHub release with `SHA256SUMS.txt` alongside the archives.

## Roadmap

Not yet implemented — contributions welcome:

- Tight and Hextile encodings
- TLS / VeNCrypt transport security
- File transfer
- Multi-monitor / multi-display sessions
- Audio redirection

## Author

**Coded by Alperen Yavuz**

## Support

If Lupinus is useful to you and you'd like to support its development:

[Support Lupinus](https://coff.ee/alplix)

## License

MIT — see [LICENSE](LICENSE).
