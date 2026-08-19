package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/misakayyds/lantally/internal/ingest"
	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
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

	server := &http.Server{
		Addr:              *listenAddr,
		Handler:           ingest.Routes(store),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("lantally-server listening on %s", *listenAddr)
	log.Fatal(server.ListenAndServe())
}
