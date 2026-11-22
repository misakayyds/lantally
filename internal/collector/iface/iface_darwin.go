//go:build darwin

package iface

import (
	"errors"
	"syscall"
	"time"

	"github.com/misakayyds/lantally/internal/protocol"
)

func readInterfaces() ([]protocol.IfaceDelta, error) {
	var buf []byte
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		buf, err = syscall.RouteRIB(syscall.NET_RT_IFLIST2, 0)
		if !errors.Is(err, syscall.ENOMEM) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		return nil, err
	}
	return parseDarwinRouteRIB(buf)
}
