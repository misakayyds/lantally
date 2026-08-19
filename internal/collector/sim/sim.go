package sim

import (
	"time"

	"github.com/misakayyds/lantally/internal/protocol"
)

const (
	simSiteID    = "sim-site"
	simNodeID    = "sim-node"
	simIfaceName = "sim0"
	simDeviceIP  = "203.0.113.1"
	simDeviceMAC = "02:00:00:00:00:01"
	simInterval  = 15000
)

// Snapshot returns a synthetic ingest batch for tests and local demos.
func Snapshot(seq uint64, bootID string) protocol.Batch {
	return protocol.Batch{
		ProtocolVersion: 1,
		SiteID:          simSiteID,
		NodeID:          simNodeID,
		BootID:          bootID,
		Sequence:        seq,
		SampledAt:       time.Now().UTC(),
		IntervalMS:      simInterval,
		Capabilities:    []protocol.Capability{protocol.CapIface},
		Interfaces: []protocol.IfaceDelta{
			{Name: simIfaceName, RxDelta: 1024, TxDelta: 512},
		},
		Devices: []protocol.DeviceDelta{
			{
				ObsIP:   simDeviceIP,
				ObsMAC:  simDeviceMAC,
				RxDelta: 2048,
				TxDelta: 1024,
				Source:  protocol.SourceNeigh,
			},
		},
	}
}
