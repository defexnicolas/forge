package remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// Server is an HTTP surface that mirrors the current forge session and
// accepts remote prompts/commands from a web UI on the LAN.
type Server struct {
	hub      *Hub
	token    string
	addr     string
	http     *http.Server
	startAt  time.Time
	inputCh  chan Input
	sessionf SessionFn
}

// Input is an entry from the remote device. Kind is "chat" for normal prompts
// and "command" for slash commands (leading "/").
type Input struct {
	Kind string `json:"type"`
	Text string `json:"text"`
}

// SessionFn returns a JSON-serializable description of the current session to
// seed newly-connected clients. Injected so this package doesn't depend on
// internal/session directly.
type SessionFn func() any

// Config bundles what the caller controls at startup.
type Config struct {
	Host        string    // bind host, e.g. "0.0.0.0"
	Port        int       // 0 => default 9595
	Hub         *Hub      // required — the agent runtime's event tee
	SessionFn   SessionFn // seeds newly-connected clients
	InputBuffer int       // capacity of the input channel (default 32)
}

// New builds a Server but does not start listening. Use Start.
func New(cfg Config) (*Server, error) {
	if cfg.Hub == nil {
		return nil, fmt.Errorf("remote: hub is required")
	}
	port := cfg.Port
	if port == 0 {
		port = 9595
	}
	host := cfg.Host
	if host == "" {
		host = "0.0.0.0"
	}
	buf := cfg.InputBuffer
	if buf <= 0 {
		buf = 32
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	return &Server{
		hub:      cfg.Hub,
		token:    token,
		addr:     fmt.Sprintf("%s:%d", host, port),
		inputCh:  make(chan Input, buf),
		sessionf: cfg.SessionFn,
	}, nil
}

// Token returns the bearer token that clients must send.
func (s *Server) Token() string { return s.token }

// Addr returns the bind address (host:port).
func (s *Server) Addr() string { return s.addr }

// LANURL returns a best-guess URL that another device on the LAN can use.
func (s *Server) LANURL() string {
	_, portStr, err := net.SplitHostPort(s.addr)
	if err != nil {
		portStr = "9595"
	}
	host := firstLANIP()
	if host == "" {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s:%s/?t=%s", host, portStr, s.token)
}

// Inputs returns the channel of remote inputs (chat/commands) the caller must
// drain into the TUI.
func (s *Server) Inputs() <-chan Input { return s.inputCh }

// Start begins serving. Non-blocking — returns after ListenAndServe is spawned.
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/session", s.authed(s.handleSession))
	mux.HandleFunc("/api/stream", s.authed(s.handleStream))
	mux.HandleFunc("/api/input", s.authed(s.handleInput))
	s.http = &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.startAt = time.Now()
	go func() { _ = s.http.Serve(ln) }()
	return nil
}

// Stop shuts the server down. Safe to call on a nil server.
func (s *Server) Stop(ctx context.Context) error {
	if s == nil || s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

// SubscriberCount reports how many web clients are currently attached.
func (s *Server) SubscriberCount() int {
	if s == nil || s.hub == nil {
		return 0
	}
	return s.hub.SubscriberCount()
}

// --- internal handlers ---

func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("Authorization")
		tok = strings.TrimPrefix(tok, "Bearer ")
		if tok == "" {
			tok = r.URL.Query().Get("t")
		}
		if tok == "" || tok != s.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var payload any
	if s.sessionf != nil {
		payload = s.sessionf()
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"session":  payload,
		"since":    s.startAt.UTC().Format(time.RFC3339),
		"viewers":  s.SubscriberCount(),
	})
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := s.hub.Subscribe()
	defer unsub()

	ctx := r.Context()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case ev, open := <-ch:
			if !open {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (s *Server) handleInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in Input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		http.Error(w, "empty text", http.StatusBadRequest)
		return
	}
	if in.Kind == "" {
		if strings.HasPrefix(in.Text, "/") {
			in.Kind = "command"
		} else {
			in.Kind = "chat"
		}
	}
	select {
	case s.inputCh <- in:
		w.WriteHeader(http.StatusAccepted)
	case <-time.After(2 * time.Second):
		http.Error(w, "input queue full", http.StatusServiceUnavailable)
	}
}

// --- helpers ---

func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// firstLANIP returns the first non-loopback IPv4 address on the host. Used to
// build a copyable URL; not a security boundary.
func firstLANIP() string {
	ifaces, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range ifaces {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil {
			continue
		}
		return ip4.String()
	}
	return ""
}
