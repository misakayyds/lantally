package nlbwmon

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/misakayyds/lantally/internal/protocol"
)

const defaultCommand = "/usr/sbin/nlbw"

// DumpFunc returns one nlbwmon JSON dump. Tests inject this; production uses
// CommandDump which only runs a local read-only command.
type DumpFunc func(context.Context) ([]byte, error)

// Collector turns successive nlbwmon dumps into interval device deltas.
type Collector struct {
	dump    DumpFunc
	tracker *Tracker
}

func NewCollector(dump DumpFunc) *Collector {
	if dump == nil {
		dump = CommandDump(defaultCommand, "-c", "json")
	}
	return &Collector{dump: dump, tracker: NewTracker()}
}

func CommandDump(name string, args ...string) DumpFunc {
	return func(ctx context.Context) ([]byte, error) {
		if name == "" {
			name = defaultCommand
		}
		cmd := exec.CommandContext(ctx, name, args...)
		raw, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("nlbwmon dump: %w", err)
		}
		return raw, nil
	}
}

func (c *Collector) Capability() protocol.Capability {
	return protocol.CapNlbwmon
}

func (c *Collector) Collect(ctx context.Context, at time.Time) ([]protocol.DeviceDelta, []protocol.Gap, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	raw, err := c.dump(ctx)
	if err != nil {
		return nil, nil, err
	}
	current, err := ParseDump(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	deltas, gaps := c.tracker.Sample(current, at)
	return deltas, gaps, nil
}
