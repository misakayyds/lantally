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
	store    *sqlitestore.Store
	metrics  *metrics.Writer
	nodeOpts accounting.NodeOptions
}

func NewHandler(store *sqlitestore.Store) *Handler {
	return &Handler{store: store}
}

func (h *Handler) SetMetrics(writer *metrics.Writer) {
	h.metrics = writer
}

func (h *Handler) SetNodeOptions(opts accounting.NodeOptions) {
	h.nodeOpts = opts
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
	var ver protocol.VersionError
	if errors.As(err, &ver) {
		http.Error(w, ver.Error(), http.StatusBadRequest)
		return
	}
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
	if _, err := h.IngestBatch(r.Context(), batch); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if err := h.observeNode(r.Context(), batch, inserted); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	collectDomains, watchDomains, err := h.store.NodeDomainPreferences(r.Context(), batch.NodeID)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	resp := protocol.IngestResponse{
		Status:       "ok",
		Duplicate:    !inserted,
		WatchDomains: watchDomains,
	}
	if collectDomains {
		v := true
		resp.CollectDomains = &v
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Ping(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

// IngestBatch applies node, device, dimension, proxy catalog, provider, and optional
// VictoriaMetrics updates idempotently for any Batch (agent or direct collector).
func (h *Handler) IngestBatch(ctx context.Context, batch protocol.Batch) (bool, error) {
	increments := accounting.NodeIncrementsWithOptions(batch, nil, h.effectiveNodeOptions(batch.Metadata))
	resolver := identity.NewResolver(h.store, batch.NodeID)
	for _, obs := range batch.Devices {
		deviceID, _, err := resolver.Resolve(batch.SiteID, obs, batch.SampledAt)
		if err != nil {
			if errors.Is(err, identity.ErrConflictingEvidence) {
				continue
			}
			return false, err
		}
		increments = append(increments, accounting.DeviceIncrements(obs, deviceID, nil)...)
	}

	dims := make([]sqlitestore.DimensionIncrement, 0, len(batch.Dimensions)+len(batch.Interfaces))
	hasIfaceDim := false
	for _, d := range batch.Dimensions {
		if d.Type == protocol.DimIface {
			hasIfaceDim = true
		}
		if d.RxDelta == 0 && d.TxDelta == 0 {
			continue
		}
		if strings.TrimSpace(d.Key) == "" {
			continue
		}
		dims = append(dims, sqlitestore.DimensionIncrement{
			Type: string(d.Type),
			Key:  d.Key,
			Rx:   d.RxDelta,
			Tx:   d.TxDelta,
		})
	}
	if !hasIfaceDim {
		for _, inc := range accounting.IfaceIncrements(batch.Interfaces) {
			if inc.Rx == 0 && inc.Tx == 0 {
				continue
			}
			dims = append(dims, sqlitestore.DimensionIncrement{
				Type: string(protocol.DimIface),
				Key:  inc.Iface,
				Rx:   inc.Rx,
				Tx:   inc.Tx,
			})
		}
	}

	applied, err := h.store.ApplyLedgerWithDimensionsOnce(
		ctx,
		batch.SiteID,
		batch.NodeID,
		batch.BootID,
		batch.Sequence,
		batch.SampledAt,
		increments,
		dims,
	)
	if err != nil {
		return false, err
	}
	if applied {
		if entries := collectProxyCatalogEntries(batch); len(entries) > 0 {
			if err := h.store.UpsertProxyNodeCatalog(ctx, batch.NodeID, entries, batch.SampledAt); err != nil {
				return false, err
			}
		}
		if len(batch.Providers) > 0 {
			if err := h.store.UpsertMihomoProviders(ctx, batch.NodeID, batch.Providers, batch.SampledAt); err != nil {
				return false, err
			}
		}
	}
	if applied && h.metrics != nil {
		totals, totalsErr := h.store.NodeLedgerTotals(ctx, batch.SiteID, batch.NodeID)
		if totalsErr == nil {
			_ = h.metrics.Write(ctx, ledgerSamples(batch.SiteID, batch.NodeID, totals))
		}
	}
	return applied, nil
}

func collectProxyCatalogEntries(batch protocol.Batch) []sqlitestore.ProxyNodeCatalogEntry {
	byName := map[string]sqlitestore.ProxyNodeCatalogEntry{}
	add := func(name, providerName string) {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || isBuiltinProxyName(trimmed) {
			return
		}
		cur, exists := byName[trimmed]
		if !exists || (cur.ProviderName == "" && providerName != "") {
			byName[trimmed] = sqlitestore.ProxyNodeCatalogEntry{
				Name:         trimmed,
				ProviderName: strings.TrimSpace(providerName),
			}
		}
	}
	for _, p := range batch.Providers {
		for _, member := range p.Proxies {
			add(member, p.Name)
		}
	}
	for _, name := range batch.ProxyNodes {
		add(name, "")
	}
	if batch.Proxy != nil {
		for _, ob := range batch.Proxy.ByOutbound {
			add(ob.Name, "")
		}
	}
	if len(byName) == 0 {
		return nil
	}
	entries := make([]sqlitestore.ProxyNodeCatalogEntry, 0, len(byName))
	for _, entry := range byName {
		entries = append(entries, entry)
	}
	return entries
}

func isBuiltinProxyName(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL":
		return true
	default:
		return false
	}
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

func (h *Handler) effectiveNodeOptions(metadata json.RawMessage) accounting.NodeOptions {
	if h.nodeOpts.ExcludeIfacePrefixes != nil || len(h.nodeOpts.IncludeIfaces) > 0 {
		return h.nodeOpts
	}
	if len(metadata) == 0 {
		return accounting.NodeOptions{}
	}
	var meta struct {
		IfaceExcludePrefixes *[]string `json:"iface_exclude_prefixes"`
		IfaceInclude         []string  `json:"iface_include"`
	}
	if err := json.Unmarshal(metadata, &meta); err != nil {
		return accounting.NodeOptions{}
	}
	var opts accounting.NodeOptions
	if meta.IfaceExcludePrefixes != nil {
		opts.ExcludeIfacePrefixes = *meta.IfaceExcludePrefixes
	}
	if len(meta.IfaceInclude) > 0 {
		opts.IncludeIfaces = meta.IfaceInclude
	}
	return opts
}
