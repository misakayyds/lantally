package metrics

import "testing"

func TestValidateLabelsRejectsForbiddenDimensions(t *testing.T) {
	for _, label := range []string{"destination", "domain", "url", "conn_id"} {
		err := ValidateLabels(map[string]string{label: "value"})
		if err == nil {
			t.Fatalf("expected forbidden label %q to fail", label)
		}
	}
}

func TestValidateLabelsAcceptsWhitelist(t *testing.T) {
	err := ValidateLabels(map[string]string{
		"site_id":   "site-a",
		"node_id":   "node-a",
		"device_id": "dev_a",
		"class":     "total",
		"direction": "rx",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFormatPrometheusImport(t *testing.T) {
	body, err := FormatPrometheusImport([]Sample{{
		Name:  "lantally_bytes_total",
		Value: 42,
		Labels: map[string]string{
			"site_id":   "site-a",
			"node_id":   "node-a",
			"class":     "total",
			"direction": "rx",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{
		"lantally_bytes_total{",
		"site_id=\"site-a\"",
		"node_id=\"node-a\"",
		"class=\"total\"",
		"direction=\"rx\"",
		"} 42",
	} {
		if !containsAll(body, part) {
			t.Fatalf("body missing %q: %s", part, body)
		}
	}
}

func containsAll(body, part string) bool {
	return len(body) > 0 && (part == "" || indexOf(body, part) >= 0)
}

func indexOf(body, part string) int {
	for i := 0; i+len(part) <= len(body); i++ {
		if body[i:i+len(part)] == part {
			return i
		}
	}
	return -1
}
