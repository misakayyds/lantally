//go:build linux

package iface

import (
	"os"

	"github.com/misakayyds/lantally/internal/protocol"
)

func readProcNetDev() ([]protocol.IfaceDelta, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseProcNetDev(f)
}
