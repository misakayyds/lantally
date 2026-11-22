package protocol

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	in := Batch{
		ProtocolVersion: 1,
		SiteID:          "site-a",
		NodeID:          "node-a",
		BootID:          "boot-1",
		Sequence:        7,
		SampledAt:       time.Unix(1_700_000_000, 0).UTC(),
		IntervalMS:      15000,
		Capabilities:    []Capability{CapIface},
		Interfaces: []IfaceDelta{{
			Name:    "eth0",
			RxDelta: 100,
			TxDelta: 20,
		}},
	}
	raw, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Sequence != 7 || out.Interfaces[0].RxDelta != 100 {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestEncodeUsesEmptyArraysNotNull(t *testing.T) {
	raw, err := Encode(validBatch())
	if err != nil {
		t.Fatal(err)
	}
	body, err := gunzipForTest(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`"capabilities":null`,
		`"interfaces":null`,
		`"devices":null`,
	} {
		if strings.Contains(body, fragment) {
			t.Fatalf("expected empty arrays, found %s in %s", fragment, body)
		}
	}
	for _, fragment := range []string{
		`"capabilities":[]`,
		`"interfaces":[]`,
		`"devices":[]`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("expected %s in encoded batch: %s", fragment, body)
		}
	}
}

func gunzipForTest(raw []byte) (string, error) {
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	defer gr.Close()
	data, err := io.ReadAll(gr)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func TestDecodeAcceptsMetadata(t *testing.T) {
	raw, err := Encode(Batch{
		ProtocolVersion: 1,
		SiteID:          "site-a",
		NodeID:          "node-a",
		BootID:          "boot-1",
		Sequence:        1,
		SampledAt:       time.Unix(1_700_000_000, 0).UTC(),
		IntervalMS:      15000,
		Metadata:        json.RawMessage(`{"collector":"sim","note":"ok"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Metadata) != `{"collector":"sim","note":"ok"}` {
		t.Fatalf("metadata round trip mismatch: %s", out.Metadata)
	}
}

func TestDecodeRejectsUnknownRootField(t *testing.T) {
	body := []byte(`{
		"protocol_version": 1,
		"site_id": "site-a",
		"node_id": "node-a",
		"boot_id": "boot-1",
		"sequence": 1,
		"sampled_at": "2023-11-14T22:13:20Z",
		"interval_ms": 15000,
		"rx_delta": 999
	}`)
	if _, err := Decode(body); err == nil {
		t.Fatal("expected unknown root field rejection")
	}
}

func TestDecodeRejectsTrailingJSON(t *testing.T) {
	body := []byte(`{
		"protocol_version": 1,
		"site_id": "site-a",
		"node_id": "node-a",
		"boot_id": "boot-1",
		"sequence": 1,
		"sampled_at": "2023-11-14T22:13:20Z",
		"interval_ms": 15000,
		"capabilities": [],
		"interfaces": [],
		"devices": []
	} {}`)
	if _, err := Decode(body); err == nil {
		t.Fatal("expected trailing JSON rejection")
	}
}

func TestDecodeRejectsOversizedGunzip(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(bytes.Repeat([]byte("x"), 32)); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := decodePayload(buf.Bytes(), 16); err != ErrDecodeSizeLimit {
		t.Fatalf("expected ErrDecodeSizeLimit, got %v", err)
	}
}

func TestDecodeRejectsWrongVersion(t *testing.T) {
	raw, err := Encode(Batch{
		ProtocolVersion: 2,
		SiteID:          "site-a",
		NodeID:          "node-a",
		BootID:          "boot-1",
		Sequence:        1,
		SampledAt:       time.Unix(1_700_000_000, 0).UTC(),
		IntervalMS:      15000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(raw); err == nil {
		t.Fatal("expected version rejection")
	}
}

func TestEncodeOmitsEmptyOutboundGroupAndPreservesPopulatedGroup(t *testing.T) {
	withoutGroup := validBatch()
	withoutGroup.Proxy = &ProxyDelta{
		ByOutbound: []OutboundDelta{
			{Name: "ss-test", ProxyRx: 100, ProxyTx: 50},
		},
	}
	raw, err := Encode(withoutGroup)
	if err != nil {
		t.Fatal(err)
	}
	body, err := gunzipForTest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, `"group"`) {
		t.Fatalf("expected empty group to be omitted, got %s", body)
	}

	withGroup := validBatch()
	withGroup.Proxy = &ProxyDelta{
		ByOutbound: []OutboundDelta{
			{Name: "ss-test", Group: "PROXY", ProxyRx: 100, ProxyTx: 50},
		},
	}
	raw, err = Encode(withGroup)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Proxy == nil || len(decoded.Proxy.ByOutbound) != 1 || decoded.Proxy.ByOutbound[0].Group != "PROXY" {
		t.Fatalf("group round trip mismatch: %+v", decoded.Proxy)
	}
}

func TestEncodeOmitsEmptyExtensionsAndRoundTripsPopulatedExtensions(t *testing.T) {
	base := validBatch()
	rawBase, err := Encode(base)
	if err != nil {
		t.Fatal(err)
	}
	baseJSON, err := gunzipForTest(rawBase)
	if err != nil {
		t.Fatal(err)
	}
	for _, omitted := range []string{`"dimensions"`, `"proxy_nodes"`, `"providers"`} {
		if strings.Contains(baseJSON, omitted) {
			t.Fatalf("expected %s to be omitted when empty, got %s", omitted, baseJSON)
		}
	}

	ext := validBatch()
	ext.Dimensions = []DimensionDelta{
		{Type: DimGroup, Key: "PROXY", RxDelta: 100, TxDelta: 50},
		{Type: DimProcess, Key: "curl", RxDelta: 0, TxDelta: 0},
	}
	ext.ProxyNodes = []string{"hk-01", "jp-01"}
	ext.Providers = []ProviderSnapshot{{
		Name:        "sub-alpha",
		VehicleType: "HTTP",
		Upload:      10,
		Download:    20,
		Total:       1000,
		Expire:      1800000000,
		Proxies:     []string{"hk-01"},
	}}
	rawExt, err := Encode(ext)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(rawExt)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Dimensions) != 2 || decoded.Dimensions[0].Key != "PROXY" || decoded.Dimensions[1].Type != DimProcess {
		t.Fatalf("dimensions round-trip mismatch: %+v", decoded.Dimensions)
	}
	if len(decoded.ProxyNodes) != 2 || decoded.ProxyNodes[1] != "jp-01" {
		t.Fatalf("proxy_nodes round-trip mismatch: %+v", decoded.ProxyNodes)
	}
	if len(decoded.Providers) != 1 || decoded.Providers[0].Name != "sub-alpha" || len(decoded.Providers[0].Proxies) != 1 {
		t.Fatalf("providers round-trip mismatch: %+v", decoded.Providers)
	}

	// Unknown nested field must be rejected by DisallowUnknownFields.
	badNested := []byte(`{
		"protocol_version": 1,
		"site_id": "site-a",
		"node_id": "node-a",
		"boot_id": "boot-1",
		"sequence": 1,
		"sampled_at": "2023-11-14T22:13:20Z",
		"interval_ms": 15000,
		"dimensions": [{"type":"group","key":"PROXY","rx_delta":1,"tx_delta":2,"extra":9}]
	}`)
	if _, err := Decode(badNested); err == nil {
		t.Fatal("expected unknown field in dimension_delta to be rejected")
	}
}

func TestVersionErrorUnwrapAndDecodeBoundaries(t *testing.T) {
	verErr := VersionError{Got: 2, Want: 1}
	if !errors.Is(verErr, ErrUnsupportedVersion) {
		t.Fatalf("errors.Is(VersionError, ErrUnsupportedVersion) = false")
	}

	// Corrupt gzip payload fails decode.
	if _, err := Decode([]byte{0x1f, 0x8b, 0x08, 0x00, 0xff, 0xff}); err == nil {
		t.Fatal("expected corrupt gzip error")
	}

	// Raw JSON > MaxDecodeSize returns ErrDecodeSizeLimit.
	oversized := bytes.Repeat([]byte(" "), MaxDecodeSize+1)
	if _, err := Decode(oversized); !errors.Is(err, ErrDecodeSizeLimit) {
		t.Fatalf("oversized raw JSON err = %v, want ErrDecodeSizeLimit", err)
	}

	// Proxy != nil with ByOutbound == nil encodes to non-null empty array [].
	b := validBatch()
	b.Proxy = &ProxyDelta{}
	raw, err := Encode(b)
	if err != nil {
		t.Fatal(err)
	}
	jsonStr, err := gunzipForTest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonStr, `"by_outbound":[]`) {
		t.Fatalf("expected non-null empty by_outbound:[], got %s", jsonStr)
	}
}
