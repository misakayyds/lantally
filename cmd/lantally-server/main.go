package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/misakayyds/lantally/internal/collector/direct"
	"github.com/misakayyds/lantally/internal/ingest"
	"github.com/misakayyds/lantally/internal/server"
	"github.com/misakayyds/lantally/internal/store/metrics"
	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
	"github.com/misakayyds/lantally/internal/version"
	webassets "github.com/misakayyds/lantally/web"
)

func main() {
	listenAddr := flag.String("listen", "0.0.0.0:8080", "HTTP listen address")
	dbPath := flag.String("db", "/var/lib/lantally/meta/lantally.db", "SQLite database path")
	showVersion := flag.Bool("version", false, "print version and exit")
	demo := flag.Bool("demo", false, "seed synthetic traffic for local preview")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.String())
		return
	}

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
	if *demo || os.Getenv("LANTALLY_DEMO") == "1" {
		if err := ingest.SeedSynthetic(context.Background(), store, time.Now().UTC()); err != nil {
			log.Fatal(err)
		}
		log.Print("seeded synthetic demo traffic")
	}

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	var bgWG sync.WaitGroup
	bgWG.Add(2)
	go func() {
		defer bgWG.Done()
		run := func() {
			if signalCtx.Err() != nil {
				return
			}
			now := time.Now().UTC()
			if err := store.MaintainLedgers(signalCtx, now); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("ledger maintenance: %v", err)
			}
		}
		run()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-signalCtx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()

	go func() {
		defer bgWG.Done()
		run := func() {
			if signalCtx.Err() != nil {
				return
			}
			if err := store.EvaluateAlerts(signalCtx, time.Now().UTC()); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("alert evaluation: %v", err)
			}
		}
		run()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-signalCtx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()

	staticFS, err := fs.Sub(webassets.Dist, "dist")
	if err != nil {
		log.Fatal(err)
	}

	agentDir := strings.TrimSpace(os.Getenv("LANTALLY_AGENT_DIR"))
	if agentDir == "" {
		agentDir = "/usr/share/lantally/agents"
	}
	if _, err := os.Stat(agentDir); err != nil {
		agentDir = ""
	}
	cfg := server.Config{StaticFS: staticFS, AgentDir: agentDir}
	if endpoint := os.Getenv("LANTALLY_METRICS_URL"); endpoint != "" {
		cfg.Metrics = metrics.NewWriter(endpoint, nil)
	}

	directMgr := direct.NewManager(store, direct.Config{Metrics: cfg.Metrics})
	if err := directMgr.Start(signalCtx); err != nil {
		log.Fatal(err)
	}
	defer directMgr.Close()
	cfg.Direct = directMgr

	httpServer := &http.Server{
		Addr:              *listenAddr,
		Handler:           server.Routes(store, cfg),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("lantally-server listening on %s", *listenAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		stopSignals()
		bgWG.Wait()
		if err != nil {
			log.Fatal(err)
		}
	case <-signalCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		_ = directMgr.Shutdown(shutdownCtx)
		bgWG.Wait()
	}
}
