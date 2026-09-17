// Package wsbridge is a loopback-only HTTP+WebSocket server that carries
// the high-frequency framebuffer pixel stream and input events between the
// Go RFB client and the frontend canvas, bypassing Wails' JSON-serialized
// binding bridge. See the project plan's "key architectural decision" note
// for why: Wails' Go<->JS calls round-trip through JSON, which is fine for
// control-plane data (connection lists, settings) but the wrong tool for a
// live pixel stream of dirty rectangles at tens of updates/sec.
//
// The server binds to 127.0.0.1 on an OS-assigned port and is never
// reachable off-box. frontend/src/viewer.js is the counterpart client.
package wsbridge

import (
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

// InputSink receives pointer/keyboard/clipboard input decoded from the
// browser-side canvas and forwards it to the live RFB session.
// *rfb.Client satisfies this interface directly.
type InputSink interface {
	SendPointerEvent(x, y int, buttonMask uint8) error
	SendKeyEvent(keysym uint32, down bool) error
	SendClientCutText(text string) error
}

// Server hosts zero or more live Sessions.
type Server struct {
	mu       sync.Mutex
	sessions map[string]*Session
	listener net.Listener
	httpSrv  *http.Server
}

func NewServer() *Server {
	return &Server{sessions: map[string]*Session{}}
}

// Start begins listening and returns the base WebSocket URL
// ("ws://127.0.0.1:PORT/fb") sessions are reachable under.
func (s *Server) Start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("wsbridge: listen: %w", err)
	}
	s.listener = ln

	mux := http.NewServeMux()
	mux.HandleFunc("/fb", s.handleFB)
	s.httpSrv = &http.Server{Handler: mux}
	go s.httpSrv.Serve(ln)

	return fmt.Sprintf("ws://%s/fb", ln.Addr().String()), nil
}

// Stop shuts the bridge server down. Call it once, on app quit.
func (s *Server) Stop() {
	if s.httpSrv != nil {
		s.httpSrv.Close()
	}
}

// NewSession registers a session the frontend can attach to by opening a
// WebSocket to "<baseURL>?session=<id>". id should be unguessable (a
// UUID): it's the only access control on this loopback endpoint, since the
// Wails webview's origin never matches this server's own origin and so
// can't be used for a same-origin check (see handleFB).
func (s *Server) NewSession(id string, input InputSink) *Session {
	sess := &Session{
		id:         id,
		input:      input,
		attachedCh: make(chan struct{}),
		closedCh:   make(chan struct{}),
	}
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
	return sess
}

// CloseSession detaches and forgets a session.
func (s *Server) CloseSession(id string) {
	s.mu.Lock()
	sess := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if sess != nil {
		sess.close()
	}
}

func (s *Server) handleFB(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session")
	s.mu.Lock()
	sess := s.sessions[id]
	s.mu.Unlock()
	if sess == nil {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The Wails webview's origin (a custom-scheme or *.localhost
		// origin depending on OS/webview backend) never matches this
		// loopback server's own origin, so the default same-origin
		// check would reject every legitimate connection. The
		// unguessable per-connection session UUID is the real access
		// control for this loopback-only endpoint.
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	sess.attach(r.Context(), conn)
}
