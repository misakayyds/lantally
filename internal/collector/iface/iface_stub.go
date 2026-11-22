//go:build !(linux || windows || darwin)

package iface

import (
	"errors"

	"github.com/misakayyds/lantally/internal/protocol"
)

var errUnsupported = errors.New("interface collector is not supported on this platform")

func readInterfaces() ([]protocol.IfaceDelta, error) {
	return nil, errUnsupported
}
