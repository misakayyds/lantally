//go:build e2e

package e2e

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var (
	serverBinPath string
	agentBinPath  string
)

func TestMain(m *testing.M) {
	binDir, err := os.MkdirTemp("", "lantally-e2e-bins-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp bin dir: %v\n", err)
		os.Exit(1)
	}

	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	serverBinPath = filepath.Join(binDir, "lantally-server"+ext)
	agentBinPath = filepath.Join(binDir, "lantally-agent"+ext)

	if err := buildBinary("github.com/misakayyds/lantally/cmd/lantally-server", serverBinPath); err != nil {
		fmt.Fprintf(os.Stderr, "build lantally-server: %v\n", err)
		_ = os.RemoveAll(binDir)
		os.Exit(1)
	}
	if err := buildBinary("github.com/misakayyds/lantally/cmd/lantally-agent", agentBinPath); err != nil {
		fmt.Fprintf(os.Stderr, "build lantally-agent: %v\n", err)
		_ = os.RemoveAll(binDir)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(binDir)
	os.Exit(code)
}

func buildBinary(pkg, outPath string) error {
	cmd := exec.Command("go", "build", "-o", outPath, pkg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func newServerCmd(listenAddr, dbPath string) *exec.Cmd {
	return exec.Command(serverBinPath, "-listen", listenAddr, "-db", dbPath)
}

func newCrossBuildCmd(repoDir, outPath, goos, goarch, pkg string) *exec.Cmd {
	cmd := exec.Command("go", "build", "-o", outPath, pkg)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	return cmd
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod above %s", wd)
		}
		dir = parent
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type ServerOpts struct {
	ListenAddr        string
	DBPath            string
	BootstrapPassword string
	MetricsURL        string
	AgentDir          string
	Demo              bool
	ExtraEnv          []string
}

type RunningServer struct {
	BaseURL string
	DBPath  string
	Cmd     *exec.Cmd

	logs     *lockedBuffer
	done     chan error
	stopOnce sync.Once
	waitErr  error
}

func (s *RunningServer) Stop() {
	if s == nil || s.Cmd == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.Cmd.Process != nil {
			_ = s.Cmd.Process.Kill()
		}
		if s.done != nil {
			s.waitErr = <-s.done
		}
	})
}

func setSQLiteUserVersion(t *testing.T, dbPath string, target uint32) uint32 {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite db %s: %v", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	var prev uint32
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&prev); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, target)); err != nil {
		t.Fatalf("set user_version=%d: %v", target, err)
	}
	_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite db %s: %v", dbPath, err)
	}
	return prev
}

func (s *RunningServer) Logs() string {
	if s == nil || s.logs == nil {
		return ""
	}
	return s.logs.String()
}

func startServer(t *testing.T, opts ServerOpts) *RunningServer {
	t.Helper()

	tempDir := t.TempDir()
	dbPath := opts.DBPath
	if dbPath == "" {
		dbPath = filepath.Join(tempDir, "lantally.db")
	}

	attempts := 1
	if opts.ListenAddr == "" {
		attempts = 3
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		addr := opts.ListenAddr
		if addr == "" {
			addr = freeAddr(t)
		}
		srv, err := launchServerOnce(t, addr, dbPath, opts)
		if err == nil {
			return srv
		}
		lastErr = err
	}
	t.Fatalf("startServer failed after %d attempt(s): %v", attempts, lastErr)
	return nil
}

