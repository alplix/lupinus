package main

import (
	"context"
	"fmt"
	"os"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/getlantern/systray"
	"github.com/google/uuid"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/alplix/lupinus/internal/rfb"
	"github.com/alplix/lupinus/internal/store"
	"github.com/alplix/lupinus/internal/version"
	"github.com/alplix/lupinus/internal/wsbridge"
)

// App is the whole Wails-bound control-plane API surface. Framebuffer
// pixels and input never go through here — see internal/wsbridge.
type App struct {
	ctx context.Context

	store     *store.Store
	bridge    *wsbridge.Server
	bridgeURL string

	mu   sync.Mutex
	live map[string]*liveSession
}

type liveSession struct {
	client *rfb.Client
	cancel context.CancelFunc
}

func NewApp() *App {
	return &App{live: map[string]*liveSession{}}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	st, err := store.Open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lupinus: opening config store:", err)
	}
	a.store = st

	a.bridge = wsbridge.NewServer()
	url, err := a.bridge.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lupinus: starting framebuffer bridge:", err)
	}
	a.bridgeURL = url

	go a.startTray()
}

func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	for _, live := range a.live {
		live.cancel()
	}
	a.mu.Unlock()
	if a.bridge != nil {
		a.bridge.Stop()
	}
	systray.Quit()
}

// startTray runs getlantern/systray's blocking event loop. It's launched
// from a goroutine in startup — deliberately not from main() — so that
// `wails.Run` (called directly from main) remains the process's primary
// blocking call. See main.go's doc comment for why: `wails build`
// compiles and runs this binary to generate bindings, and that step hangs
// forever if the program can't reach a natural exit point on its own,
// which it never could if systray's own event loop were the outer call.
func (a *App) startTray() {
	goruntime.LockOSThread()
	systray.Run(a.onTrayReady, a.onTrayExit)
}

func (a *App) onTrayReady() {
	if goruntime.GOOS == "windows" {
		systray.SetIcon(iconICO)
	} else {
		systray.SetIcon(iconPNG)
	}
	systray.SetTitle(version.Name)
	systray.SetTooltip(version.Name + " - Native VNC Client")

	mShow := systray.AddMenuItem("Open "+version.Name, "Show main window")
	mHide := systray.AddMenuItem("Hide to tray", "Hide the main window")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit "+version.Name)

	go func() {
		for {
			select {
			case <-mShow.ClickedCh:
				a.showWindow()
			case <-mHide.ClickedCh:
				a.hideWindow()
			case <-mQuit.ClickedCh:
				runtime.Quit(a.ctx)
				return
			}
		}
	}()
}

func (a *App) onTrayExit() {}

func (a *App) showWindow() {
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
}

func (a *App) hideWindow() {
	runtime.WindowHide(a.ctx)
}

// --- About / branding ---

type About struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Author  string `json:"author"`
	Repo    string `json:"repo"`
	Support string `json:"support"`
}

func (a *App) GetAbout() About {
	return About{
		Name:    version.Name,
		Version: version.Version,
		Author:  version.Author,
		Repo:    version.Repo,
		Support: version.Support,
	}
}

// OpenSupportLink opens the donation page in the user's default browser —
// never an embedded webview, per Lupinus's no-tracking/no-embedded-browser
// branding requirement.
func (a *App) OpenSupportLink() {
	runtime.BrowserOpenURL(a.ctx, version.Support)
}

func (a *App) OpenGitHub() {
	runtime.BrowserOpenURL(a.ctx, version.Repo)
}

// --- Settings ---

func (a *App) GetTheme() string {
	if a.store == nil {
		return "system"
	}
	return a.store.Theme()
}

func (a *App) SetTheme(theme string) error {
	if a.store == nil {
		return fmt.Errorf("config store unavailable")
	}
	return a.store.SetTheme(theme)
}

// --- Connections ---

func (a *App) ListConnections() []store.Connection {
	if a.store == nil {
		return nil
	}
	return a.store.List()
}

func (a *App) SaveConnection(conn store.Connection, password string) (store.Connection, error) {
	if a.store == nil {
		return store.Connection{}, fmt.Errorf("config store unavailable")
	}
	return a.store.Save(conn, password)
}

func (a *App) DeleteConnection(id string) error {
	if a.store == nil {
		return fmt.Errorf("config store unavailable")
	}
	return a.store.Delete(id)
}

// --- Live sessions ---

// ConnectResult tells the frontend where to open the framebuffer
// WebSocket for a newly started session.
type ConnectResult struct {
	SessionID string `json:"sessionId"`
	BridgeURL string `json:"bridgeUrl"`
}

// StatusPayload is emitted on the "lupinus:status" Wails event as a
// session's state changes. Framebuffer/input traffic itself never crosses
// this event bus — only these low-frequency lifecycle notifications do.
type StatusPayload struct {
	SessionID string `json:"sessionId"`
	State     string `json:"state"` // connecting | connected | error | disconnected
	Message   string `json:"message,omitempty"`
}

func (a *App) emitStatus(sessionID, state, message string) {
	runtime.EventsEmit(a.ctx, "lupinus:status", StatusPayload{
		SessionID: sessionID,
		State:     state,
		Message:   message,
	})
}

// Connect starts a session for a saved connection, using its stored
// credential (if any).
func (a *App) Connect(id string) (ConnectResult, error) {
	if a.store == nil {
		return ConnectResult{}, fmt.Errorf("config store unavailable")
	}
	var target store.Connection
	found := false
	for _, c := range a.store.List() {
		if c.ID == id {
			target, found = c, true
			break
		}
	}
	if !found {
		return ConnectResult{}, fmt.Errorf("connection %q not found", id)
	}

	password, err := a.store.Password(id)
	if err != nil {
		return ConnectResult{}, err
	}
	_ = a.store.TouchLastUsed(id)

	return a.startSession(fmt.Sprintf("%s:%d", target.Host, target.Port), password)
}

// QuickConnect starts a session without saving it.
func (a *App) QuickConnect(host string, port int, password string) (ConnectResult, error) {
	return a.startSession(fmt.Sprintf("%s:%d", host, port), password)
}

func (a *App) startSession(addr, password string) (ConnectResult, error) {
	sessionID := uuid.NewString()
	a.emitStatus(sessionID, "connecting", "")

	ctx, cancel := context.WithCancel(a.ctx)
	client, err := rfb.Dial(ctx, addr, rfb.DialOptions{
		Password:    password,
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		cancel()
		a.emitStatus(sessionID, "error", err.Error())
		return ConnectResult{}, err
	}

	sess := a.bridge.NewSession(sessionID, client)

	a.mu.Lock()
	a.live[sessionID] = &liveSession{client: client, cancel: cancel}
	a.mu.Unlock()

	a.emitStatus(sessionID, "connected", "")

	go func() {
		runErr := client.Run(ctx, sess)

		a.mu.Lock()
		delete(a.live, sessionID)
		a.mu.Unlock()

		a.bridge.CloseSession(sessionID)
		client.Close()
		cancelled := ctx.Err() != nil
		cancel()

		if runErr != nil && !cancelled {
			a.emitStatus(sessionID, "error", runErr.Error())
		} else {
			a.emitStatus(sessionID, "disconnected", "")
		}
	}()

	return ConnectResult{
		SessionID: sessionID,
		BridgeURL: fmt.Sprintf("%s?session=%s", a.bridgeURL, sessionID),
	}, nil
}

// Disconnect tears down a live session.
func (a *App) Disconnect(sessionID string) {
	a.mu.Lock()
	live, ok := a.live[sessionID]
	a.mu.Unlock()
	if ok {
		live.cancel()
	}
}
