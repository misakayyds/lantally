package protocol

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validBatch() Batch {
	return Batch{
		ProtocolVersion: 1,
		SiteID:          "site-a",
		NodeID:          "node-a",
		BootID:          "boot-1",
		Sequence:        1,
		SampledAt:       time.Unix(1_700_000_000, 0).UTC(),
		IntervalMS:      15000,
	}
}

func TestValidateRejectsWrongVersion(t *testing.T) {
	b := validBatch()
	b.ProtocolVersion = 2
	err := Validate(b)
	var ver VersionError
	if !errors.As(err, &ver) || ver.Got != 2 || ver.Want != 1 {
		t.Fatalf("expected VersionError got=2 want=1, got %v", err)
	}
	if !strings.Contains(err.Error(), "protocol_version 2") {
		t.Fatalf("error should name the version: %v", err)
	}
}

func TestValidateAcceptsMinimalV1(t *testing.T) {
	err := Validate(Batch{
		ProtocolVersion: 1,
		SiteID:          "site-a",
		NodeID:          "node-a",
		BootID:          "boot-1",
		Sequence:        1,
		SampledAt:       time.Unix(1_700_000_000, 0).UTC(),
		IntervalMS:      15000,
		Capabilities:    []Capability{CapIface},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsEmptyIDs(t *testing.T) {
	err := Validate(Batch{
		ProtocolVersion: 1,
		Sequence:        1,
		SampledAt:       time.Unix(1_700_000_000, 0).UTC(),
		IntervalMS:      15000,
	})
	if !errors.Is(err, errSiteID) {
		t.Fatalf("expected missing site_id error, got %v", err)
	}
}

func TestValidateRejectsEmptyNodeID(t *testing.T) {
	b := validBatch()
	b.NodeID = ""
	err := Validate(b)
	if !errors.Is(err, errNodeID) {
		t.Fatalf("expected node_id error, got %v", err)
	}
}

func TestValidateRejectsEmptyBootID(t *testing.T) {
	b := validBatch()
	b.BootID = ""
	err := Validate(b)
	if !errors.Is(err, errBootID) {
		t.Fatalf("expected boot_id error, got %v", err)
	}
}

func TestValidateRejectsZeroSequence(t *testing.T) {
	b := validBatch()
	b.Sequence = 0
	err := Validate(b)
	if !errors.Is(err, errSequence) {
		t.Fatalf("expected sequence error, got %v", err)
	}
}

func TestValidateRejectsZeroSampledAt(t *testing.T) {
	b := validBatch()
	b.SampledAt = time.Time{}
	err := Validate(b)
	if !errors.Is(err, errSampledAt) {
		t.Fatalf("expected sampled_at error, got %v", err)
	}
}

func TestValidateRejectsNonPositiveIntervalMS(t *testing.T) {
	for _, interval := range []int{0, -1} {
		b := validBatch()
		b.IntervalMS = interval
		err := Validate(b)
		if !errors.Is(err, errIntervalMS) {
			t.Fatalf("interval_ms=%d: expected interval error, got %v", interval, err)
		}
	}
}

func TestValidateRejectsUnknownCapability(t *testing.T) {
	b := validBatch()
	b.Capabilities = []Capability{"not-a-real-cap"}
	err := Validate(b)
	if err == nil {
		t.Fatal("expected unknown capability error")
	}
	if !strings.Contains(err.Error(), "unknown capability") {
		t.Fatalf("expected unknown capability error, got %v", err)
	}
}

func TestValidateRejectsInvalidDimensionsProxyNodesAndProviders(t *testing.T) {
	b := validBatch()
	b.Dimensions = []DimensionDelta{{Type: "unknown", Key: "k", RxDelta: 1, TxDelta: 1}}
	if err := Validate(b); err == nil {
		t.Fatal("expected invalid dimension type error")
	}

	b = validBatch()
	b.Dimensions = []DimensionDelta{{Type: DimGroup, Key: "   ", RxDelta: 1, TxDelta: 1}}
	if err := Validate(b); err == nil {
		t.Fatal("expected empty dimension key error")
	}

	b = validBatch()
	b.Dimensions = []DimensionDelta{{Type: DimIface, Key: "eth0", RxDelta: 0, TxDelta: 0}}
	if err := Validate(b); err != nil {
		t.Fatalf("zero rx/tx dimension should be valid, got %v", err)
	}

	b = validBatch()
	b.ProxyNodes = []string{"hk-01", " "}
	if err := Validate(b); err == nil {
		t.Fatal("expected empty proxy_node error")
	}

	b = validBatch()
	b.Providers = []ProviderSnapshot{{Name: ""}}
	if err := Validate(b); err == nil {
		t.Fatal("expected empty provider name error")
	}

	b = validBatch()
	b.Providers = []ProviderSnapshot{{Name: "sub-alpha", Proxies: []string{"hk-01", ""}}}
	if err := Validate(b); err == nil {
		t.Fatal("expected empty provider proxy name error")
	}
}

func TestValidateEnforcesBatchSchemaConstraints(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	badCases := []struct {
		name  string
		mut   func(*Batch)
	}{
		{
			name: "empty interface name",
			mut:  func(b *Batch) { b.Interfaces = []IfaceDelta{{Name: " ", RxDelta: 1, TxDelta: 1}} },
		},
		{
			name: "empty device obs_ip",
			mut:  func(b *Batch) { b.Devices = []DeviceDelta{{ObsIP: "", Source: SourceMihomo}} },
		},
		{
			name: "invalid device source",
			mut:  func(b *Batch) { b.Devices = []DeviceDelta{{ObsIP: "203.0.113.1", Source: "bogus"}} },
		},
		{
			name: "empty proxy outbound name",
			mut:  func(b *Batch) { b.Proxy = &ProxyDelta{ByOutbound: []OutboundDelta{{Name: ""}}} },
		},
		{
			name: "invalid gap reason",
			mut:  func(b *Batch) { b.Gaps = []Gap{{Reason: "bogus", From: now, To: now}} },
		},
		{
			name: "zero gap from",
			mut:  func(b *Batch) { b.Gaps = []Gap{{Reason: GapReboot, To: now}} },
		},
		{
			name: "zero gap to",
			mut:  func(b *Batch) { b.Gaps = []Gap{{Reason: GapReboot, From: now}} },
		},
		{
			name: "non-object metadata array",
			mut:  func(b *Batch) { b.Metadata = []byte(`[1,2,3]`) },
		},
		{
			name: "null metadata",
			mut:  func(b *Batch) { b.Metadata = []byte(`null`) },
		},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			b := validBatch()
			tc.mut(&b)
			if err := Validate(b); err == nil {
				t.Fatalf("expected Validate to reject %s", tc.name)
			}
		})
	}

	// Valid empty object metadata and valid gaps/devices/outbounds pass.
	okBatch := validBatch()
	okBatch.Interfaces = []IfaceDelta{{Name: "eth0", RxDelta: 10, TxDelta: 5}}
	okBatch.Devices = []DeviceDelta{{ObsIP: "203.0.113.10", Source: SourceNeigh}}
	okBatch.Proxy = &ProxyDelta{ByOutbound: []OutboundDelta{{Name: "DIRECT", DirectRx: 1}}}
	okBatch.Gaps = []Gap{{Reason: GapBufferDrop, From: now.Add(-time.Minute), To: now}}
	okBatch.Metadata = []byte(`{}`)
	if err := Validate(okBatch); err != nil {
		t.Fatalf("expected valid batch to pass, got %v", err)
	}
}
