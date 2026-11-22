package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWriterAndQueries(t *testing.T) {
	var gotPath, gotContentType, gotBody string
	statusToReturn := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.WriteHeader(statusToReturn)
		if statusToReturn >= 400 {
			_, _ = w.Write([]byte("import rejected"))
		}
	}))
	defer srv.Close()

	w := NewWriter(srv.URL+"/", nil)
	samples := []Sample{{
		Name:  "lantally_bytes_total",
		Value: 1024,
		Labels: map[string]string{
			"site_id":   "site-a",
			"node_id":   "node-a",
			"class":     "total",
			"direction": "rx",
		},
	}}
	if err := w.Write(context.Background(), samples); err != nil {
		t.Fatalf("Write 204 failed: %v", err)
	}
	if gotPath != "/api/v1/import/prometheus" || gotContentType != "text/plain" {
		t.Fatalf("unexpected request path=%q ct=%q", gotPath, gotContentType)
	}
	if !strings.Contains(gotBody, `lantally_bytes_total{`) || !strings.Contains(gotBody, `} 1024`) {
		t.Fatalf("unexpected prometheus body: %s", gotBody)
	}

	// Non-2xx returns error with status and body summary.
	statusToReturn = http.StatusBadRequest
	if err := w.Write(context.Background(), samples); err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "import rejected") {
		t.Fatalf("expected 400 error propagation, got %v", err)
	}

	// Forbidden label rejected before HTTP call.
	statusToReturn = http.StatusNoContent
	badSamples := []Sample{{
		Name:   "lantally_bytes_total",
		Value:  1,
		Labels: map[string]string{"domain": "forbidden.example"},
	}}
	if err := w.Write(context.Background(), badSamples); err == nil {
		t.Fatal("expected forbidden label rejection")
	}

	// Query helpers produce valid PromQL without quoted range vectors.
	todayQ := TodayTotalQuery("site-a", "node-a")
	if !strings.Contains(todayQ, `site_id="site-a"`) || !strings.Contains(todayQ, `[1d]`) {
		t.Fatalf("TodayTotalQuery = %q", todayQ)
	}

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	cycleQ := billingCycleTotalQueryAt("site-a", "node-a", now.Add(-24*time.Hour).Format(time.RFC3339), now)
	if !strings.Contains(cycleQ, `[86400s]`) || strings.Contains(cycleQ, `["`) {
		t.Fatalf("BillingCycleTotalQuery invalid PromQL: %q", cycleQ)
	}
	if durQ := BillingCycleTotalQuery("site-a", "node-a", "7d"); !strings.Contains(durQ, `[7d]`) {
		t.Fatalf("BillingCycleTotalQuery duration = %q", durQ)
	}
	if fallbackQ := BillingCycleTotalQuery("site-a", "node-a", ""); !strings.Contains(fallbackQ, `[30d]`) {
		t.Fatalf("BillingCycleTotalQuery fallback = %q", fallbackQ)
	}

	devQ := DeviceLedgerQuery("site-a", "dev_1")
	for _, class := range []string{"total", "direct", "proxy_raw", "proxy_adjusted", "proxy_unadjusted"} {
		if q := devQ[class]; !strings.Contains(q, `device_id="dev_1"`) || !strings.Contains(q, `class="`+class+`"`) {
			t.Fatalf("DeviceLedgerQuery[%q] = %q", class, q)
		}
	}
}
