//go:build darwin

package iface

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestDarwinIfMsghdr2LayoutMatchesUnix(t *testing.T) {
	if got := unsafe.Sizeof(unix.IfMsghdr2{}); got != unix.SizeofIfMsghdr2 {
		t.Fatalf("sizeof(IfMsghdr2) = %d, want unix.SizeofIfMsghdr2 (%d)", got, unix.SizeofIfMsghdr2)
	}
	if unix.SizeofIfMsghdr2 != darwinSizeofMsghdr2 {
		t.Fatalf("unix.SizeofIfMsghdr2 = %d, want %d", unix.SizeofIfMsghdr2, darwinSizeofMsghdr2)
	}
	if got := unsafe.Sizeof(unix.IfData64{}); got != unix.SizeofIfData64 {
		t.Fatalf("sizeof(IfData64) = %d, want %d", got, unix.SizeofIfData64)
	}
	var hdr unix.IfMsghdr2
	var data unix.IfData64
	ibytesOff := unsafe.Offsetof(hdr.Data) + unsafe.Offsetof(data.Ibytes)
	obytesOff := unsafe.Offsetof(hdr.Data) + unsafe.Offsetof(data.Obytes)
	if ibytesOff != darwinIbytesOffset {
		t.Fatalf("Ibytes offset = %d, want %d", ibytesOff, darwinIbytesOffset)
	}
	if obytesOff != darwinObytesOffset {
		t.Fatalf("Obytes offset = %d, want %d", obytesOff, darwinObytesOffset)
	}
}
