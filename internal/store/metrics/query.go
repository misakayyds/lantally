package metrics

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var promDurationPattern = regexp.MustCompile(`^\d+[smhdwy]$`)

// TodayTotalQuery returns a PromQL expression for today's total bytes.
func TodayTotalQuery(siteID, nodeID string) string {
	return fmt.Sprintf(
		`sum(increase(lantally_bytes_total{site_id=%q,node_id=%q,class="total"}[1d]))`,
		siteID,
		nodeID,
	)
}

// BillingCycleTotalQuery returns a PromQL expression for the current billing cycle.
func BillingCycleTotalQuery(siteID, nodeID, cycleStartRFC3339 string) string {
	return billingCycleTotalQueryAt(siteID, nodeID, cycleStartRFC3339, time.Now().UTC())
}

func billingCycleTotalQueryAt(siteID, nodeID, cycleStartRFC3339 string, now time.Time) string {
	window := "30d"
	trimmed := strings.TrimSpace(cycleStartRFC3339)
	if promDurationPattern.MatchString(trimmed) {
		window = trimmed
	} else if trimmed != "" {
		if start, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
			secs := int64(now.UTC().Sub(start.UTC()).Seconds())
			if secs < 1 {
				secs = 1
			}
			window = fmt.Sprintf("%ds", secs)
		}
	}
	return fmt.Sprintf(
		`sum(increase(lantally_bytes_total{site_id=%q,node_id=%q,class="total"}[%s]))`,
		siteID,
		nodeID,
		window,
	)
}

// DeviceLedgerQuery returns separate ledger totals for one device.
func DeviceLedgerQuery(siteID, deviceID string) map[string]string {
	base := fmt.Sprintf(`{site_id=%q,device_id=%q`, siteID, deviceID)
	return map[string]string{
		"total":           "sum(lantally_bytes_total" + base + `,class="total"})`,
		"direct":          "sum(lantally_bytes_total" + base + `,class="direct"})`,
		"proxy_raw":       "sum(lantally_bytes_total" + base + `,class="proxy_raw"})`,
		"proxy_adjusted":  "sum(lantally_bytes_total" + base + `,class="proxy_adjusted"})`,
		"proxy_unadjusted":"sum(lantally_bytes_total" + base + `,class="proxy_unadjusted"})`,
	}
}
