package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

// VersionInfo describes the response from GET /version.
type VersionInfo struct {
	Version string `json:"version"`
	Meta    bool   `json:"meta,omitempty"`
}

// ProbeResult describes a read-only HTTP probe against a Mihomo controller.
type ProbeResult struct {
	OK          bool   `json:"ok"`
	Version     string `json:"version,omitempty"`
	Meta        bool   `json:"meta,omitempty"`
	ProxyCount  int    `json:"proxy_count"`
	GroupCount  int    `json:"group_count"`
	WSSupported bool   `json:"ws_supported"`
	LatencyMS   int64  `json:"latency_ms"`
	Error       string `json:"error,omitempty"`
}

// ProxyInfo describes a single Mihomo proxy or selector/group entry from GET /proxies.
type ProxyInfo struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Now     string   `json:"now,omitempty"`
	All     []string `json:"all,omitempty"`
	IsGroup bool     `json:"is_group"`
}

// SubscriptionInfo describes Mihomo provider subscription byte/expiry metadata.
type SubscriptionInfo struct {
	Upload   uint64 `json:"upload"`
	Download uint64 `json:"download"`
	Total    uint64 `json:"total"`
	Expire   int64  `json:"expire"`
}

// ProviderSnapshot describes one entry from GET /providers/proxies.
type ProviderSnapshot struct {
	Name             string            `json:"name"`
	VehicleType      string            `json:"vehicle_type"`
	SubscriptionInfo *SubscriptionInfo `json:"subscription_info,omitempty"`
	Proxies          []string          `json:"proxies"`
}

type proxiesResponse struct {
	Proxies map[string]struct {
		Name string   `json:"name"`
		Type string   `json:"type"`
		Now  string   `json:"now"`
		All  []string `json:"all"`
	} `json:"proxies"`
}

type providersResponse struct {
	Providers map[string]struct {
		Name             string `json:"name"`
		VehicleType      string `json:"vehicleType"`
		SubscriptionInfo *struct {
			Upload   uint64 `json:"Upload"`
			Download uint64 `json:"Download"`
			Total    uint64 `json:"Total"`
			Expire   int64  `json:"Expire"`
		} `json:"subscriptionInfo"`
		Proxies []struct {
			Name string `json:"name"`
		} `json:"proxies"`
	} `json:"providers"`
}

// FetchVersion performs a read-only GET /version request.
func (c *Client) FetchVersion(ctx context.Context) (VersionInfo, error) {
	raw, err := c.getPath(ctx, defaultVersionPath, "version")
	if err != nil {
		return VersionInfo{}, err
	}
	var info VersionInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return VersionInfo{}, fmt.Errorf("decode mihomo version: %w", err)
	}
	return info, nil
}

// ProbeHTTP performs a strictly read-only HTTP GET probe (/version + /proxies).
// It intentionally never dials WebSocket /connections so queued mock/stream frames
// are never consumed during a connectivity test.
func (c *Client) ProbeHTTP(ctx context.Context) ProbeResult {
	start := time.Now()
	ver, verErr := c.FetchVersion(ctx)
	if verErr != nil {
		var statusErr *HTTPStatusError
		if errors.As(verErr, &statusErr) {
			if statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden {
				return ProbeResult{
					OK:        false,
					LatencyMS: time.Since(start).Milliseconds(),
					Error:     "unauthorized: " + statusErr.Status,
				}
			}
			if statusErr.StatusCode != http.StatusNotFound {
				return ProbeResult{
					OK:        false,
					LatencyMS: time.Since(start).Milliseconds(),
					Error:     verErr.Error(),
				}
			}
		} else {
			return ProbeResult{
				OK:        false,
				LatencyMS: time.Since(start).Milliseconds(),
				Error:     verErr.Error(),
			}
		}
	}

	proxies, pErr := c.FetchProxies(ctx)
	if pErr != nil {
		errMsg := pErr.Error()
		var statusErr *HTTPStatusError
		if errors.As(pErr, &statusErr) && (statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden) {
			errMsg = "unauthorized: " + statusErr.Status
		}
		return ProbeResult{
			OK:        false,
			LatencyMS: time.Since(start).Milliseconds(),
			Error:     errMsg,
		}
	}

	groupCount := 0
	for _, p := range proxies {
		if p.IsGroup {
			groupCount++
		}
	}
	return ProbeResult{
		OK:          true,
		Version:     ver.Version,
		Meta:        ver.Meta,
		ProxyCount:  len(proxies),
		GroupCount:  groupCount,
		WSSupported: true,
		LatencyMS:   time.Since(start).Milliseconds(),
	}
}

