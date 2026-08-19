package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/misakayyds/lantally/internal/ingest"
	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
)

type Config struct {
	StaticFS fs.FS
}

type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]time.Time)}
}

func (s *sessionStore) create() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	s.mu.Lock()
	s.sessions[token] = time.Now().UTC().Add(24 * time.Hour)
	s.mu.Unlock()
	return token, nil
}

func (s *sessionStore) valid(token string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	expires, ok := s.sessions[token]
	return ok && time.Now().UTC().Before(expires)
}

func Routes(store *sqlitestore.Store, cfg Config) http.Handler {
	sessions := newSessionStore()
	ingestHandler := ingest.NewHandler(store)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ingest", ingestHandler.Ingest)
	mux.HandleFunc("GET /healthz", ingestHandler.Healthz)
	mux.HandleFunc("POST /v1/login", loginHandler(store, sessions))
	mux.HandleFunc("GET /v1/overview", requireSession(sessions, overviewHandler(store)))
	mux.HandleFunc("GET /v1/devices", requireSession(sessions, emptyListHandler("devices")))
	mux.HandleFunc("GET /v1/nodes", requireSession(sessions, nodesHandler(store)))
	mux.HandleFunc("GET /v1/proxy", requireSession(sessions, emptyListHandler("proxy")))
	mux.HandleFunc("GET /v1/alerts", requireSession(sessions, emptyListHandler("alerts")))
	if cfg.StaticFS != nil {
		mux.Handle("GET /{$}", http.FileServer(http.FS(cfg.StaticFS)))
		mux.Handle("GET /styles.css", http.FileServer(http.FS(cfg.StaticFS)))
		mux.Handle("GET /app.js", http.FileServer(http.FS(cfg.StaticFS)))
	}
	return mux
}

func requireSession(sessions *sessionStore, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("lantally_session")
		if err != nil || !sessions.valid(cookie.Value) {
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func loginHandler(store *sqlitestore.Store, sessions *sessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "invalid login", http.StatusBadRequest)
			return
		}
		if err := store.AuthenticateAdmin(body.Password); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token, err := sessions.create()
		if err != nil {
			http.Error(w, "session unavailable", http.StatusServiceUnavailable)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "lantally_session",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func overviewHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		count, err := store.BatchCount(r.Context())
		if err != nil {
			http.Error(w, "overview unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ledgers":        []string{"total", "direct", "proxy_raw", "proxy_adjusted"},
			"ingest_batches": count,
		})
	}
}

func nodesHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []any{}})
	}
}

func emptyListHandler(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{name: []any{}})
	}
}

// EnsureFirstRunAdmin creates a one-time admin password when none exists.
func EnsureFirstRunAdmin(store *sqlitestore.Store, stdout io.Writer) error {
	hasAdmin, err := store.HasAdminCredential()
	if err != nil {
		return err
	}
	if hasAdmin {
		return nil
	}
	password, err := sqlitestore.GenerateAdminPassword()
	if err != nil {
		return err
	}
	if err := store.CreateAdminCredential(password); err != nil {
		return err
	}
	if stdout == nil {
		stdout = io.Discard
	}
	_, err = fmt.Fprintf(stdout, "LanTally admin password (shown once): %s\n", password)
	return err
}
