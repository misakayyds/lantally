package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/misakayyds/lantally/internal/allinone"
)

func main() {
	vmBinary := flag.String("vm", "/usr/local/bin/victoria-metrics", "VictoriaMetrics binary path")
	serverBinary := flag.String("server", "/usr/local/bin/lantally-server", "lantally-server binary path")
	flag.Parse()

	supervisor := allinone.NewSupervisor(*vmBinary, *serverBinary)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Fatal(supervisor.Run(ctx))
}
