package iface

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/misakayyds/lantally/internal/protocol"
)

const (
	darwinRTMVersion     = 5
	darwinRTMIfInfo2     = 0x12
	darwinRTAIfp         = 0x10
	darwinAFLink         = 0x12
	darwinSizeofMsghdr2  = 160
	darwinIbytesOffset   = 96
	darwinObytesOffset   = 104
)

// parseDarwinRouteRIB parses macOS sysctl NET_RT_IFLIST2 routing information messages
// (RTM_IFINFO2 / IfMsghdr2 + IfData64 + RTA_IFP sockaddr_dl).
func parseDarwinRouteRIB(buf []byte) ([]protocol.IfaceDelta, error) {
	var result []protocol.IfaceDelta
	seen := make(map[string]struct{})

	for len(buf) >= 4 {
		msglen := int(binary.LittleEndian.Uint16(buf[0:2]))
		if msglen < 4 || msglen > len(buf) {
			return nil, fmt.Errorf("invalid darwin route message length %d (remaining %d)", msglen, len(buf))
		}
		msg := buf[:msglen]
		buf = buf[msglen:]

		if msg[2] != darwinRTMVersion || msg[3] != darwinRTMIfInfo2 || msglen < darwinSizeofMsghdr2 {
			continue
		}
		addrs := int32(binary.LittleEndian.Uint32(msg[4:8]))
		if addrs&darwinRTAIfp == 0 {
			continue
		}
		rx := binary.LittleEndian.Uint64(msg[darwinIbytesOffset : darwinIbytesOffset+8])
		tx := binary.LittleEndian.Uint64(msg[darwinObytesOffset : darwinObytesOffset+8])

		name := extractDarwinIfpName(addrs, msg[darwinSizeofMsghdr2:])
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, protocol.IfaceDelta{
			Name:    name,
			RxDelta: rx,
			TxDelta: tx,
		})
	}
	return result, nil
}

func extractDarwinIfpName(addrs int32, payload []byte) string {
	pos := 0
	for bit := 0; bit <= 4; bit++ {
		if addrs&(1<<bit) == 0 {
			continue
		}
		if pos >= len(payload) {
			return ""
		}
		sa := payload[pos:]
		if len(sa) == 0 {
			return ""
		}
		if bit == 4 { // RTAX_IFP
			if len(sa) < 8 || sa[1] != darwinAFLink {
				return ""
			}
			nlen := int(sa[5])
			if nlen <= 0 || 8+nlen > len(sa) {
				return ""
			}
			return strings.TrimSpace(strings.TrimRight(string(sa[8:8+nlen]), "\x00"))
		}
		saLen := int(sa[0])
		step := 4
		if saLen > 0 {
			step = (saLen + 3) &^ 3
		}
		pos += step
	}
	return ""
}
