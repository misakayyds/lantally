package protocol

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
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
