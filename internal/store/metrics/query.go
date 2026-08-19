package metrics

import "fmt"

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
	return fmt.Sprintf(
		`sum(increase(lantally_bytes_total{site_id=%q,node_id=%q,class="total"}[%q:]))`,
		siteID,
		nodeID,
		cycleStartRFC3339,
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
