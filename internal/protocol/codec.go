package protocol

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
)

// Encode serializes a batch as gzip-compressed JSON.
func Encode(b Batch) ([]byte, error) {
	b = normalizeBatchForEncode(b)

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(gw).Encode(b); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decode parses gzip-compressed or raw JSON into a batch and validates it.
func Decode(raw []byte) (Batch, error) {
	var b Batch

	data, err := decodePayload(raw)
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return b, err
	}
	if err := Validate(b); err != nil {
		return b, err
	}
	return b, nil
}

func normalizeBatchForEncode(b Batch) Batch {
	out := b
	if out.Capabilities == nil {
		out.Capabilities = []Capability{}
	}
	if out.Interfaces == nil {
		out.Interfaces = []IfaceDelta{}
	}
	if out.Devices == nil {
		out.Devices = []DeviceDelta{}
	}
	if out.Gaps == nil {
		out.Gaps = []Gap{}
	}
	if out.Proxy != nil && out.Proxy.ByOutbound == nil {
		p := *out.Proxy
		p.ByOutbound = []OutboundDelta{}
		out.Proxy = &p
	}
	return out
}

func decodePayload(raw []byte) ([]byte, error) {
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer gr.Close()
		return io.ReadAll(gr)
	}
	return raw, nil
}
