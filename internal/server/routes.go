package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/misakayyds/lantally/internal/enroll"
	"github.com/misakayyds/lantally/internal/ingest"
	"github.com/misakayyds/lantally/internal/store/metrics"
	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
)

type Config struct {
	StaticFS fs.FS
	Metrics  *metrics.Writer
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
	if cfg.Metrics != nil {
		ingestHandler.SetMetrics(cfg.Metrics)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ingest", ingestHandler.Ingest)
	mux.HandleFunc("GET /healthz", ingestHandler.Healthz)
	mux.HandleFunc("POST /v1/login", loginHandler(store, sessions))
	mux.HandleFunc("POST /v1/logout", logoutHandler(sessions))
	mux.HandleFunc("POST /v1/enroll", requireSession(sessions, enrollHandler(store)))
	mux.HandleFunc("GET /v1/overview", requireSession(sessions, overviewHandler(store)))
	mux.HandleFunc("GET /v1/devices", requireSession(sessions, devicesHandler(store)))
	mux.HandleFunc("GET /v1/nodes", requireSession(sessions, nodesHandler(store)))
	mux.HandleFunc("GET /v1/proxy", requireSession(sessions, proxyHandler(store)))
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

func logoutHandler(sessions *sessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("lantally_session"); err == nil {
			sessions.mu.Lock()
			delete(sessions.sessions, cookie.Value)
			sessions.mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "lantally_session",
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func enrollHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			SiteID string `json:"site_id"`
			NodeID string `json:"node_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "invalid enroll request", http.StatusBadRequest)
			return
		}
		body.SiteID = strings.TrimSpace(body.SiteID)
		body.NodeID = strings.TrimSpace(body.NodeID)
		if body.SiteID == "" || body.NodeID == "" {
			http.Error(w, "site_id and node_id are required", http.StatusBadRequest)
			return
		}
		token, credentialID, err := enroll.IssueToken()
		if err != nil {
			http.Error(w, "token unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := store.CreateNode(
			r.Context(),
			body.NodeID,
			body.SiteID,
			credentialID,
			enroll.HashToken(token),
		); err != nil {
			http.Error(w, "enroll failed", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"site_id": body.SiteID,
			"node_id": body.NodeID,
			"token":   token,
		})
	}
}

func overviewHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		count, err := store.BatchCount(r.Context())
		if err != nil {
			http.Error(w, "overview unavailable", http.StatusServiceUnavailable)
			return
		}
		bytes, err := store.LedgerTotals(r.Context())
		if err != nil {
			http.Error(w, "overview unavailable", http.StatusServiceUnavailable)
			return
		}
		traffic, err := lastTraffic(store, r, "node", "total")
		if err != nil {
			http.Error(w, "overview unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ledgers":        []string{"total", "direct", "proxy_raw", "proxy_adjusted"},
			"ingest_batches": count,
			"bytes":          bytes,
			"traffic":        traffic,
		})
	}
}

func devicesHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		devices, err := store.ListDeviceLedgers(r.Context())
		if err != nil {
			http.Error(w, "devices unavailable", http.StatusServiceUnavailable)
			return
		}
		if devices == nil {
			devices = []sqlitestore.DeviceLedger{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": devices})
	}
}

func proxyHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bytes, err := store.LedgerTotals(r.Context())
		if err != nil {
			http.Error(w, "proxy unavailable", http.StatusServiceUnavailable)
			return
		}
		traffic, err := lastTraffic(store, r, "class", "")
		if err != nil {
			http.Error(w, "proxy unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"proxy": map[string]uint64{
				"direct":           bytes["direct"],
				"proxy_raw":        bytes["proxy_raw"],
				"proxy_adjusted":   bytes["proxy_adjusted"],
				"proxy_unadjusted": bytes["proxy_unadjusted"],
			},
			"traffic": traffic,
		})
	}
}

func nodesHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodes, err := store.ListNodes(r.Context())
		if err != nil {
			http.Error(w, "nodes unavailable", http.StatusServiceUnavailable)
			return
		}
		if nodes == nil {
			nodes = []sqlitestore.Node{}
		}
		out := make([]map[string]any, 0, len(nodes))
		for _, node := range nodes {
			bytes, err := store.NodeLedgerTotals(r.Context(), node.SiteID, node.ID)
			if err != nil {
				http.Error(w, "nodes unavailable", http.StatusServiceUnavailable)
				return
			}
			out = append(out, map[string]any{
				"id":      node.ID,
				"site_id": node.SiteID,
				"bytes":   bytes,
			})
		}
		traffic, err := lastTraffic(store, r, "node", "total")
		if err != nil {
			http.Error(w, "nodes unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": out, "traffic": traffic})
	}
}

func emptyListHandler(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{name: []any{}})
	}
}

func lastTraffic(store *sqlitestore.Store, r *http.Request, group, class string) (sqlitestore.TrafficSeries, error) {
	now := time.Now().UTC()
	return store.TrafficSeries(r.Context(), sqlitestore.TrafficQuery{
		From:          now.Add(-72 * time.Hour),
		To:            now,
		BucketSeconds: 1800,
		Group:         group,
		Class:         class,
	})
}

// EnsureFirstRunAdmin creates a one-time admin password when none exists.
// If LANTALLY_BOOTSTRAP_ADMIN_PASSWORD is set and no admin exists yet, that
// value is used instead of generating a random password.
func EnsureFirstRunAdmin(store *sqlitestore.Store, stdout io.Writer) error {
	hasAdmin, err := store.HasAdminCredential()
	if err != nil {
		return err
	}
	if hasAdmin {
		return nil
	}
	password, err := bootstrapAdminPassword()
	if err != nil {
		return err
	}
	if err := store.CreateAdminCredential(password.value); err != nil {
		return err
	}
	if password.fromEnv {
		if stdout == nil {
			stdout = io.Discard
		}
		_, err = fmt.Fprintln(stdout, "LanTally admin password loaded from LANTALLY_BOOTSTRAP_ADMIN_PASSWORD")
		return err
	}
	if stdout == nil {
		stdout = io.Discard
	}
	_, err = fmt.Fprintf(stdout, "LanTally admin password (shown once): %s\n", password.value)
	return err
}

type adminPassword struct {
	value   string
	fromEnv bool
}

func bootstrapAdminPassword() (adminPassword, error) {
	if envPassword := strings.TrimSpace(os.Getenv("LANTALLY_BOOTSTRAP_ADMIN_PASSWORD")); envPassword != "" {
		return adminPassword{value: envPassword, fromEnv: true}, nil
	}
	generated, err := sqlitestore.GenerateAdminPassword()
	if err != nil {
		return adminPassword{}, err
	}
	return adminPassword{value: generated, fromEnv: false}, nil
}
