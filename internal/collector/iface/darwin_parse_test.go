package iface

import (
	"encoding/binary"
	"testing"

	"github.com/misakayyds/lantally/internal/protocol"
)

func buildSyntheticIfInfo2Msg(name string, rx, tx uint64, extraAddrs int32, extraPayload []byte) []byte {
	sdlLen := 8 + len(name)
	alignedSDL := (sdlLen + 3) &^ 3
	msglen := darwinSizeofMsghdr2 + len(extraPayload) + alignedSDL
	msg := make([]byte, msglen)
	binary.LittleEndian.PutUint16(msg[0:2], uint16(msglen))
	msg[2] = darwinRTMVersion
	msg[3] = darwinRTMIfInfo2
	binary.LittleEndian.PutUint32(msg[4:8], uint32(extraAddrs|darwinRTAIfp))
	binary.LittleEndian.PutUint64(msg[darwinIbytesOffset:darwinIbytesOffset+8], rx)
	binary.LittleEndian.PutUint64(msg[darwinObytesOffset:darwinObytesOffset+8], tx)

	pos := darwinSizeofMsghdr2
	if len(extraPayload) > 0 {
		copy(msg[pos:], extraPayload)
		pos += len(extraPayload)
	}
	sdl := msg[pos:]
	sdl[0] = byte(sdlLen)
	sdl[1] = darwinAFLink
	sdl[5] = byte(len(name))
	copy(sdl[8:], name)
	return msg
}

func TestParseDarwinRouteRIB(t *testing.T) {
	// Include a non-RTM_IFINFO2 message (type 0x0c) that should be skipped.
	other := make([]byte, 16)
	binary.LittleEndian.PutUint16(other[0:2], 16)
	other[2] = darwinRTMVersion
	other[3] = 0x0c

	// Preceding RTA_DST (bit 0) sockaddr (16 bytes, aligned to 16) before RTA_IFP.
	extraSA := make([]byte, 16)
	extraSA[0] = 16

	var rib []byte
	rib = append(rib, other...)
	rib = append(rib, buildSyntheticIfInfo2Msg("en0", 1234567890123, 9876543210, 0x01, extraSA)...)
	rib = append(rib, buildSyntheticIfInfo2Msg("lo0", 500, 500, 0, nil)...)
	rib = append(rib, buildSyntheticIfInfo2Msg("utun4", 1024, 2048, 0, nil)...)
	rib = append(rib, buildSyntheticIfInfo2Msg("en1234567890123", 42, 84, 0, nil)...)

	got, err := parseDarwinRouteRIB(rib)
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.IfaceDelta{
		{Name: "en0", RxDelta: 1234567890123, TxDelta: 9876543210},
		{Name: "lo0", RxDelta: 500, TxDelta: 500},
		{Name: "utun4", RxDelta: 1024, TxDelta: 2048},
		{Name: "en1234567890123", RxDelta: 42, TxDelta: 84},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d interfaces, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("interface %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseDarwinRouteRIBRejectsInvalidLength(t *testing.T) {
	bad := []byte{0x00, 0x00, darwinRTMVersion, darwinRTMIfInfo2}
	if _, err := parseDarwinRouteRIB(bad); err == nil {
		t.Fatal("expected error for zero msglen")
	}
	truncated := []byte{0x20, 0x00, darwinRTMVersion, darwinRTMIfInfo2}
	if _, err := parseDarwinRouteRIB(truncated); err == nil {
		t.Fatal("expected error for truncated msglen")
	}
}

func TestExtractDarwinIfpNameEdgeCases(t *testing.T) {
	// saLen == 0 preceding sockaddr advances by 4 bytes to RTA_IFP.
	payloadZeroSALen := make([]byte, 4+8+3)
	payloadZeroSALen[4+0] = 11
	payloadZeroSALen[4+1] = darwinAFLink
	payloadZeroSALen[4+5] = 3
	copy(payloadZeroSALen[4+8:], "en0")
	if got := extractDarwinIfpName(0x01|darwinRTAIfp, payloadZeroSALen); got != "en0" {
		t.Fatalf("saLen==0 advance got %q, want en0", got)
	}

	// Non-AF_LINK sockaddr returns empty string.
	nonAFLink := make([]byte, 12)
	nonAFLink[0] = 12
	nonAFLink[1] = 0x02 // AF_INET instead of darwinAFLink
	nonAFLink[5] = 3
	copy(nonAFLink[8:], "en0")
	if got := extractDarwinIfpName(darwinRTAIfp, nonAFLink); got != "" {
		t.Fatalf("non-AF_LINK got %q, want empty", got)
	}

	// nlen == 0 returns empty string.
	zeroNLen := make([]byte, 12)
	zeroNLen[0] = 12
	zeroNLen[1] = darwinAFLink
	zeroNLen[5] = 0
	if got := extractDarwinIfpName(darwinRTAIfp, zeroNLen); got != "" {
		t.Fatalf("nlen==0 got %q, want empty", got)
	}
}