func launchServerOnce(t *testing.T, addr, dbPath string, opts ServerOpts) (*RunningServer, error) {
	t.Helper()

	args := []string{"-listen", addr, "-db", dbPath}
	if opts.Demo {
		args = append(args, "-demo")
	}

	cmd := exec.Command(serverBinPath, args...)
	env := append([]string{}, os.Environ()...)
	if opts.BootstrapPassword != "" {
		env = append(env, "LANTALLY_BOOTSTRAP_ADMIN_PASSWORD="+opts.BootstrapPassword)
	}
	if opts.MetricsURL != "" {
		env = append(env, "LANTALLY_METRICS_URL="+opts.MetricsURL)
	}
	if opts.AgentDir != "" {
		env = append(env, "LANTALLY_AGENT_DIR="+opts.AgentDir)
	}
	env = append(env, opts.ExtraEnv...)
	cmd.Env = env

	logs := &lockedBuffer{}
	cmd.Stdout = logs
	cmd.Stderr = logs

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start server process: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	srv := &RunningServer{
		BaseURL: "http://" + addr,
		DBPath:  dbPath,
		Cmd:     cmd,
		logs:    logs,
		done:    done,
	}

	deadline := time.Now().Add(10 * time.Second)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			srv.waitErr = err
			srv.stopOnce.Do(func() {})
			return nil, fmt.Errorf("server exited early (%v), logs:\n%s", err, logs.String())
		default:
		}

		resp, err := client.Get(srv.BaseURL + "/healthz")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && string(body) == "ok\n" {
				t.Cleanup(srv.Stop)
				return srv, nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	srv.Stop()
	return nil, fmt.Errorf("timed out waiting for /healthz on %s; logs:\n%s", srv.BaseURL, logs.String())
}

type AgentCollectorsConfig struct {
	Iface   bool `json:"iface"`
	Mihomo  bool `json:"mihomo"`
	Nlbwmon bool `json:"nlbwmon"`
}

type AgentMihomoConfig struct {
	URL        string `json:"url,omitempty"`
	SecretFile string `json:"secret_file,omitempty"`
}

type AgentNlbwmonConfig struct {
	Command string `json:"command,omitempty"`
}

type AgentIfaceConfig struct {
	ExcludePrefixes []string `json:"exclude_prefixes,omitempty"`
	Include         []string `json:"include,omitempty"`
}

type AgentConfig struct {
	ServerURL  string                `json:"server_url"`
	SiteID     string                `json:"site_id"`
	NodeID     string                `json:"node_id"`
	TokenFile  string                `json:"token_file"`
	Interval   string                `json:"interval,omitempty"`
	Collectors AgentCollectorsConfig `json:"collectors"`
	Mihomo     *AgentMihomoConfig    `json:"mihomo,omitempty"`
	Nlbwmon    *AgentNlbwmonConfig   `json:"nlbwmon,omitempty"`
	Iface      *AgentIfaceConfig     `json:"iface,omitempty"`
}

type RunningAgent struct {
	ConfigPath string
	TokenPath  string
	SecretPath string
	Cmd        *exec.Cmd

	logs     *lockedBuffer
	done     chan error
	stopOnce sync.Once
	waitErr  error
}

func (a *RunningAgent) Stop() {
	if a == nil || a.Cmd == nil {
		return
	}
	a.stopOnce.Do(func() {
		if a.Cmd.Process != nil {
			_ = a.Cmd.Process.Kill()
		}
		if a.done != nil {
			a.waitErr = <-a.done
		}
	})
}

func (a *RunningAgent) Logs() string {
	if a == nil || a.logs == nil {
		return ""
	}
	return a.logs.String()
}

func (a *RunningAgent) ConfigSHA256(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(a.ConfigPath)
	if err != nil {
		t.Fatalf("read agent config %s: %v", a.ConfigPath, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func startAgent(t *testing.T, cfg AgentConfig, token, mihomoSecret string) *RunningAgent {
	t.Helper()

	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	cfg.TokenFile = tokenPath

	var secretPath string
	if mihomoSecret != "" && cfg.Mihomo != nil {
		secretPath = filepath.Join(dir, "mihomo-secret")
		if err := os.WriteFile(secretPath, []byte(mihomoSecret), 0o600); err != nil {
			t.Fatalf("write mihomo secret file: %v", err)
		}
		cfg.Mihomo.SecretFile = secretPath
	}

	cfgBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal agent config: %v", err)
	}
	cfgPath := filepath.Join(dir, "agent.json")
	if err := os.WriteFile(cfgPath, cfgBytes, 0o600); err != nil {
		t.Fatalf("write agent config: %v", err)
	}

	cmd := exec.Command(agentBinPath, "-config", cfgPath)
	logs := &lockedBuffer{}
	cmd.Stdout = logs
	cmd.Stderr = logs

	if err := cmd.Start(); err != nil {
		t.Fatalf("start agent: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	agent := &RunningAgent{
		ConfigPath: cfgPath,
		TokenPath:  tokenPath,
		SecretPath: secretPath,
		Cmd:        cmd,
		logs:       logs,
		done:       done,
	}
	t.Cleanup(agent.Stop)

	// Fail fast if agent immediately rejects config or crashes on startup.
	select {
	case err := <-done:
		agent.waitErr = err
		agent.stopOnce.Do(func() {})
		t.Fatalf("agent exited immediately (%v), logs:\n%s", err, logs.String())
	case <-time.After(100 * time.Millisecond):
	}

	return agent
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(raw)
}

func decodeMap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode json map: %v\nbody: %s", err, string(body))
	}
	return out
}

func newCookieClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	return &http.Client{
		Jar:     jar,
		Timeout: 10 * time.Second,
	}
}

