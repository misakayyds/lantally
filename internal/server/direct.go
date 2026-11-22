package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/misakayyds/lantally/internal/collector/direct"
	"github.com/misakayyds/lantally/internal/collector/mihomo"
	"github.com/misakayyds/lantally/internal/redact"
	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
)

const defaultDirectProbeTimeout = 5 * time.Second

func listDirectCollectorsHandler(store *sqlitestore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		collectors, err := store.ListDirectCollectors(r.Context())
		if err != nil {
			writeStoreError(w, err, "direct collectors unavailable")
			return
		}
		if collectors == nil {
			collectors = []sqlitestore.DirectCollector{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"collectors": collectors,
		})
	}
}

func createDirectCollectorHandler(store *sqlitestore.Store, mgr *direct.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body sqlitestore.DirectCollectorInput
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid direct collector", http.StatusBadRequest)
			return
		}
		created, err := store.CreateDirectCollector(r.Context(), body)
		if err != nil {
			writeStoreError(w, err, "direct collector save failed")
			return
		}
		if mgr != nil {
			if syncErr := mgr.SyncCollector(r.Context(), created.ID); syncErr != nil {
				log.Printf("sync created direct collector %d: %v", created.ID, syncErr)
			}
		}
		writeJSON(w, http.StatusCreated, created)
	}
}

func updateDirectCollectorHandler(store *sqlitestore.Store, mgr *direct.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid direct collector id", http.StatusBadRequest)
			return
		}
		var body sqlitestore.DirectCollectorInput
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid direct collector", http.StatusBadRequest)
			return
		}
		updated, err := store.UpdateDirectCollector(r.Context(), id, body)
		if err != nil {
			writeStoreError(w, err, "direct collector update failed")
			return
		}
		if mgr != nil {
			if syncErr := mgr.SyncCollector(r.Context(), updated.ID); syncErr != nil {
				log.Printf("sync updated direct collector %d: %v", updated.ID, syncErr)
			}
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

func deleteDirectCollectorHandler(store *sqlitestore.Store, mgr *direct.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid direct collector id", http.StatusBadRequest)
			return
		}
		if err := store.DeleteDirectCollector(r.Context(), id); err != nil {
			writeStoreError(w, err, "direct collector delete failed")
			return
		}
		if mgr != nil {
			mgr.RemoveCollector(id)
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func testDirectCollectorConfigHandler(mgr *direct.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL    string `json:"url"`
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid test request", http.StatusBadRequest)
			return
		}
		rawURL, err := sqlitestore.ValidateDirectCollectorURL(body.URL)
		if err != nil {
			writeStoreError(w, err, "invalid url")
			return
		}
		res := runDirectProbe(r.Context(), mgr, rawURL, body.Secret)
		writeJSON(w, http.StatusOK, res)
	}
}

func testDirectCollectorByIDHandler(store *sqlitestore.Store, mgr *direct.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid direct collector id", http.StatusBadRequest)
			return
		}
		rawBody, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			http.Error(w, "invalid test request", http.StatusBadRequest)
			return
		}
		var overrides struct {
			URL         *string `json:"url"`
			Secret      *string `json:"secret"`
			ClearSecret bool    `json:"clear_secret"`
		}
		if len(bytes.TrimSpace(rawBody)) > 0 {
			if err := json.Unmarshal(rawBody, &overrides); err != nil {
				http.Error(w, "invalid test request", http.StatusBadRequest)
				return
			}
		}

		dc, err := store.GetDirectCollectorRuntime(r.Context(), id)
		if err != nil {
			writeStoreError(w, err, "direct collector unavailable")
			return
		}

		targetURL := dc.RawURL
		if overrides.URL != nil {
			trimmedOverride := strings.TrimSpace(*overrides.URL)
			if trimmedOverride != "" && trimmedOverride == redact.URL(dc.RawURL) {
				targetURL = dc.RawURL
			} else {
				targetURL, err = sqlitestore.ValidateDirectCollectorURL(*overrides.URL)
				if err != nil {
					writeStoreError(w, err, "invalid url")
					return
				}
			}
		}
		secret := dc.SecretPlain
		switch {
		case overrides.ClearSecret:
			secret = ""
		case overrides.Secret == nil || *overrides.Secret == "********":
			// Preserve existing stored secret.
		case *overrides.Secret == "":
			secret = ""
		default:
			secret = *overrides.Secret
		}

		res := runDirectProbe(r.Context(), mgr, targetURL, secret)
		writeJSON(w, http.StatusOK, res)
	}
}

func toggleDirectCollectorHandler(store *sqlitestore.Store, mgr *direct.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid direct collector id", http.StatusBadRequest)
			return
		}
		rawBody, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil {
			http.Error(w, "invalid toggle request", http.StatusBadRequest)
			return
		}
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if len(bytes.TrimSpace(rawBody)) > 0 {
			if err := json.Unmarshal(rawBody, &body); err != nil {
				http.Error(w, "invalid toggle request", http.StatusBadRequest)
				return
			}
		}
		updated, err := store.ToggleDirectCollector(r.Context(), id, body.Enabled)
		if err != nil {
			writeStoreError(w, err, "direct collector toggle failed")
			return
		}
		if mgr != nil {
			if syncErr := mgr.SyncCollector(r.Context(), id); syncErr != nil {
				log.Printf("sync toggled direct collector %d: %v", id, syncErr)
			}
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

func runDirectProbe(ctx context.Context, mgr *direct.Manager, rawURL, secret string) mihomo.ProbeResult {
	if mgr != nil {
		return mgr.ProbeConfig(ctx, rawURL, secret)
	}
	probeCtx, cancel := context.WithTimeout(ctx, defaultDirectProbeTimeout)
	defer cancel()
	return direct.Probe(probeCtx, rawURL, secret, nil)
}
