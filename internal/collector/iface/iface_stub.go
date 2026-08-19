//go:build !linux

package iface

import (
	"errors"

	"github.com/misakayyds/lantally/internal/protocol"
)

var errUnsupported = errors.New("interface collector is only available on Linux")

func readProcNetDev() ([]protocol.IfaceDelta, error) {
	return nil, errUnsupported
}
