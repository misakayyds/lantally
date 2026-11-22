package server

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/collector/direct"
	"github.com/misakayyds/lantally/internal/enroll"
	"github.com/misakayyds/lantally/internal/identity"
	"github.com/misakayyds/lantally/internal/ingest"
	"github.com/misakayyds/lantally/internal/redact"
	"github.com/misakayyds/lantally/internal/store/metrics"
	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
	"github.com/misakayyds/lantally/internal/version"
)

//go:embed install.sh install.ps1
var installFiles embed.FS

type Config struct {
	StaticFS       fs.FS
	Metrics        *metrics.Writer
	AgentDir       string
	Direct         *direct.Manager
	TrustedProxies []string
}

type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]time.Time
}

var sessionTTL = 24 * time.Hour

const loginFailureLimit = 5

type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{failures: make(map[string][]time.Time)}
}

func (l *loginLimiter) allow(key string, now time.Time) bool {
	window := now.Add(-15 * time.Minute)
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.failures[key][:0]
	for _, at := range l.failures[key] {
		if at.After(window) {
			kept = append(kept, at)
		}
	}
	l.failures[key] = kept
	return len(kept) < loginFailureLimit
}

func (l *loginLimiter) fail(key string, now time.Time) {
	l.mu.Lock()
	l.failures[key] = append(l.failures[key], now)
	l.mu.Unlock()
}

func (l *loginLimiter) success(key string) {
	l.mu.Lock()
	delete(l.failures, key)
	l.mu.Unlock()
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
	s.sessions[token] = time.Now().UTC().Add(sessionTTL)
	s.mu.Unlock()
	return token, nil
}

func (s *sessionStore) valid(token string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	expires, ok := s.sessions[token]
	return ok && time.Now().UTC().Before(expires)
}

func (s *sessionStore) touch(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[token]; !ok {
		return
	}
	s.sessions[token] = time.Now().UTC().Add(sessionTTL)
}