func doJSONRequest(t *testing.T, client *http.Client, method, rawURL string, body any) (int, []byte) {
	t.Helper()
	return doJSONRequestWithHeaders(t, client, method, rawURL, body, nil)
}

func doJSONRequestWithHeaders(t *testing.T, client *http.Client, method, rawURL string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, rawURL, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, rawURL, err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response %s %s: %v", method, rawURL, err)
	}
	return resp.StatusCode, respBytes
}

func doRawRequest(t *testing.T, client *http.Client, method, rawURL string, rawBody []byte, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	var reader io.Reader
	if rawBody != nil {
		reader = bytes.NewReader(rawBody)
	}
	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		t.Fatalf("new raw request %s %s: %v", method, rawURL, err)
	}
	for k, v := range headers {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, rawURL, err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response %s %s: %v", method, rawURL, err)
	}
	return resp.StatusCode, resp.Header, respBytes
}

func setupAdmin(t *testing.T, client *http.Client, baseURL, password string) {
	t.Helper()
	status, body := doJSONRequest(t, client, http.MethodPost, baseURL+"/v1/setup", map[string]string{"password": password})
	if status != http.StatusOK {
		t.Fatalf("POST /v1/setup status = %d, want 200; body=%s", status, string(body))
	}
}

func loginAdmin(t *testing.T, client *http.Client, baseURL, password string) (int, []byte) {
	t.Helper()
	return doJSONRequest(t, client, http.MethodPost, baseURL+"/v1/login", map[string]string{"password": password})
}

func enrollNode(t *testing.T, client *http.Client, baseURL, siteID, nodeID string) string {
	t.Helper()
	status, body := doJSONRequest(t, client, http.MethodPost, baseURL+"/v1/enroll", map[string]string{
		"site_id": siteID,
		"node_id": nodeID,
	})
	if status != http.StatusOK {
		t.Fatalf("POST /v1/enroll status = %d, want 200; body=%s", status, string(body))
	}
	m := decodeMap(t, body)
	tok := asString(m["token"])
	if tok == "" {
		t.Fatalf("enroll response missing token: %s", string(body))
	}
	return tok
}

func createClaim(t *testing.T, client *http.Client, baseURL, siteID, nodeID string, local bool) map[string]any {
	t.Helper()
	status, body := doJSONRequest(t, client, http.MethodPost, baseURL+"/v1/claims", map[string]any{
		"site_id": siteID,
		"node_id": nodeID,
		"local":   local,
	})
	if status != http.StatusOK {
		t.Fatalf("POST /v1/claims status = %d, want 200; body=%s", status, string(body))
	}
	return decodeMap(t, body)
}

func redeemClaim(t *testing.T, baseURL, code string) (int, map[string]any) {
	t.Helper()
	status, body := doJSONRequest(t, nil, http.MethodPost, baseURL+"/v1/claim", map[string]string{
		"code": code,
	})
	if len(body) == 0 || body[0] != '{' {
		return status, map[string]any{"raw": string(body)}
	}
	return status, decodeMap(t, body)
}

type BatchOption func(map[string]any)

