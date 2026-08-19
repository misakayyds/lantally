package ingest

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/misakayyds/lantally/internal/protocol"
	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
)

type Handler struct {
	store *sqlitestore.Store
}

func NewHandler(store *sqlitestore.Store) *Handler {
	return &Handler{store: store}
}

func Routes(store *sqlitestore.Store) http.Handler {
	handler := NewHandler(store)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ingest", handler.Ingest)
	mux.HandleFunc("GET /healthz", handler.Healthz)
	return mux
}

func (h *Handler) Ingest(w http.ResponseWriter, r *http.Request) {
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

	contentEncoding := r.Header.Get("Content-Encoding")
	if contentEncoding != "" && !strings.EqualFold(contentEncoding, "gzip") {
		http.Error(w, "unsupported content encoding", http.StatusBadRequest)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, protocol.MaxDecodeSize+1))
	if err != nil || len(raw) > protocol.MaxDecodeSize {
		http.Error(w, "invalid batch", http.StatusBadRequest)
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

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	return token, token != "" && !strings.ContainsAny(token, " \t\r\n")
}
