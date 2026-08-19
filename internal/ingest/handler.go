package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/alert"
	"github.com/misakayyds/lantally/internal/identity"
	"github.com/misakayyds/lantally/internal/protocol"
	"github.com/misakayyds/lantally/internal/store/metrics"
	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
)

type Handler struct {
	store   *sqlitestore.Store
	metrics *metrics.Writer
}

func NewHandler(store *sqlitestore.Store) *Handler {
	return &Handler{store: store}
}

func (h *Handler) SetMetrics(writer *metrics.Writer) {
	h.metrics = writer
}

func Routes(store *sqlitestore.Store) http.Handler {
	handler := NewHandler(store)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ingest", handler.Ingest)
	mux.HandleFunc("GET /healthz", handler.Healthz)
	return mux
}

func (h *Handler) Ingest(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		http.Error(w, "content encoding must be gzip", http.StatusBadRequest)
		return
	}
	if !validJSONContentType(r.Header.Get("Content-Type")) {
		http.Error(w, "content type must be application/json", http.StatusBadRequest)
		return
	}

	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	node, err := h.store.Authenticate(r.Context(), token)
	if err != nil {
		if errors.Is(err, sqlitestore.ErrUnauthorized) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, protocol.MaxDecodeSize+1))
	if err != nil || len(raw) > protocol.MaxDecodeSize || !isGzip(raw) {
		http.Error(w, "invalid gzip batch", http.StatusBadRequest)
		return
	}

	batch, err := protocol.Decode(raw)
	if err != nil {
		http.Error(w, "invalid batch", http.StatusBadRequest)
		return
	}
	if batch.NodeID != node.ID || batch.SiteID != node.SiteID {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	inserted, err := h.store.InsertBatch(
		r.Context(),
		batch.NodeID,
		batch.BootID,
		batch.Sequence,
		raw,
	)
	if errors.Is(err, sqlitestore.ErrSequenceRegression) {
		http.Error(w, "sequence regression", http.StatusConflict)
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if err := h.account(r.Context(), batch); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if err := h.observeNode(r.Context(), batch, inserted); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Status    string `json:"status"`
		Duplicate bool   `json:"duplicate"`
	}{
		Status:    "ok",
		Duplicate: !inserted,
	})
}

func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Ping(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

func (h *Handler) account(ctx context.Context, batch protocol.Batch) error {
	multipliers, err := h.store.ListMultipliers(ctx)
	if err != nil {
		return err
	}
	increments := accounting.NodeIncrements(batch, multipliers)
	resolver := identity.NewResolver(h.store, batch.NodeID)
	for _, obs := range batch.Devices {
		deviceID, _, err := resolver.Resolve(batch.SiteID, obs, batch.SampledAt)
		if err != nil {
			if errors.Is(err, identity.ErrConflictingEvidence) {
				continue
			}
			return err
		}
		increments = append(increments, accounting.DeviceIncrements(obs, deviceID, multipliers)...)
	}
	applied, err := h.store.ApplyLedgerOnce(
		ctx,
		batch.SiteID,
		batch.NodeID,
		batch.BootID,
		batch.Sequence,
		batch.SampledAt,
		increments,
	)
	if err != nil {
		return err
	}
	if applied && h.metrics != nil {
		totals, totalsErr := h.store.NodeLedgerTotals(ctx, batch.SiteID, batch.NodeID)
		if totalsErr == nil {
			_ = h.metrics.Write(ctx, ledgerSamples(batch.SiteID, batch.NodeID, totals))
		}
	}
	return nil
}

func (h *Handler) observeNode(ctx context.Context, batch protocol.Batch, inserted bool) error {
	prevBoot, err := h.store.LastBootID(ctx, batch.NodeID)
	if err != nil {
		return err
	}
	interval := time.Duration(batch.IntervalMS) * time.Millisecond
	if err := h.store.TouchNode(ctx, batch.NodeID, batch.BootID, interval, time.Now().UTC()); err != nil {
		return err
	}
	if !inserted {
		return h.store.ResolveFingerprint(ctx, "silence|"+batch.NodeID, time.Now().UTC())
	}
	if prevBoot != "" && prevBoot != batch.BootID {
		rec, fired := alert.EvalReset(true, "")
		if fired {
			rec.SiteID = batch.SiteID
			rec.NodeID = batch.NodeID
			rec.ObservedAt = time.Now().UTC()
			rec.Fingerprint = "reset|" + batch.NodeID + "|" + batch.BootID
			if err := h.store.RaiseAlert(ctx, rec); err != nil {
				return err
			}
		}
		return nil
	}
	for _, gap := range batch.Gaps {
		rec, fired := alert.EvalReset(false, string(gap.Reason))
		if !fired {
			continue
		}
		rec.SiteID = batch.SiteID
		rec.NodeID = batch.NodeID
		rec.ObservedAt = time.Now().UTC()
		rec.Fingerprint = "reset|" + batch.NodeID + "|" + string(gap.Reason) + "|" + gap.From.UTC().Format(time.RFC3339)
		if err := h.store.RaiseAlert(ctx, rec); err != nil {
			return err
		}
	}
	return h.store.ResolveFingerprint(ctx, "silence|"+batch.NodeID, time.Now().UTC())
}

func ledgerSamples(siteID, nodeID string, totals map[string]uint64) []metrics.Sample {
	samples := make([]metrics.Sample, 0, len(totals))
	for class, value := range totals {
		if value == 0 {
			continue
		}
		samples = append(samples, metrics.Sample{
			Name:  "lantally_bytes_total",
			Value: float64(value),
			Labels: map[string]string{
				"site_id": siteID,
				"node_id": nodeID,
				"class":   class,
			},
		})
	}
	return samples
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	return token, token != "" && !strings.ContainsAny(token, " \t\r\n")
}

func validJSONContentType(header string) bool {
	mediaType, params, err := mime.ParseMediaType(header)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false
	}
	for name, value := range params {
		if !strings.EqualFold(name, "charset") || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

func isGzip(raw []byte) bool {
	return len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b
}