// FetchProxies performs a read-only GET /proxies request.
func (c *Client) FetchProxies(ctx context.Context) ([]ProxyInfo, error) {
	raw, err := c.getPath(ctx, defaultProxiesPath, "proxies")
	if err != nil {
		return nil, err
	}
	var resp proxiesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode mihomo proxies: %w", err)
	}
	out := make([]ProxyInfo, 0, len(resp.Proxies))
	for mapName, entry := range resp.Proxies {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			name = strings.TrimSpace(mapName)
		}
		if name == "" {
			continue
		}
		var all []string
		if len(entry.All) > 0 {
			all = make([]string, 0, len(entry.All))
			for _, member := range entry.All {
				if trimmed := strings.TrimSpace(member); trimmed != "" {
					all = append(all, trimmed)
				}
			}
		}
		out = append(out, ProxyInfo{
			Name:    name,
			Type:    strings.TrimSpace(entry.Type),
			Now:     strings.TrimSpace(entry.Now),
			All:     all,
			IsGroup: len(all) > 0,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// FetchProviders performs a read-only GET /providers/proxies request.
func (c *Client) FetchProviders(ctx context.Context) ([]ProviderSnapshot, error) {
	raw, err := c.getPath(ctx, defaultProvidersProxiesPath, "providers")
	if err != nil {
		return nil, err
	}
	var resp providersResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode mihomo providers: %w", err)
	}
	out := make([]ProviderSnapshot, 0, len(resp.Providers))
	for mapName, entry := range resp.Providers {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			name = strings.TrimSpace(mapName)
		}
		if name == "" {
			continue
		}
		proxies := make([]string, 0, len(entry.Proxies))
		for _, member := range entry.Proxies {
			if memberName := strings.TrimSpace(member.Name); memberName != "" {
				proxies = append(proxies, memberName)
			}
		}
		var sub *SubscriptionInfo
		if entry.SubscriptionInfo != nil {
			sub = &SubscriptionInfo{
				Upload:   entry.SubscriptionInfo.Upload,
				Download: entry.SubscriptionInfo.Download,
				Total:    entry.SubscriptionInfo.Total,
				Expire:   entry.SubscriptionInfo.Expire,
			}
		}
		out = append(out, ProviderSnapshot{
			Name:             name,
			VehicleType:      strings.TrimSpace(entry.VehicleType),
			SubscriptionInfo: sub,
			Proxies:          proxies,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func extractProcess(meta connectionMetadata) string {
	if name := processBasename(meta.Process); name != "" {
		return name
	}
	return processBasename(meta.ProcessPath)
}

func processBasename(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimRight(s, "/\\")
	if s == "" {
		return ""
	}
	if idx := strings.LastIndexAny(s, "/\\"); idx >= 0 {
		s = s[idx+1:]
	}
	return strings.TrimSpace(s)
}

type compiledWatchDomains struct {
	exact    []string
	keywords []string
	regexes  []*regexp.Regexp
}

func compileWatchDomains(patterns []string) *compiledWatchDomains {
	if len(patterns) == 0 {
		return nil
	}
	cw := &compiledWatchDomains{}
	for _, raw := range patterns {
		switch {
		case strings.HasPrefix(raw, "kw:"):
			kw := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(raw, "kw:")))
			if kw != "" {
				cw.keywords = append(cw.keywords, kw)
			}
		case strings.HasPrefix(raw, "re:"):
			pat := strings.TrimPrefix(raw, "re:")
			if strings.TrimSpace(pat) == "" {
				continue
			}
			if re, err := regexp.Compile(pat); err == nil {
				cw.regexes = append(cw.regexes, re)
			}
		default:
			if wNorm := cleanHost(raw); wNorm != "" {
				cw.exact = append(cw.exact, wNorm)
			}
		}
	}
	return cw
}

func extractDomain(meta connectionMetadata, opts Options) (domain string, watched bool) {
	if !opts.CollectDomains && len(opts.WatchDomains) == 0 {
		return "", false
	}

	candidate := cleanHost(meta.Host)
	if candidate == "" {
		candidate = cleanHost(meta.SniffHost)
	}
	if candidate == "" {
		return "", false
	}

	cw := opts.compiledWatch
	if cw == nil && len(opts.WatchDomains) > 0 {
		cw = compileWatchDomains(opts.WatchDomains)
	}

	if cw != nil {
		for _, kw := range cw.keywords {
			if strings.Contains(candidate, kw) {
				return candidate, true
			}
		}
		for _, re := range cw.regexes {
			if re.MatchString(candidate) {
				return candidate, true
			}
		}

		bestWatch := ""
		for _, wNorm := range cw.exact {
			if candidate == wNorm || strings.HasSuffix(candidate, "."+wNorm) {
				if len(wNorm) > len(bestWatch) {
					bestWatch = wNorm
				}
			}
		}
		if bestWatch != "" {
			return bestWatch, true
		}
	}

	if !opts.CollectDomains {
		return "", false
	}
	return etld1OrCandidate(candidate), false
}

func etld1OrCandidate(candidate string) string {
	etld1, err := publicsuffix.EffectiveTLDPlusOne(candidate)
	if err != nil || etld1 == "" {
		return candidate
	}
	return etld1
}

func cleanHost(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.TrimRight(s, ".")
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	s = strings.TrimRight(s, ".")
	if s == "" {
		return ""
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return ""
	}
	if strings.ContainsAny(s, "[]%") {
		return ""
	}
	return s
}

func finalizeDomains(byDomain map[string]*domainAgg, topN int) []DimensionDelta {
	if len(byDomain) == 0 {
		return nil
	}
	if topN <= 0 {
		topN = defaultDomainTopN
	}

	var watched []DimensionDelta
	var regular []DimensionDelta
	for _, agg := range byDomain {
		if agg.watched {
			watched = append(watched, agg.delta)
		} else {
			regular = append(regular, agg.delta)
		}
	}

	sort.Slice(regular, func(i, j int) bool {
		totI := regular[i].RxDelta + regular[i].TxDelta
		totJ := regular[j].RxDelta + regular[j].TxDelta
		if totI != totJ {
			return totI > totJ
		}
		return regular[i].Key < regular[j].Key
	})

	var otherRx, otherTx uint64
	if len(regular) > topN {
		for _, tail := range regular[topN:] {
			otherRx += tail.RxDelta
			otherTx += tail.TxDelta
		}
		regular = regular[:topN]
	}

	out := make([]DimensionDelta, 0, len(watched)+len(regular)+1)
	out = append(out, watched...)
	out = append(out, regular...)
	if otherRx+otherTx > 0 {
		out = append(out, DimensionDelta{
			Type:    "domain",
			Key:     "other",
			RxDelta: otherRx,
			TxDelta: otherTx,
		})
	}
	return out
}