func syntheticBatch(siteID, nodeID, bootID string, seq uint64, sampledAt time.Time, opts ...BatchOption) map[string]any {
	b := map[string]any{
		"protocol_version": 1,
		"site_id":          siteID,
		"node_id":          nodeID,
		"boot_id":          bootID,
		"sequence":         seq,
		"sampled_at":       sampledAt.UTC().Format(time.RFC3339Nano),
		"interval_ms":      15000,
		"capabilities":     []string{},
		"interfaces":       []map[string]any{},
		"devices":          []map[string]any{},
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

func withCapabilities(caps ...string) BatchOption {
	return func(b map[string]any) {
		if caps == nil {
			caps = []string{}
		}
		b["capabilities"] = caps
	}
}

func withIntervalMS(ms int) BatchOption {
	return func(b map[string]any) {
		b["interval_ms"] = ms
	}
}

func withInterfaces(ifaces ...map[string]any) BatchOption {
	return func(b map[string]any) {
		if ifaces == nil {
			ifaces = []map[string]any{}
		}
		b["interfaces"] = ifaces
	}
}

func withDevices(devs ...map[string]any) BatchOption {
	return func(b map[string]any) {
		if devs == nil {
			devs = []map[string]any{}
		}
		b["devices"] = devs
	}
}

func withProxyOutbounds(outbounds ...map[string]any) BatchOption {
	return func(b map[string]any) {
		if outbounds == nil {
			outbounds = []map[string]any{}
		}
		b["proxy"] = map[string]any{
			"by_outbound": outbounds,
		}
	}
}

func withGaps(gaps ...map[string]any) BatchOption {
	return func(b map[string]any) {
		if gaps == nil {
			gaps = []map[string]any{}
		}
		b["gaps"] = gaps
	}
}

func withDimensions(dims ...map[string]any) BatchOption {
	return func(b map[string]any) {
		if dims == nil {
			dims = []map[string]any{}
		}
		b["dimensions"] = dims
	}
}

func withProxyNodes(nodes ...string) BatchOption {
	return func(b map[string]any) {
		if nodes == nil {
			nodes = []string{}
		}
		b["proxy_nodes"] = nodes
	}
}

func withProviders(providers ...map[string]any) BatchOption {
	return func(b map[string]any) {
		if providers == nil {
			providers = []map[string]any{}
		}
		b["providers"] = providers
	}
}

func withMetadata(meta map[string]any) BatchOption {
	return func(b map[string]any) {
		b["metadata"] = meta
	}
}

func gzipBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func gzipJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}
	return gzipBytes(t, raw)
}

func postIngestBatch(t *testing.T, baseURL, token string, batch any) (int, []byte) {
	t.Helper()
	gz := gzipJSON(t, batch)
	return postIngestRaw(t, baseURL, token, gz, map[string]string{
		"Content-Encoding": "gzip",
		"Content-Type":     "application/json",
	})
}

func postIngestRaw(t *testing.T, baseURL, token string, body []byte, headers map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/ingest", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new ingest request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/ingest: %v", err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /v1/ingest response: %v", err)
	}
	return resp.StatusCode, respBytes
}

type VMRequest struct {
	Timestamp time.Time
	RawBody   string
	Lines     []string
}

type MockVictoriaMetrics struct {
	Server *httptest.Server

	mu       sync.Mutex
	requests []VMRequest
}

func newMockVM(t *testing.T) *MockVictoriaMetrics {
	t.Helper()
	m := &MockVictoriaMetrics{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		raw := string(body)
		var lines []string
		for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				lines = append(lines, line)
			}
		}
		m.mu.Lock()
		m.requests = append(m.requests, VMRequest{
			Timestamp: time.Now().UTC(),
			RawBody:   raw,
			Lines:     lines,
		})
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(m.Server.Close)
	return m
}

func (m *MockVictoriaMetrics) URL() string {
	return m.Server.URL
}

func (m *MockVictoriaMetrics) Requests() []VMRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]VMRequest, len(m.requests))
	copy(out, m.requests)
	return out
}

func (m *MockVictoriaMetrics) WaitForSamples(t *testing.T, timeout time.Duration, minReqs int) []VMRequest {
	t.Helper()
	waitUntil(t, timeout, 50*time.Millisecond, func() bool {
		return len(m.Requests()) >= minReqs
	})
	return m.Requests()
}

func waitUntil(t *testing.T, timeout, interval time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(interval)
	}
	if cond() {
		return
	}
	t.Fatalf("condition not met within %s", timeout)
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}

func asUint64(v any) (uint64, bool) {
	switch n := v.(type) {
	case float64:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	case uint64:
		return n, true
	case int64:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	case int:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	default:
		return 0, false
	}
}

