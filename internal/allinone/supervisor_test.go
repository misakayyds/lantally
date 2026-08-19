package allinone

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthRequiresBothProcesses(t *testing.T) {
	supervisor := NewSupervisor("vm", "server")
	states, ok := supervisor.Health(context.Background(), &http.Client{})
	if ok {
		t.Fatal("expected unhealthy without reachable endpoints")
	}
	if len(states) != 2 || states[0].Healthy || states[1].Healthy {
		t.Fatalf("states = %+v", states)
	}
}

func TestHealthPassesWhenBothEndpointsOK(t *testing.T) {
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer vm.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	supervisor := NewSupervisor("vm", "server")
	supervisor.health = HealthConfig{
		VMURL:     vm.URL,
		ServerURL: server.URL,
	}
	states, ok := supervisor.Health(context.Background(), vm.Client())
	if !ok {
		t.Fatalf("expected healthy endpoints, got %+v", states)
	}
}
