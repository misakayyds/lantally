package metrics

import (
	"fmt"
	"strings"
)

var allowedLabels = map[string]bool{
	"site_id":   true,
	"node_id":   true,
	"device_id": true,
	"class":     true,
	"direction": true,
}

var forbiddenLabels = map[string]bool{
	"destination": true,
	"domain":      true,
	"url":         true,
	"conn_id":     true,
	"source_ip":   true,
	"sourceip":    true,
}

// ValidateLabels rejects privacy-sensitive or unknown metric labels.
func ValidateLabels(labels map[string]string) error {
	for name := range labels {
		lower := strings.ToLower(strings.TrimSpace(name))
		if forbiddenLabels[lower] {
			return fmt.Errorf("forbidden metric label %q", name)
		}
		if !allowedLabels[lower] {
			return fmt.Errorf("unknown metric label %q", name)
		}
	}
	return nil
}

// Sample represents one counter sample for VictoriaMetrics import.
type Sample struct {
	Name   string
	Value  float64
	Labels map[string]string
}

// FormatPrometheusImport renders samples for VictoriaMetrics /api/v1/import/prometheus.
func FormatPrometheusImport(samples []Sample) (string, error) {
	var lines []string
	for _, sample := range samples {
		if err := ValidateLabels(sample.Labels); err != nil {
			return "", err
		}
		labelParts := make([]string, 0, len(sample.Labels))
		for key, value := range sample.Labels {
			labelParts = append(labelParts, fmt.Sprintf("%s=%q", key, value))
		}
		metric := sample.Name
		if len(labelParts) > 0 {
			metric += "{" + strings.Join(labelParts, ",") + "}"
		}
		lines = append(lines, fmt.Sprintf("%s %g", metric, sample.Value))
	}
	return strings.Join(lines, "\n") + "\n", nil
}
