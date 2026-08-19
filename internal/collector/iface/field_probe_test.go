package iface_test

import (
	"os"
	"testing"

	"github.com/misakayyds/lantally/internal/collector/iface"
)

func TestParseProcNetDevFromFieldSample(t *testing.T) {
	path := os.Getenv("LANTALLY_FIELD_PROC_NET_DEV")
	if path == "" {
		t.Skip("set LANTALLY_FIELD_PROC_NET_DEV for optional field probe")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := iface.ParseProcNetDev(file); err != nil {
		t.Fatal(err)
	}
}
