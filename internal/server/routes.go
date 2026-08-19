package server

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/misakayyds/lantally/internal/identity"

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
	mux.HandleFunc("GET /v1/traffic", requireSession(sessions, trafficHandler(store)))
	mux.HandleFunc("GET /v1/devices", requireSession(sessions, devicesHandler(store)))
	mux.HandleFunc("GET /v1/devices/{id}", requireSession(sessions, deviceDetailHandler(store)))
	mux.HandleFunc("PUT /v1/devices/{id}", requireSession(sessions, renameDeviceHandler(store)))
	mux.HandleFunc("POST /v1/devices/{id}/merge", requireSession(sessions, mergeDeviceHandler(store)))
	mux.HandleFunc("POST /v1/devices/{id}/unmerge", requireSession(sessions, unmergeDeviceHandler(store)))
	mux.HandleFunc("GET /v1/nodes", requireSession(sessions, nodesHandler(store)))
	mux.HandleFunc("GET /v1/proxy", requireSession(sessions, proxyHandler(store)))
	mux.HandleFunc("PUT /v1/proxy/multipliers", requireSession(sessions, setMultiplierHandler(store)))
	mux.HandleFunc("PUT /v1/proxy/billing", requireSession(sessions, setBillingHandler(store)))
	mux.HandleFunc("GET /v1/alerts", requireSession(sessions, alertsHandler(store)))
	mux.HandleFunc("POST /v1/alerts/{id}/ack", requireSession(sessions, ackAlertHandler(store)))
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
		outbounds, err := store.ListOutboundLedgers(r.Context())
		if err != nil {
			http.Error(w, "proxy unavailable", http.StatusServiceUnavailable)
			return
		}
		traffic, err := lastTraffic(store, r, "outbound", "proxy_raw")
		if err != nil {
			http.Error(w, "proxy unavailable", http.StatusServiceUnavailable)
			return
		}
		billing, err := store.BillingStatus(r.Context(), time.Now().UTC())
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
			"outbounds": outbounds,
			"traffic":   traffic,
			"billing":   billing,
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
			http.Error(w, err.Error(), http.StatusBadRequest)
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
		id := r.PathValue("id")
		device, merged, err := store.GetDevice(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "device not found", http.StatusNotFound)
			return
		}
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
		reason := strings.TrimSpace(body.Reason)
		if reason == "" {
			reason = "manual"
		}
		if err := store.MergeIdentities(
			r.PathValue("id"),
			strings.TrimSpace(body.OtherID),
			int(identity.RankPinned),
			reason,
			time.Now().UTC(),
		); err != nil {
			http.Error(w, "merge failed", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func unmergeDeviceHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.UnmergeIdentity(r.PathValue("id")); err != nil {
			http.Error(w, "unmerge failed", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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
	query, err := parseTrafficQuery(r)
	if err != nil {
		return sqlitestore.TrafficSeries{}, err
	}
	if r.URL.Query().Get("group") == "" {
		query.Group = group
	}
	if r.URL.Query().Get("class") == "" && class != "" {
		query.Class = class
	}
	return store.TrafficSeries(r.Context(), query)
}

func trafficHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query, err := parseTrafficQuery(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		series, err := store.TrafficSeries(r.Context(), query)
		if err != nil {
			http.Error(w, "traffic unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(series)
	}
}

func parseTrafficQuery(r *http.Request) (sqlitestore.TrafficQuery, error) {
	values := r.URL.Query()
	group := values.Get("group")
	if group == "" {
		group = "node"
	}
	switch group {
	case "node", "device", "outbound", "class":
	default:
		return sqlitestore.TrafficQuery{}, fmt.Errorf("invalid group")
	}

	now := time.Now().UTC()
	query := sqlitestore.TrafficQuery{
		From:     now.Add(-72 * time.Hour),
		To:       now,
		Group:    group,
		Class:    values.Get("class"),
		NodeID:   values.Get("node"),
		DeviceID: values.Get("device"),
	}
	if raw := values.Get("from"); raw != "" {
		from, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return sqlitestore.TrafficQuery{}, fmt.Errorf("invalid from")
		}
		query.From = from.UTC()
	}
	if raw := values.Get("to"); raw != "" {
		to, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return sqlitestore.TrafficQuery{}, fmt.Errorf("invalid to")
		}
		query.To = to.UTC()
	}
	if raw := values.Get("bucket"); raw != "" {
		bucket, err := strconv.Atoi(raw)
		if err != nil || bucket <= 0 {
			return sqlitestore.TrafficQuery{}, fmt.Errorf("invalid bucket")
		}
		query.BucketSeconds = bucket
	}
	if query.Group == "outbound" && query.Class == "" {
		query.Class = "proxy_raw"
	}
	return query, nil
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