func Routes(store *sqlitestore.Store, cfg Config) http.Handler {
	sessions := newSessionStore()
	limiter := newLoginLimiter()
	trustedProxies := parseTrustedProxies(cfg.TrustedProxies)
	ingestHandler := ingest.NewHandler(store)
	if cfg.Metrics != nil {
		ingestHandler.SetMetrics(cfg.Metrics)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ingest", ingestHandler.Ingest)
	mux.HandleFunc("GET /healthz", ingestHandler.Healthz)
	mux.HandleFunc("POST /v1/login", loginHandler(store, sessions, limiter, trustedProxies...))
	mux.HandleFunc("POST /v1/logout", logoutHandler(sessions))
	mux.HandleFunc("GET /v1/setup", setupStatusHandler(store))
	mux.HandleFunc("POST /v1/setup", setupHandler(store, sessions))
	mux.HandleFunc("POST /v1/enroll", requireSession(sessions, enrollHandler(store)))
	mux.HandleFunc("POST /v1/claims", requireSession(sessions, createClaimHandler(store)))
	mux.HandleFunc("POST /v1/claim", redeemClaimHandler(store))
	mux.HandleFunc("GET /install.sh", installScriptHandler("install.sh"))
	mux.HandleFunc("GET /install.ps1", installScriptHandler("install.ps1"))
	mux.HandleFunc("GET /agents/{os}/{arch}", agentBinaryHandler(cfg.AgentDir))
	mux.HandleFunc("GET /v1/overview", requireSession(sessions, overviewHandler(store)))
	mux.HandleFunc("GET /v1/traffic", requireSession(sessions, trafficHandler(store)))
	mux.HandleFunc("GET /v1/dimensions", requireSession(sessions, dimensionsHandler(store)))
	mux.HandleFunc("GET /v1/devices", requireSession(sessions, devicesHandler(store)))
	mux.HandleFunc("GET /v1/devices/{id}", requireSession(sessions, deviceDetailHandler(store)))
	mux.HandleFunc("PUT /v1/devices/{id}", requireSession(sessions, renameDeviceHandler(store)))
	mux.HandleFunc("POST /v1/devices/{id}/merge", requireSession(sessions, mergeDeviceHandler(store)))
	mux.HandleFunc("POST /v1/devices/{id}/unmerge", requireSession(sessions, unmergeDeviceHandler(store)))
	mux.HandleFunc("GET /v1/nodes", requireSession(sessions, nodesHandler(store)))
	mux.HandleFunc("GET /v1/nodes/{id}", requireSession(sessions, nodeDetailHandler(store)))
	mux.HandleFunc("PUT /v1/nodes/{id}/config", requireSession(sessions, nodeConfigHandler(store, cfg.Direct)))
	mux.HandleFunc("POST /v1/nodes/{id}/revoke", requireSession(sessions, revokeNodeHandler(store, cfg.Direct)))
	mux.HandleFunc("GET /v1/direct-collectors", requireSession(sessions, listDirectCollectorsHandler(store)))
	mux.HandleFunc("POST /v1/direct-collectors", requireSession(sessions, createDirectCollectorHandler(store, cfg.Direct)))
	mux.HandleFunc("POST /v1/direct-collectors/test", requireSession(sessions, testDirectCollectorConfigHandler(cfg.Direct)))
	mux.HandleFunc("PUT /v1/direct-collectors/{id}", requireSession(sessions, updateDirectCollectorHandler(store, cfg.Direct)))
	mux.HandleFunc("DELETE /v1/direct-collectors/{id}", requireSession(sessions, deleteDirectCollectorHandler(store, cfg.Direct)))
	mux.HandleFunc("POST /v1/direct-collectors/{id}/test", requireSession(sessions, testDirectCollectorByIDHandler(store, cfg.Direct)))
	mux.HandleFunc("POST /v1/direct-collectors/{id}/toggle", requireSession(sessions, toggleDirectCollectorHandler(store, cfg.Direct)))
	mux.HandleFunc("GET /v1/proxy", requireSession(sessions, proxyHandler(store)))
	mux.HandleFunc("GET /v1/proxy/rules", requireSession(sessions, listProxyRulesHandler(store)))
	mux.HandleFunc("POST /v1/proxy/rules", requireSession(sessions, createProxyRuleHandler(store)))
	mux.HandleFunc("PUT /v1/proxy/rules/{id}", requireSession(sessions, updateProxyRuleHandler(store)))
	mux.HandleFunc("DELETE /v1/proxy/rules/{id}", requireSession(sessions, deleteProxyRuleHandler(store)))
	mux.HandleFunc("GET /v1/proxy/rules/export", requireSession(sessions, exportProxyRulesHandler(store)))
	mux.HandleFunc("POST /v1/proxy/rules/import", requireSession(sessions, importProxyRulesHandler(store)))
	mux.HandleFunc("POST /v1/proxy/rules/preview", requireSession(sessions, previewProxyRulesHandler(store)))
	mux.HandleFunc("PUT /v1/proxy/multipliers", requireSession(sessions, setMultiplierHandler(store)))
	mux.HandleFunc("PUT /v1/proxy/billing", requireSession(sessions, setBillingHandler(store)))
	mux.HandleFunc("GET /v1/proxy/providers", requireSession(sessions, listProxyProvidersHandler(store)))
	mux.HandleFunc("POST /v1/proxy/providers", requireSession(sessions, createProxyProviderHandler(store)))
	mux.HandleFunc("PUT /v1/proxy/providers/{id}", requireSession(sessions, updateProxyProviderHandler(store)))
	mux.HandleFunc("DELETE /v1/proxy/providers/{id}", requireSession(sessions, deleteProxyProviderHandler(store)))
	mux.HandleFunc("GET /v1/watch", requireSession(sessions, listWatchHandler(store)))
	mux.HandleFunc("POST /v1/watch", requireSession(sessions, createWatchHandler(store, cfg.Direct)))
	mux.HandleFunc("GET /v1/watch/{id}", requireSession(sessions, getWatchHandler(store)))
	mux.HandleFunc("PUT /v1/watch/{id}", requireSession(sessions, updateWatchHandler(store, cfg.Direct)))
	mux.HandleFunc("DELETE /v1/watch/{id}", requireSession(sessions, deleteWatchHandler(store, cfg.Direct)))
	mux.HandleFunc("GET /v1/alerts", requireSession(sessions, alertsHandler(store)))
	mux.HandleFunc("POST /v1/alerts/{id}/ack", requireSession(sessions, ackAlertHandler(store)))
	mux.HandleFunc("GET /v1/settings", requireSession(sessions, settingsHandler()))
	mux.HandleFunc("PUT /v1/settings/password", requireSession(sessions, changePasswordHandler(store)))
	mux.HandleFunc("GET /v1/backup", requireSession(sessions, backupHandler(store)))
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	if cfg.StaticFS != nil {
		mux.Handle("/", staticSPAHandler(cfg.StaticFS))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "..") {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func requireSession(sessions *sessionStore, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("lantally_session")
		if err != nil || !sessions.valid(cookie.Value) {
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}
		sessions.touch(cookie.Value)
		http.SetCookie(w, sessionCookie(r, cookie.Value, int(sessionTTL.Seconds())))
		next(w, r)
	}
}

func requestHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func sessionCookie(r *http.Request, token string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     "lantally_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	}
}

func parseTrustedProxies(raw []string) []netip.Prefix {
	var list []string
	list = append(list, raw...)
	if env := strings.TrimSpace(os.Getenv("LANTALLY_TRUSTED_PROXIES")); env != "" {
		list = append(list, strings.Split(env, ",")...)
	}
	var prefixes []netip.Prefix
	for _, item := range list {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if p, err := netip.ParsePrefix(trimmed); err == nil {
			prefixes = append(prefixes, p)
			continue
		}
		if addr, err := netip.ParseAddr(trimmed); err == nil {
			bits := 128
			if addr.Is4() {
				bits = 32
			}
			prefixes = append(prefixes, netip.PrefixFrom(addr, bits))
		}
	}
	return prefixes
}

func isTrustedProxyPeer(addr netip.Addr, trusted []netip.Prefix) bool {
	unmapped := addr.Unmap()
	if unmapped.IsLoopback() {
		return true
	}
	for _, p := range trusted {
		if p.Contains(addr) || p.Contains(unmapped) {
			return true
		}
	}
	return false
}

func clientIP(r *http.Request, trusted ...netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	if peer, parseErr := netip.ParseAddr(strings.TrimSpace(host)); parseErr == nil && isTrustedProxyPeer(peer, trusted) {
		if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
			if first := strings.TrimSpace(strings.Split(fwd, ",")[0]); first != "" {
				return first
			}
		}
	}
	if host != "" {
		return host
	}
	return r.RemoteAddr
}

func loginHandler(store *sqlitestore.Store, sessions *sessionStore, limiter *loginLimiter, trusted ...netip.Prefix) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := clientIP(r, trusted...)
		now := time.Now().UTC()
		if !limiter.allow(key, now) {
			http.Error(w, "too many login attempts", http.StatusTooManyRequests)
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "invalid login", http.StatusBadRequest)
			return
		}
		if err := store.AuthenticateAdmin(body.Password); err != nil {
			limiter.fail(key, now)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		limiter.success(key)
		token, err := sessions.create()
		if err != nil {
			http.Error(w, "session unavailable", http.StatusServiceUnavailable)
			return
		}
		http.SetCookie(w, sessionCookie(r, token, int(sessionTTL.Seconds())))
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
		http.SetCookie(w, sessionCookie(r, "", -1))
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func setupStatusHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		has, err := store.HasAdminCredential()
		if err != nil {
			http.Error(w, "setup unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"needed": !has})
	}
}

func setupHandler(store *sqlitestore.Store, sessions *sessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		has, err := store.HasAdminCredential()
		if err != nil {
			http.Error(w, "setup unavailable", http.StatusServiceUnavailable)
			return
		}
		if has {
			http.Error(w, "already set up", http.StatusConflict)
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "invalid setup", http.StatusBadRequest)
			return
		}
		body.Password = strings.TrimSpace(body.Password)
		if len(body.Password) < 8 {
			http.Error(w, "password too short", http.StatusBadRequest)
			return
		}
		if err := store.CreateAdminCredential(body.Password); err != nil {
			http.Error(w, "setup failed", http.StatusConflict)
			return
		}
		token, err := sessions.create()
		if err != nil {
			http.Error(w, "session unavailable", http.StatusServiceUnavailable)
			return
		}
		http.SetCookie(w, sessionCookie(r, token, int(sessionTTL.Seconds())))
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func settingsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"app_version":      version.Version,
			"protocol_version": 1,
			"retention": map[string]any{
				"samples_days": sqlitestore.SampleRetentionDays,
				"daily":        "permanent",
			},
			"public_https": "required",
		})
	}
}

func changePasswordHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Current  string `json:"current"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "invalid password", http.StatusBadRequest)
			return
		}
		if err := store.UpdateAdminPassword(body.Current, body.Password); err != nil {
			if errors.Is(err, sqlitestore.ErrAdminUnauthorized) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func backupHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tmpDir, err := os.MkdirTemp("", "lantally-backup-*")
		if err != nil {
			http.Error(w, "backup unavailable", http.StatusServiceUnavailable)
			return
		}
		defer os.RemoveAll(tmpDir)

		path := filepath.Join(tmpDir, "lantally.db")
		if err := store.BackupTo(path); err != nil {
			http.Error(w, "backup unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="lantally.db"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, path)
	}
}

func createClaimHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			SiteID string `json:"site_id"`
			NodeID string `json:"node_id"`
			Local  bool   `json:"local"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "invalid claim", http.StatusBadRequest)
			return
		}
		body.SiteID = strings.TrimSpace(body.SiteID)
		body.NodeID = strings.TrimSpace(body.NodeID)
		if body.SiteID == "" || body.NodeID == "" {
			http.Error(w, "site_id and node_id are required", http.StatusBadRequest)
			return
		}
		claim, err := store.CreateClaim(r.Context(), body.SiteID, body.NodeID, body.Local, time.Now().UTC())
		if err != nil {
			http.Error(w, "claim unavailable", http.StatusServiceUnavailable)
			return
		}
		base := publicBase(r)
		mihomoHint := ""
		if body.Local {
			mihomoHint = "本机 Mihomo 默认 127.0.0.1:9090。探测不到则只记网卡总量。"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":        claim.Code,
			"expires_at":  claim.ExpiresAt.UTC().Format(time.RFC3339),
			"local":       claim.Local,
			"linux":       `sh -c "$(wget -qO- ` + base + `/install.sh)" -- ` + claim.Code,
			"macos":       `curl -fsSL ` + base + `/install.sh | sh -s -- ` + claim.Code,
			"windows":     `$env:LANTALLY_CLAIM='` + claim.Code + `'; irm ` + base + `/install.ps1 | iex`,
			"mihomo_hint": mihomoHint,
		})
	}
}

func redeemClaimHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "invalid claim", http.StatusBadRequest)
			return
		}
		claim, err := store.RedeemClaim(r.Context(), strings.TrimSpace(body.Code), time.Now().UTC())
		if errors.Is(err, sqlitestore.ErrClaimNotFound) {
			http.Error(w, "claim not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, sqlitestore.ErrClaimUsed) || errors.Is(err, sqlitestore.ErrClaimExpired) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, "claim unavailable", http.StatusServiceUnavailable)
			return
		}
		token, credentialID, err := enroll.IssueToken()
		if err != nil {
			http.Error(w, "token unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := store.UpsertNodeToken(r.Context(), claim.NodeID, claim.SiteID, credentialID, enroll.HashToken(token)); err != nil {
			http.Error(w, "enroll failed", http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "ok",
			"token":   token,
			"site_id": claim.SiteID,
			"node_id": claim.NodeID,
			"local":   claim.Local,
		})
	}
}

func installScriptHandler(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := installFiles.ReadFile(name)
		if err != nil {
			http.Error(w, "script unavailable", http.StatusServiceUnavailable)
			return
		}
		body := strings.ReplaceAll(string(raw), "__SERVER_URL__", publicBase(r))
		if strings.HasSuffix(name, ".ps1") {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		}
		_, _ = io.WriteString(w, body)
	}
}

func agentBinaryHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(dir) == "" {
			http.Error(w, "agent binaries not packaged", http.StatusNotFound)
			return
		}
		osName := filepath.Base(r.PathValue("os"))
		arch := filepath.Base(r.PathValue("arch"))
		name := "lantally-agent"
		if osName == "windows" {
			name += ".exe"
		}
		path := filepath.Join(dir, osName, arch, name)
		http.ServeFile(w, r, path)
	}
}

func publicBase(r *http.Request) string {
	proto := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		proto = "https"
	}
	return proto + "://" + r.Host
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
		now := time.Now().UTC()
		tr, err := parseTimeRange(r, store, now)
		if err != nil {
			writeStoreError(w, err, "overview unavailable")
			return
		}
		count, err := store.BatchCount(r.Context())
		if err != nil {
			http.Error(w, "overview unavailable", http.StatusServiceUnavailable)
			return
		}
		var from, to time.Time
		if tr.Explicit {
			from, to = tr.From, tr.To
		}
		breakdown, err := store.LedgerBreakdown(r.Context(), from, to, "", "")
		if err != nil {
			http.Error(w, "overview unavailable", http.StatusServiceUnavailable)
			return
		}
		bytes := flattenBreakdownMap(breakdown)
		rx, tx, tot := summaryRxTxTotal(breakdown)
		traffic, err := lastTrafficWithRange(store, r, tr, "node", "total")
		if err != nil {
			writeStoreError(w, err, "overview unavailable")
			return
		}
		pinnedWatch, err := store.ListPinnedWatchStatuses(r.Context(), now, tr.Location)
		if err != nil {
			writeStoreError(w, err, "overview unavailable")
			return
		}
		if pinnedWatch == nil {
			pinnedWatch = []sqlitestore.WatchItemStatus{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ledgers":        []string{"total", "direct", "proxy_raw", "proxy_adjusted"},
			"ingest_batches": count,
			"bytes":          bytes,
			"breakdown":      breakdown,
			"rx":             rx,
			"tx":             tx,
			"total":          tot,
			"range":          tr,
			"traffic":        traffic,
			"pinned_watch":   pinnedWatch,
		})
	}
}

func devicesHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tr, err := parseTimeRange(r, store, time.Now().UTC())
		if err != nil {
			writeStoreError(w, err, "devices unavailable")
			return
		}
		var devices []sqlitestore.DeviceLedger
		if tr.Explicit {
			devices, err = store.ListDeviceLedgersInRange(r.Context(), tr.From, tr.To)
		} else {
			devices, err = store.ListDeviceLedgers(r.Context())
		}
		if err != nil {
			http.Error(w, "devices unavailable", http.StatusServiceUnavailable)
			return
		}
		if devices == nil {
			devices = []sqlitestore.DeviceLedger{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"devices": devices,
			"range":   tr,
		})
	}
}

func proxyHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UTC()
		tr, err := parseTimeRange(r, store, now)
		if err != nil {
			writeStoreError(w, err, "proxy unavailable")
			return
		}
		var from, to time.Time
		if tr.Explicit {
			from, to = tr.From, tr.To
		}
		breakdown, err := store.LedgerBreakdown(r.Context(), from, to, "", "")
		if err != nil {
			http.Error(w, "proxy unavailable", http.StatusServiceUnavailable)
			return
		}
		bytes := flattenBreakdownMap(breakdown)
		outbounds, err := store.ListOutboundLedgersInRange(r.Context(), from, to, "")
		if err != nil {
			http.Error(w, "proxy unavailable", http.StatusServiceUnavailable)
			return
		}
		traffic, err := lastTrafficWithRange(store, r, tr, "outbound", "proxy_raw")
		if err != nil {
			writeStoreError(w, err, "proxy unavailable")
			return
		}
		billing, err := store.BillingStatus(r.Context(), now)
		if err != nil {
			http.Error(w, "proxy unavailable", http.StatusServiceUnavailable)
			return
		}
		var proxyRx, proxyTx, proxyTot uint64
		if p, ok := breakdown[accounting.ClassProxyRaw]; ok {
			proxyRx, proxyTx, proxyTot = p.Rx, p.Tx, p.Total
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"proxy": map[string]uint64{
				"direct":           bytes["direct"],
				"proxy_raw":        bytes["proxy_raw"],
				"proxy_adjusted":   bytes["proxy_adjusted"],
				"proxy_unadjusted": bytes["proxy_unadjusted"],
			},
			"proxy_breakdown": breakdown,
			"rx":              proxyRx,
			"tx":              proxyTx,
			"total":           proxyTot,
			"range":           tr,
			"outbounds":       outbounds,
			"traffic":         traffic,
			"billing":         billing,
		})
	}
}

func setMultiplierHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name   string  `json:"name"`
			Factor float64 `json:"factor"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "invalid multiplier", http.StatusBadRequest)
			return
		}
		if err := store.SetMultiplier(r.Context(), body.Name, body.Factor); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func setBillingHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body sqlitestore.Billing
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "invalid billing", http.StatusBadRequest)
			return
		}
		if err := store.SetBilling(r.Context(), body); err != nil {
			writeStoreError(w, err, "billing unavailable")
			return
		}
		now := time.Now().UTC()
		if err := store.EvaluateAlerts(r.Context(), now); err != nil {
			http.Error(w, "billing unavailable", http.StatusServiceUnavailable)
			return
		}
		status, err := store.BillingStatus(r.Context(), now)
		if err != nil {
			http.Error(w, "billing unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	}
}

func alertsHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.EvaluateAlerts(r.Context(), time.Now().UTC()); err != nil {
			http.Error(w, "alerts unavailable", http.StatusServiceUnavailable)
			return
		}
		alerts, err := store.ListOpenAlerts(r.Context())
		if err != nil {
			http.Error(w, "alerts unavailable", http.StatusServiceUnavailable)
			return
		}
		if alerts == nil {
			alerts = []sqlitestore.Alert{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"alerts": alerts})
	}
}

func ackAlertHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid alert", http.StatusBadRequest)
			return
		}
		if err := store.AckAlert(r.Context(), id, time.Now().UTC()); errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "alert not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "ack failed", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func deviceDetailHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tr, err := parseTimeRange(r, store, time.Now().UTC())
		if err != nil {
			writeStoreError(w, err, "device unavailable")
			return
		}
		id := r.PathValue("id")
		var (
			device sqlitestore.DeviceLedger
			merged []string
		)
		if tr.Explicit {
			device, merged, err = store.GetDeviceInRange(r.Context(), id, tr.From, tr.To)
		} else {
			device, merged, err = store.GetDevice(r.Context(), id)
		}
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "device not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "device unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"device":      device,
			"merged_from": merged,
			"range":       tr,
		})
	}
}

func renameDeviceHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "invalid device", http.StatusBadRequest)
			return
		}
		id := r.PathValue("id")
		if err := store.RenameDevice(r.Context(), id, body.Name); errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "device not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "rename failed", http.StatusBadRequest)
			return
		}
		device, merged, err := store.GetDevice(r.Context(), id)
		if err != nil {
			http.Error(w, "device unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device":      device,
			"merged_from": merged,
		})
	}
}

func mergeDeviceHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OtherID string `json:"other_id"`
			Reason  string `json:"reason"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "invalid merge", http.StatusBadRequest)
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		otherID := strings.TrimSpace(body.OtherID)
		if id == "" || otherID == "" {
			http.Error(w, "other_id is required", http.StatusBadRequest)
			return
		}
		if id == otherID {
			http.Error(w, "cannot merge device with itself", http.StatusBadRequest)
			return
		}
		reason := strings.TrimSpace(body.Reason)
		if reason == "" {
			reason = "manual"
		}
		err := identity.NewResolver(store, "").Merge(
			id,
			otherID,
			identity.Evidence{
				Rank:  identity.RankPinned,
				Value: reason,
			},
		)
		if err != nil {
			switch {
			case errors.Is(err, identity.ErrPinnedConflict):
				http.Error(w, err.Error(), http.StatusConflict)
			case errors.Is(err, sql.ErrNoRows):
				http.Error(w, "device not found", http.StatusNotFound)
			case errors.Is(err, sqlitestore.ErrInvalidInput):
				http.Error(w, err.Error(), http.StatusBadRequest)
			default:
				http.Error(w, "merge failed", http.StatusBadRequest)
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func unmergeDeviceHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		if id == "" {
			http.Error(w, "device id is required", http.StatusBadRequest)
			return
		}
		if err := identity.NewResolver(store, "").Unmerge(id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "merge not found", http.StatusNotFound)
				return
			}
			http.Error(w, "unmerge failed", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func nodesHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tr, err := parseTimeRange(r, store, time.Now().UTC())
		if err != nil {
			writeStoreError(w, err, "nodes unavailable")
			return
		}
		nodes, err := store.ListNodes(r.Context())
		if err != nil {
			http.Error(w, "nodes unavailable", http.StatusServiceUnavailable)
			return
		}
		if nodes == nil {
			nodes = []sqlitestore.Node{}
		}
		var from, to time.Time
		if tr.Explicit {
			from, to = tr.From, tr.To
		}
		out := make([]map[string]any, 0, len(nodes))
		for _, node := range nodes {
			breakdown, err := store.LedgerBreakdown(r.Context(), from, to, node.SiteID, node.ID)
			if err != nil {
				http.Error(w, "nodes unavailable", http.StatusServiceUnavailable)
				return
			}
			bytes := flattenBreakdownMap(breakdown)
			rx, tx, tot := summaryRxTxTotal(breakdown)
			out = append(out, map[string]any{
				"id":              node.ID,
				"site_id":         node.SiteID,
				"mode":            node.Mode,
				"last_seen_at":    node.LastSeenAt,
				"last_boot_id":    node.LastBootID,
				"collect_domains": node.CollectDomains,
				"bytes":           bytes,
				"breakdown":       breakdown,
				"rx":              rx,
				"tx":              tx,
				"total":           tot,
			})
		}
		traffic, err := lastTrafficWithRange(store, r, tr, "node", "total")
		if err != nil {
			writeStoreError(w, err, "nodes unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"nodes":   out,
			"range":   tr,
			"traffic": traffic,
		})
	}
}

func nodeDetailHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tr, err := parseTimeRange(r, store, time.Now().UTC())
		if err != nil {
			writeStoreError(w, err, "node unavailable")
			return
		}
		node, err := store.GetNode(r.Context(), r.PathValue("id"))
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "node unavailable", http.StatusServiceUnavailable)
			return
		}
		var from, to time.Time
		if tr.Explicit {
			from, to = tr.From, tr.To
		}
		breakdown, err := store.LedgerBreakdown(r.Context(), from, to, node.SiteID, node.ID)
		if err != nil {
			http.Error(w, "node unavailable", http.StatusServiceUnavailable)
			return
		}
		bytes := flattenBreakdownMap(breakdown)
		rx, tx, tot := summaryRxTxTotal(breakdown)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":              node.ID,
			"site_id":         node.SiteID,
			"mode":            node.Mode,
			"last_seen_at":    node.LastSeenAt,
			"last_boot_id":    node.LastBootID,
			"collect_domains": node.CollectDomains,
			"bytes":           bytes,
			"breakdown":       breakdown,
			"rx":              rx,
			"tx":              tx,
			"total":           tot,
			"range":           tr,
			"online":          strings.TrimSpace(node.LastSeenAt) != "",
		})
	}
}

