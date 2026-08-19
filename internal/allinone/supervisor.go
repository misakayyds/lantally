package allinone

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

type ProcessState struct {
	Name    string
	Running bool
	Healthy bool
}

type HealthConfig struct {
	VMURL     string
	ServerURL string
}

type Supervisor struct {
	vmBinary   string
	vmArgs     []string
	server     string
	serverArgs []string
	health     HealthConfig
}

func NewSupervisor(vmBinary, serverBinary string) *Supervisor {
	serverArgs := []string{
		"-listen", "0.0.0.0:8080",
		"-db", "/var/lib/lantally/meta/lantally.db",
	}
	if os.Getenv("LANTALLY_DEMO") == "1" {
		serverArgs = append(serverArgs, "-demo")
	}
	return &Supervisor{
		vmBinary: vmBinary,
		vmArgs: []string{
			"-httpListenAddr=127.0.0.1:8428",
			"-storageDataPath=/var/lib/lantally/metrics",
		},
		server:     serverBinary,
		serverArgs: serverArgs,
		health: HealthConfig{
			VMURL:     "http://127.0.0.1:8428/health",
			ServerURL: "http://127.0.0.1:8080/healthz",
		},
	}
}

func (s *Supervisor) Health(ctx context.Context, client *http.Client) ([]ProcessState, bool) {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	states := []ProcessState{
		probe(ctx, client, "victoriametrics", s.health.VMURL),
		probe(ctx, client, "lantally-server", s.health.ServerURL),
	}
	ok := true
	for _, state := range states {
		if !state.Healthy {
			ok = false
		}
	}
	return states, ok
}

func probe(ctx context.Context, client *http.Client, name, url string) ProcessState {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ProcessState{Name: name}
	}
	response, err := client.Do(request)
	if err != nil {
		return ProcessState{Name: name, Running: true}
	}
	defer response.Body.Close()
	return ProcessState{
		Name:    name,
		Running: true,
		Healthy: response.StatusCode >= 200 && response.StatusCode < 300,
	}
}

func (s *Supervisor) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	start := func(name, binary string, args []string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil && !errors.Is(err, context.Canceled) {
				errCh <- fmt.Errorf("%s exited: %w", name, err)
			}
			cancel()
		}()
	}

	start("victoriametrics", s.vmBinary, s.vmArgs)
	time.Sleep(500 * time.Millisecond)
	start("lantally-server", s.server, s.serverArgs)

	select {
	case <-ctx.Done():
		wg.Wait()
		return ctx.Err()
	case err := <-errCh:
		cancel()
		wg.Wait()
		return err
	}
}
