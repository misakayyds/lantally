package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Writer pushes Prometheus import payloads to VictoriaMetrics.
type Writer struct {
	endpoint string
	client   *http.Client
}

func NewWriter(endpoint string, client *http.Client) *Writer {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Writer{endpoint: strings.TrimRight(endpoint, "/"), client: client}
}

func (w *Writer) Write(ctx context.Context, samples []Sample) error {
	body, err := FormatPrometheusImport(samples)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		w.endpoint+"/api/v1/import/prometheus",
		strings.NewReader(body),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "text/plain")
	response, err := w.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		summary, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("metrics import returned %s: %s", response.Status, strings.TrimSpace(string(summary)))
	}
	return nil
}