func nodeConfigHandler(store *sqlitestore.Store, mgr *direct.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			CollectDomains *bool `json:"collect_domains"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "invalid node config", http.StatusBadRequest)
			return
		}
		if body.CollectDomains == nil {
			http.Error(w, "collect_domains is required", http.StatusBadRequest)
			return
		}
		id := r.PathValue("id")
		err := store.SetNodeCollectDomains(r.Context(), id, *body.CollectDomains)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "node config update failed", http.StatusServiceUnavailable)
			return
		}
		if mgr != nil {
			mgr.RefreshPreferences(r.Context())
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":          "ok",
			"id":              id,
			"collect_domains": *body.CollectDomains,
		})
	}
}

func revokeNodeHandler(store *sqlitestore.Store, mgr *direct.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.RevokeNode(r.Context(), r.PathValue("id")); err != nil {
			http.Error(w, "revoke failed", http.StatusServiceUnavailable)
			return
		}
		if mgr != nil {
			_ = mgr.Reload(r.Context())
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func lastTrafficWithRange(
	store *sqlitestore.Store,
	r *http.Request,
	tr resolvedTimeRange,
	group, class string,
) (sqlitestore.TrafficSeries, error) {
	query, err := parseTrafficQueryWithRange(r, tr)
	if err != nil {
		return sqlitestore.TrafficSeries{}, err
	}
	hasGroup := strings.TrimSpace(r.URL.Query().Get("group")) != ""
	hasClass := strings.TrimSpace(r.URL.Query().Get("class")) != ""
	if !hasGroup {
		query.Group = group
	}
	if !hasClass && class != "" && (!hasGroup || query.Group == group) {
		query.Class = class
	}
	return store.TrafficSeries(r.Context(), query)
}

func trafficHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tr, err := parseTimeRange(r, store, time.Now().UTC())
		if err != nil {
			writeStoreError(w, err, "traffic unavailable")
			return
		}
		query, err := parseTrafficQueryWithRange(r, tr)
		if err != nil {
			writeStoreError(w, err, "traffic unavailable")
			return
		}
		series, err := store.TrafficSeries(r.Context(), query)
		if err != nil {
			writeStoreError(w, err, "traffic unavailable")
			return
		}
		writeJSON(w, http.StatusOK, series)
	}
}

func parseTrafficQueryWithRange(r *http.Request, tr resolvedTimeRange) (sqlitestore.TrafficQuery, error) {
	values := r.URL.Query()
	group := strings.TrimSpace(values.Get("group"))
	if group == "" {
		group = "node"
	}
	switch group {
	case "node", "device", "outbound", "class", "group", "process", "domain", "iface":
	default:
		return sqlitestore.TrafficQuery{}, fmt.Errorf("%w: invalid group", sqlitestore.ErrInvalidInput)
	}

	deviceID := strings.TrimSpace(values.Get("device_id"))
	if deviceID == "" {
		deviceID = strings.TrimSpace(values.Get("device"))
	}
	if (group == "group" || group == "process" || group == "domain" || group == "iface") && deviceID != "" {
		return sqlitestore.TrafficQuery{}, fmt.Errorf("%w: device filter is not supported for dimension groups", sqlitestore.ErrInvalidInput)
	}

	siteID := strings.TrimSpace(values.Get("site_id"))
	if siteID == "" {
		siteID = strings.TrimSpace(values.Get("site"))
	}
	nodeID := strings.TrimSpace(values.Get("node_id"))
	if nodeID == "" {
		nodeID = strings.TrimSpace(values.Get("node"))
	}

	query := sqlitestore.TrafficQuery{
		From:     tr.From,
		To:       tr.To,
		Group:    group,
		Class:    strings.TrimSpace(values.Get("class")),
		SiteID:   siteID,
		NodeID:   nodeID,
		DeviceID: deviceID,
		Range:    tr.Preset,
	}
	if raw := strings.TrimSpace(values.Get("bucket")); raw != "" {
		bucket, err := strconv.Atoi(raw)
		if err != nil || bucket <= 0 {
			return sqlitestore.TrafficQuery{}, fmt.Errorf("%w: invalid bucket", sqlitestore.ErrInvalidInput)
		}
		query.BucketSeconds = bucket
	}
	if query.Group == "outbound" && query.Class == "" {
		query.Class = "proxy_raw"
	}
	return query, nil
}

func dimensionsHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tr, err := parseTimeRange(r, store, time.Now().UTC())
		if err != nil {
			writeStoreError(w, err, "dimensions unavailable")
			return
		}
		values := r.URL.Query()
		dimType := strings.TrimSpace(values.Get("type"))
		switch dimType {
		case "", "group", "process", "domain", "iface":
		default:
			http.Error(w, "invalid dimension type", http.StatusBadRequest)
			return
		}
		limit := 50
		if rawLimit := strings.TrimSpace(values.Get("limit")); rawLimit != "" {
			n, err := strconv.Atoi(rawLimit)
			if err != nil || n <= 0 {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			if n > 500 {
				n = 500
			}
			limit = n
		}
		sortBy := strings.TrimSpace(values.Get("sort"))
		switch sortBy {
		case "", "total", "rx", "tx":
		default:
			http.Error(w, "invalid sort", http.StatusBadRequest)
			return
		}
		nodeID := strings.TrimSpace(values.Get("node_id"))
		if nodeID == "" {
			nodeID = strings.TrimSpace(values.Get("node"))
		}
		siteID := strings.TrimSpace(values.Get("site_id"))
		if siteID == "" {
			siteID = strings.TrimSpace(values.Get("site"))
		}

		res, err := store.ListDimensionStats(r.Context(), sqlitestore.DimensionQuery{
			Type:   dimType,
			SiteID: siteID,
			NodeID: nodeID,
			From:   tr.From,
			To:     tr.To,
			Limit:  limit,
			Sort:   sortBy,
		})
		if err != nil {
			if errors.Is(err, sqlitestore.ErrInvalidInput) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, "dimensions unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"type":     res.Type,
			"from":     res.From,
			"to":       res.To,
			"range":    tr,
			"total_rx": res.TotalRx,
			"total_tx": res.TotalTx,
			"total":    res.Total,
			"items":    res.Items,
		})
	}
}

type resolvedTimeRange struct {
	Preset   string         `json:"preset"`
	From     time.Time      `json:"from"`
	To       time.Time      `json:"to"`
	Explicit bool           `json:"-"`
	Location *time.Location `json:"-"`
}

func parseTimeRange(r *http.Request, store *sqlitestore.Store, now time.Time) (resolvedTimeRange, error) {
	now = now.UTC()
	values := r.URL.Query()

	loc := time.UTC
	if rawOffset := strings.TrimSpace(values.Get("tz_offset")); rawOffset != "" {
		offsetMin, err := strconv.Atoi(rawOffset)
		if err != nil || offsetMin < -720 || offsetMin > 840 {
			return resolvedTimeRange{}, fmt.Errorf("%w: invalid tz_offset", sqlitestore.ErrInvalidInput)
		}
		loc = time.FixedZone("", offsetMin*60)
	}
	if rawTZ := strings.TrimSpace(values.Get("tz")); rawTZ != "" {
		loaded, err := time.LoadLocation(rawTZ)
		if err != nil {
			return resolvedTimeRange{}, fmt.Errorf("%w: invalid tz", sqlitestore.ErrInvalidInput)
		}
		loc = loaded
	}

	preset := strings.ToLower(strings.TrimSpace(values.Get("range")))
	rawFrom := strings.TrimSpace(values.Get("from"))
	rawTo := strings.TrimSpace(values.Get("to"))

	if preset != "" && preset != "custom" && (rawFrom != "" || rawTo != "") {
		return resolvedTimeRange{}, fmt.Errorf("%w: from/to cannot be combined with preset range", sqlitestore.ErrInvalidInput)
	}

	if preset == "" && rawFrom == "" && rawTo == "" {
		return resolvedTimeRange{
			Preset:   "72h",
			From:     now.Add(-72 * time.Hour),
			To:       now,
			Explicit: false,
			Location: loc,
		}, nil
	}

	if preset == "custom" || (preset == "" && (rawFrom != "" || rawTo != "")) {
		if rawFrom == "" {
			return resolvedTimeRange{}, fmt.Errorf("%w: from is required for custom range", sqlitestore.ErrInvalidInput)
		}
		from, err := parseTimeBound(rawFrom, loc, false)
		if err != nil {
			return resolvedTimeRange{}, fmt.Errorf("%w: invalid from", sqlitestore.ErrInvalidInput)
		}
		to := now
		if rawTo != "" {
			to, err = parseTimeBound(rawTo, loc, true)
			if err != nil {
				return resolvedTimeRange{}, fmt.Errorf("%w: invalid to", sqlitestore.ErrInvalidInput)
			}
		}
		if from.After(to) {
			return resolvedTimeRange{}, fmt.Errorf("%w: from must not be after to", sqlitestore.ErrInvalidInput)
		}
		return resolvedTimeRange{
			Preset:   "custom",
			From:     from.UTC(),
			To:       to.UTC(),
			Explicit: true,
			Location: loc,
		}, nil
	}

	switch preset {
	case "today":
		locNow := now.In(loc)
		from := time.Date(locNow.Year(), locNow.Month(), locNow.Day(), 0, 0, 0, 0, loc).UTC()
		return resolvedTimeRange{Preset: "today", From: from, To: now, Explicit: true, Location: loc}, nil
	case "24h":
		return resolvedTimeRange{Preset: "24h", From: now.Add(-24 * time.Hour), To: now, Explicit: true, Location: loc}, nil
	case "72h":
		return resolvedTimeRange{Preset: "72h", From: now.Add(-72 * time.Hour), To: now, Explicit: true, Location: loc}, nil
	case "7d":
		return resolvedTimeRange{Preset: "7d", From: now.Add(-7 * 24 * time.Hour), To: now, Explicit: true, Location: loc}, nil
	case "30d":
		return resolvedTimeRange{Preset: "30d", From: now.Add(-30 * 24 * time.Hour), To: now, Explicit: true, Location: loc}, nil
	case "billing":
		resetDay := 1
		if store != nil {
			rd, err := store.BillingResetDay(r.Context())
			if err != nil {
				return resolvedTimeRange{}, fmt.Errorf("billing unavailable")
			}
			resetDay = rd
		}
		from, to := sqlitestore.BillingPeriodInLoc(now, resetDay, loc)
		return resolvedTimeRange{Preset: "billing", From: from, To: to, Explicit: true, Location: loc}, nil
	default:
		return resolvedTimeRange{}, fmt.Errorf("%w: invalid range", sqlitestore.ErrInvalidInput)
	}
}

func parseTimeBound(raw string, loc *time.Location, endOfDay bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UTC(), nil
	}
	if d, err := time.ParseInLocation("2006-01-02", raw, loc); err == nil {
		if endOfDay {
			return time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 999999999, loc).UTC(), nil
		}
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unrecognized time format")
}

type ruleInput struct {
	Name      string               `json:"name"`
	MatchType accounting.MatchType `json:"match_type"`
	Pattern   string               `json:"pattern"`
	Factor    float64              `json:"factor"`
	Priority  int                  `json:"priority"`
	Enabled   *bool                `json:"enabled"`
}

func (ri ruleInput) toRule() accounting.MultiplierRule {
	enabled := true
	if ri.Enabled != nil {
		enabled = *ri.Enabled
	}
	return accounting.MultiplierRule{
		Name:      ri.Name,
		MatchType: ri.MatchType,
		Pattern:   ri.Pattern,
		Factor:    ri.Factor,
		Priority:  ri.Priority,
		Enabled:   enabled,
	}
}

func listProxyRulesHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rules, err := store.ListMultiplierRules(r.Context())
		if err != nil {
			http.Error(w, "rules unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
	}
}

func createProxyRuleHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body ruleInput
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "invalid rule", http.StatusBadRequest)
			return
		}
		created, err := store.CreateMultiplierRule(r.Context(), body.toRule())
		if err != nil {
			if errors.Is(err, sqlitestore.ErrInvalidInput) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, "rule save failed", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rule": created})
	}
}

func updateProxyRuleHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid rule id", http.StatusBadRequest)
			return
		}
		var body ruleInput
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "invalid rule", http.StatusBadRequest)
			return
		}
		updated, err := store.UpdateMultiplierRule(r.Context(), id, body.toRule())
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "rule not found", http.StatusNotFound)
			return
		}
		if err != nil {
			if errors.Is(err, sqlitestore.ErrInvalidInput) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, "rule update failed", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rule": updated})
	}
}

func deleteProxyRuleHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid rule id", http.StatusBadRequest)
			return
		}
		err = store.DeleteMultiplierRule(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "rule not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "delete failed", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func exportProxyRulesHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rules, err := store.ListMultiplierRules(r.Context())
		if err != nil {
			http.Error(w, "export unavailable", http.StatusServiceUnavailable)
			return
		}
		type exportedRule struct {
			Name      string               `json:"name"`
			MatchType accounting.MatchType `json:"match_type"`
			Pattern   string               `json:"pattern"`
			Factor    float64              `json:"factor"`
			Priority  int                  `json:"priority"`
			Enabled   bool                 `json:"enabled"`
		}
		exported := make([]exportedRule, 0, len(rules))
		for _, rule := range rules {
			exported = append(exported, exportedRule{
				Name:      rule.Name,
				MatchType: rule.MatchType,
				Pattern:   rule.Pattern,
				Factor:    rule.Factor,
				Priority:  rule.Priority,
				Enabled:   rule.Enabled,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="lantally-multiplier-rules.json"`)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version":     1,
			"exported_at": time.Now().UTC().Format(time.RFC3339),
			"rules":       exported,
		})
	}
}

func importProxyRulesHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode  string      `json:"mode"`
			Rules []ruleInput `json:"rules"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid import payload", http.StatusBadRequest)
			return
		}
		if body.Rules == nil {
			http.Error(w, "rules array is required", http.StatusBadRequest)
			return
		}
		rules := make([]accounting.MultiplierRule, 0, len(body.Rules))
		for _, item := range body.Rules {
			rules = append(rules, item.toRule())
		}
		saved, err := store.ImportMultiplierRules(r.Context(), body.Mode, rules)
		if err != nil {
			if errors.Is(err, sqlitestore.ErrInvalidInput) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, "rule import failed", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "ok",
			"imported": len(rules),
			"rules":    saved,
		})
	}
}

func previewProxyRulesHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Rules       *[]ruleInput `json:"rules"`
			SampleNames []string     `json:"sample_names"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid preview payload", http.StatusBadRequest)
			return
		}
		var draft *[]accounting.MultiplierRule
		if body.Rules != nil {
			converted := make([]accounting.MultiplierRule, 0, len(*body.Rules))
			for _, item := range *body.Rules {
				converted = append(converted, item.toRule())
			}
			draft = &converted
		}
		matches, err := store.PreviewMultiplierRules(r.Context(), draft, body.SampleNames)
		if err != nil {
			if errors.Is(err, sqlitestore.ErrInvalidInput) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, "rule preview unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"matches": matches})
	}
}

