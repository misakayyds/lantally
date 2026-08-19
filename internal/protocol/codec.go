package protocol

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
)

// MaxDecodeSize is the maximum decompressed batch payload size accepted by Decode.
const MaxDecodeSize = 4 << 20 // 4 MiB

// ErrDecodeSizeLimit is returned when a gzip payload exceeds MaxDecodeSize after decompression.
var ErrDecodeSizeLimit = errors.New("decode payload exceeds maximum size")

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

	data, err := decodePayload(raw, MaxDecodeSize)
	if err != nil {
		return b, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
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

func decodePayload(raw []byte, maxSize int64) ([]byte, error) {
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer gr.Close()
		return readLimited(gr, maxSize)
	}
	if int64(len(raw)) > maxSize {
		return nil, ErrDecodeSizeLimit
	}
	return raw, nil
}

func readLimited(r io.Reader, maxSize int64) ([]byte, error) {
	limited := io.LimitReader(r, maxSize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxSize {
		return nil, ErrDecodeSizeLimit
	}
	return data, nil
}
