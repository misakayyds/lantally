package main

import (
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/misakayyds/lantally/internal/server"
	"github.com/misakayyds/lantally/internal/store/metrics"
	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
	webassets "github.com/misakayyds/lantally/web"
)

func main() {
	listenAddr := flag.String("listen", "0.0.0.0:8080", "HTTP listen address")
	dbPath := flag.String("db", "/var/lib/lantally/meta/lantally.db", "SQLite database path")
	flag.Parse()

	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o750); err != nil {
		log.Fatal(err)
	}
	store, err := sqlitestore.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	if err := server.EnsureFirstRunAdmin(store, os.Stdout); err != nil {
		log.Fatal(err)
	}

	staticFS, err := fs.Sub(webassets.Dist, "dist")
	if err != nil {
		log.Fatal(err)
	}

	cfg := server.Config{StaticFS: staticFS}
	if endpoint := os.Getenv("LANTALLY_METRICS_URL"); endpoint != "" {
		cfg.Metrics = metrics.NewWriter(endpoint, nil)
	}

	httpServer := &http.Server{
		Addr:              *listenAddr,
		Handler:           server.Routes(store, cfg),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("lantally-server listening on %s", *listenAddr)
	log.Fatal(httpServer.ListenAndServe())
}