func flattenBreakdownMap(breakdown map[string]sqlitestore.RxTxTotal) map[string]uint64 {
	out := make(map[string]uint64, len(breakdown))
	for k, v := range breakdown {
		out[k] = v.Total
	}
	return out
}

func summaryRxTxTotal(breakdown map[string]sqlitestore.RxTxTotal) (rx, tx, total uint64) {
	if t, ok := breakdown[accounting.ClassTotal]; ok && t.Total > 0 {
		return t.Rx, t.Tx, t.Total
	}
	d := breakdown[accounting.ClassDirect]
	p := breakdown[accounting.ClassProxyRaw]
	rx = d.Rx + p.Rx
	tx = d.Tx + p.Tx
	return rx, tx, rx + tx
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeStoreError(w http.ResponseWriter, err error, unavailableMsg string) {
	switch {
	case errors.Is(err, sqlitestore.ErrInvalidInput):
		http.Error(w, redact.String(err.Error()), http.StatusBadRequest)
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		http.Error(w, unavailableMsg, http.StatusServiceUnavailable)
	}
}

type watchInput struct {
	Name           string                     `json:"name"`
	Matchers       accounting.WatchMatcherMap `json:"matchers"`
	OverrideFactor *float64                   `json:"override_factor"`
	QuotaValue     float64                    `json:"quota_value"`
	QuotaUnit      string                     `json:"quota_unit"`
	QuotaBytes     uint64                     `json:"quota_bytes"`
	ResetType      accounting.ResetType       `json:"reset_type"`
	ResetDay       int                        `json:"reset_day"`
	ExpiresAt      string                     `json:"expires_at"`
	ExpireAt       string                     `json:"expire_at"`
	ResetDate      string                     `json:"reset_date"`
	Pinned         bool                       `json:"pinned"`
	Enabled        *bool                      `json:"enabled"`
}

func (wi watchInput) toWatchItem() accounting.WatchItem {
	enabled := true
	if wi.Enabled != nil {
		enabled = *wi.Enabled
	}
	expiresAt := strings.TrimSpace(wi.ExpiresAt)
	if expiresAt == "" {
		expiresAt = strings.TrimSpace(wi.ExpireAt)
	}
	if expiresAt == "" {
		expiresAt = strings.TrimSpace(wi.ResetDate)
	}
	return accounting.WatchItem{
		Name:           wi.Name,
		Matchers:       wi.Matchers,
		OverrideFactor: wi.OverrideFactor,
		QuotaValue:     wi.QuotaValue,
		QuotaUnit:      wi.QuotaUnit,
		QuotaBytes:     wi.QuotaBytes,
		ResetType:      wi.ResetType,
		ResetDay:       wi.ResetDay,
		ExpiresAt:      expiresAt,
		Pinned:         wi.Pinned,
		Enabled:        enabled,
	}
}

func parseWatchBucket(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("bucket"))
	if raw == "" {
		return 0, nil
	}
	b, err := strconv.Atoi(raw)
	if err != nil || b <= 0 {
		return 0, fmt.Errorf("%w: invalid bucket", sqlitestore.ErrInvalidInput)
	}
	return b, nil
}

func listWatchHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UTC()
		tr, err := parseTimeRange(r, store, now)
		if err != nil {
			writeStoreError(w, err, "watch unavailable")
			return
		}
		bucket, err := parseWatchBucket(r)
		if err != nil {
			writeStoreError(w, err, "watch unavailable")
			return
		}
		var from, to time.Time
		if tr.Explicit {
			from, to = tr.From, tr.To
		}
		items, err := store.EvaluateWatchItems(r.Context(), from, to, bucket, tr.Location, now)
		if err != nil {
			writeStoreError(w, err, "watch unavailable")
			return
		}
		if items == nil {
			items = []sqlitestore.WatchItemStatus{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"items": items,
			"range": tr,
		})
	}
}

func createWatchHandler(store *sqlitestore.Store, mgr *direct.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body watchInput
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid watch item", http.StatusBadRequest)
			return
		}
		created, err := store.CreateWatchItem(r.Context(), body.toWatchItem())
		if err != nil {
			writeStoreError(w, err, "watch save failed")
			return
		}
		if mgr != nil {
			mgr.RefreshPreferences(r.Context())
		}
		writeJSON(w, http.StatusCreated, created)
	}
}

func getWatchHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid watch id", http.StatusBadRequest)
			return
		}
		now := time.Now().UTC()
		tr, err := parseTimeRange(r, store, now)
		if err != nil {
			writeStoreError(w, err, "watch unavailable")
			return
		}
		bucket, err := parseWatchBucket(r)
		if err != nil {
			writeStoreError(w, err, "watch unavailable")
			return
		}
		item, err := store.GetWatchItem(r.Context(), id)
		if err != nil {
			writeStoreError(w, err, "watch unavailable")
			return
		}
		var from, to time.Time
		if tr.Explicit {
			from, to = tr.From, tr.To
		}
		status, err := store.EvaluateWatchItem(r.Context(), item, from, to, bucket, tr.Location, now)
		if err != nil {
			writeStoreError(w, err, "watch unavailable")
			return
		}
		writeJSON(w, http.StatusOK, struct {
			sqlitestore.WatchItemStatus
			Buckets []sqlitestore.WatchPoint `json:"buckets"`
		}{
			WatchItemStatus: status,
			Buckets:         status.Series,
		})
	}
}