func extractRuleID(t *testing.T, body []byte) string {
	t.Helper()
	m := decodeMap(t, body)
	rule := asMap(m["rule"])
	if rule == nil {
		t.Fatalf("expected rule object in response; body=%s", string(body))
	}
	idNum, ok := asUint64(rule["id"])
	if !ok || idNum == 0 {
		t.Fatalf("expected positive rule.id in response; body=%s", string(body))
	}
	return strconv.FormatUint(idNum, 10)
}

func extractEntityID(t *testing.T, body []byte, nestedKey string) string {
	t.Helper()
	m := decodeMap(t, body)
	if id, ok := asUint64(m["id"]); ok && id > 0 {
		return strconv.FormatUint(id, 10)
	}
	if nested := asMap(m[nestedKey]); nested != nil {
		if id, ok := asUint64(nested["id"]); ok && id > 0 {
			return strconv.FormatUint(id, 10)
		}
		if s := asString(nested["id"]); s != "" {
			return s
		}
	}
	if s := asString(m["id"]); s != "" {
		return s
	}
	t.Fatalf("missing id in response (nestedKey=%q); body=%s", nestedKey, string(body))
	return ""
}

func extractPreviewMatches(t *testing.T, body []byte) []any {
	t.Helper()
	m := decodeMap(t, body)
	items := asSlice(m["matches"])
	if items == nil {
		t.Fatalf("expected matches array in preview response; body=%s", string(body))
	}
	return items
}

type FaultProxyRecord struct {
	Method       string
	Path         string
	Status       int
	Dropped      bool
	ResponseBody string
}

type FaultIngestProxy struct {
	Server *httptest.Server

	targetBaseURL string
	client        *http.Client

	mu               sync.Mutex
	failNextStatus   int
	failNextCount    int
	dropAfterForward int
	records          []FaultProxyRecord
}

func newFaultIngestProxy(t *testing.T, targetBaseURL string) *FaultIngestProxy {
	t.Helper()
	p := &FaultIngestProxy{
		targetBaseURL: strings.TrimSuffix(targetBaseURL, "/"),
		client:        &http.Client{Timeout: 10 * time.Second},
	}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serveHTTP))
	t.Cleanup(p.Server.Close)
	return p
}

func (p *FaultIngestProxy) URL() string {
	return p.Server.URL
}

func (p *FaultIngestProxy) SetFailNext(status, count int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failNextStatus = status
	p.failNextCount = count
}

func (p *FaultIngestProxy) SetDropAfterForward(count int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropAfterForward = count
}

func (p *FaultIngestProxy) Records() []FaultProxyRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]FaultProxyRecord, len(p.records))
	copy(out, p.records)
	return out
}

func (p *FaultIngestProxy) serveHTTP(w http.ResponseWriter, r *http.Request) {
	reqBody, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}

	p.mu.Lock()
	if p.failNextCount > 0 {
		p.failNextCount--
		st := p.failNextStatus
		if st == 0 {
			st = http.StatusServiceUnavailable
		}
		p.records = append(p.records, FaultProxyRecord{
			Method: r.Method,
			Path:   r.URL.Path,
			Status: st,
		})
		p.mu.Unlock()
		http.Error(w, "injected fault", st)
		return
	}
	shouldDrop := false
	if p.dropAfterForward > 0 {
		p.dropAfterForward--
		shouldDrop = true
	}
	p.mu.Unlock()

	fwdReq, err := http.NewRequestWithContext(r.Context(), r.Method, p.targetBaseURL+r.URL.RequestURI(), bytes.NewReader(reqBody))
	if err != nil {
		http.Error(w, "proxy request error", http.StatusBadGateway)
		return
	}
	for k, vals := range r.Header {
		for _, v := range vals {
			fwdReq.Header.Add(k, v)
		}
	}
	resp, err := p.client.Do(fwdReq)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "upstream read error", http.StatusBadGateway)
		return
	}

	p.mu.Lock()
	p.records = append(p.records, FaultProxyRecord{
		Method:       r.Method,
		Path:         r.URL.Path,
		Status:       resp.StatusCode,
		Dropped:      shouldDrop,
		ResponseBody: string(respBytes),
	})
	p.mu.Unlock()

	if shouldDrop {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		http.Error(w, "dropped", http.StatusBadGateway)
		return
	}

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBytes)
}
