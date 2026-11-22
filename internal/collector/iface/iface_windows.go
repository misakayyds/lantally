//go:build windows

package iface

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/misakayyds/lantally/internal/protocol"
)

func readInterfaces() ([]protocol.IfaceDelta, error) {
	var table *windows.MibIfTable2
	if err := windows.GetIfTable2Ex(windows.MibIfTableNormal, &table); err != nil {
		return nil, err
	}
	if table == nil {
		return nil, nil
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))

	if table.NumEntries == 0 {
		return nil, nil
	}
	rows := unsafe.Slice(&table.Table[0], int(table.NumEntries))
	result := make([]protocol.IfaceDelta, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))

	for i := range rows {
		row := &rows[i]
		if row.OperStatus == windows.IfOperStatusNotPresent {
			continue
		}
		name := strings.TrimSpace(windows.UTF16ToString(row.Alias[:]))
		if name == "" {
			name = strings.TrimSpace(windows.UTF16ToString(row.Description[:]))
		}
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, protocol.IfaceDelta{
			Name:    name,
			RxDelta: row.InOctets,
			TxDelta: row.OutOctets,
		})
	}
	return result, nil
}