func updateWatchHandler(store *sqlitestore.Store, mgr *direct.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid watch id", http.StatusBadRequest)
			return
		}
		var body watchInput
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid watch item", http.StatusBadRequest)
			return
		}
		updated, err := store.UpdateWatchItem(r.Context(), id, body.toWatchItem())
		if err != nil {
			writeStoreError(w, err, "watch update failed")
			return
		}
		if err := store.EvaluateAlerts(r.Context(), time.Now().UTC()); err != nil {
			writeStoreError(w, err, "watch update failed")
			return
		}
		if mgr != nil {
			mgr.RefreshPreferences(r.Context())
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

func deleteWatchHandler(store *sqlitestore.Store, mgr *direct.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid watch id", http.StatusBadRequest)
			return
		}
		if err := store.DeleteWatchItem(r.Context(), id); err != nil {
			writeStoreError(w, err, "watch delete failed")
			return
		}
		if mgr != nil {
			mgr.RefreshPreferences(r.Context())
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

type providerInput struct {
	Name            string    `json:"name"`
	Unit            string    `json:"unit"`
	QuotaUnit       string    `json:"quota_unit"`
	TotalValue      *float64  `json:"total_value"`
	TotalBytes      *uint64   `json:"total_bytes"`
	UsedValue       *float64  `json:"used_value"`
	UsedBytes       *uint64   `json:"used_bytes"`
	UploadBytes     *uint64   `json:"upload_bytes"`
	DownloadBytes   *uint64   `json:"download_bytes"`
	Expire          *int64    `json:"expire"`
	ExpireAt        *int64    `json:"expire_at"`
	ResetDay        *int      `json:"reset_day"`
	OutboundPattern *string   `json:"outbound_pattern"`
	LinkedNodes     *[]string `json:"linked_nodes"`
	Proxies         *[]string `json:"proxies"`
	Enabled         *bool     `json:"enabled"`
}

func (pi providerInput) toStoreInput() sqlitestore.ProviderInput {
	unit := strings.TrimSpace(pi.Unit)
	if unit == "" {
		unit = strings.TrimSpace(pi.QuotaUnit)
	}
	expireAt := pi.ExpireAt
	if expireAt == nil {
		expireAt = pi.Expire
	}
	return sqlitestore.ProviderInput{
		Name:            pi.Name,
		Unit:            unit,
		TotalValue:      pi.TotalValue,
		TotalBytes:      pi.TotalBytes,
		UsedValue:       pi.UsedValue,
		UsedBytes:       pi.UsedBytes,
		UploadBytes:     pi.UploadBytes,
		DownloadBytes:   pi.DownloadBytes,
		ExpireAt:        expireAt,
		ResetDay:        pi.ResetDay,
		OutboundPattern: pi.OutboundPattern,
		LinkedNodes:     pi.LinkedNodes,
		Proxies:         pi.Proxies,
		Enabled:         pi.Enabled,
	}
}

func listProxyProvidersHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UTC()
		tr, err := parseTimeRange(r, store, now)
		if err != nil {
			writeStoreError(w, err, "providers unavailable")
			return
		}
		providers, summary, err := store.ListProviderStatuses(r.Context(), now, tr.Location)
		if err != nil {
			writeStoreError(w, err, "providers unavailable")
			return
		}
		if providers == nil {
			providers = []sqlitestore.ProviderStatus{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"providers": providers,
			"summary":   summary,
		})
	}
}

func createProxyProviderHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body providerInput
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid provider", http.StatusBadRequest)
			return
		}
		created, err := store.CreateProviderSubscription(r.Context(), body.toStoreInput())
		if err != nil {
			writeStoreError(w, err, "provider save failed")
			return
		}
		writeJSON(w, http.StatusCreated, created)
	}
}

func updateProxyProviderHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid provider id", http.StatusBadRequest)
			return
		}
		var body providerInput
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid provider", http.StatusBadRequest)
			return
		}
		updated, err := store.UpdateProviderSubscription(r.Context(), id, body.toStoreInput())
		if err != nil {
			writeStoreError(w, err, "provider update failed")
			return
		}
		if err := store.EvaluateAlerts(r.Context(), time.Now().UTC()); err != nil {
			writeStoreError(w, err, "provider update failed")
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

func deleteProxyProviderHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid provider id", http.StatusBadRequest)
			return
		}
		if err := store.DeleteProviderSubscription(r.Context(), id); err != nil {
			writeStoreError(w, err, "provider delete failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// EnsureFirstRunAdmin creates an admin password only when
// LANTALLY_BOOTSTRAP_ADMIN_PASSWORD is set and no admin exists yet.
// Otherwise the first-run web wizard sets the password.
func EnsureFirstRunAdmin(store *sqlitestore.Store, stdout io.Writer) error {
	hasAdmin, err := store.HasAdminCredential()
	if err != nil {
		return err
	}
	if hasAdmin {
		return nil
	}
	envPassword := strings.TrimSpace(os.Getenv("LANTALLY_BOOTSTRAP_ADMIN_PASSWORD"))
	if envPassword == "" {
		return nil
	}
	if err := store.CreateAdminCredential(envPassword); err != nil {
		return err
	}
	if stdout == nil {
		stdout = io.Discard
	}
	_, err = fmt.Fprintln(stdout, "LanTally admin password loaded from LANTALLY_BOOTSTRAP_ADMIN_PASSWORD")
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

func staticSPAHandler(staticFS fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "..") {
			http.NotFound(w, r)
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		if clean == "/v1" || strings.HasPrefix(clean, "/v1/") || clean == "/agents" || strings.HasPrefix(clean, "/agents/") {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rel := strings.TrimPrefix(clean, "/")
		if rel == "" || rel == "index.html" {
			serveStaticFile(w, r, staticFS, "index.html", "no-cache")
			return
		}
		if info, err := fs.Stat(staticFS, rel); err == nil && !info.IsDir() {
			cacheControl := "no-cache"
			if strings.HasPrefix(clean, "/assets/") {
				cacheControl = "public, max-age=31536000, immutable"
			}
			serveStaticFile(w, r, staticFS, rel, cacheControl)
			return
		}
		if strings.HasPrefix(clean, "/assets/") || path.Ext(rel) != "" {
			http.NotFound(w, r)
			return
		}
		serveStaticFile(w, r, staticFS, "index.html", "no-cache")
	})
}

func serveStaticFile(w http.ResponseWriter, r *http.Request, staticFS fs.FS, name, cacheControl string) {
	f, err := staticFS.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	if ct := staticContentType(name); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if cacheControl != "" {
		w.Header().Set("Cache-Control", cacheControl)
	}

	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, path.Base(name), info.ModTime(), rs)
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "failed to read static asset", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, path.Base(name), info.ModTime(), bytes.NewReader(data))
}

func staticContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".json":
		return "application/json; charset=utf-8"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	default:
		return mime.TypeByExtension(path.Ext(name))
	}
}
