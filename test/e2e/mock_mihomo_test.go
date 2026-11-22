//go:build e2e

package e2e

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type MockMihomoConn struct {
	ID       string             `json:"id"`
	Upload   uint64             `json:"upload"`
	Download uint64             `json:"download"`
	Chains   []string           `json:"chains"`
	Rule     string             `json:"rule,omitempty"`
	Payload  string             `json:"rulePayload,omitempty"`
	Metadata MockMihomoMetadata `json:"metadata,omitempty"`
}

type MockMihomoMetadata struct {
	Network         string `json:"network,omitempty"`
	Type            string `json:"type,omitempty"`
	SourceIP        string `json:"sourceIP,omitempty"`
	DestinationIP   string `json:"destinationIP,omitempty"`
	SourcePort      string `json:"sourcePort,omitempty"`
	DestinationPort string `json:"destinationPort,omitempty"`
	Host            string `json:"host,omitempty"`
	SniffHost       string `json:"sniffHost,omitempty"`
	Process         string `json:"process,omitempty"`
	ProcessPath     string `json:"processPath,omitempty"`
}

type MockMihomoProxy struct {
	Name  string   `json:"name"`
	Type  string   `json:"type"`
	Alive bool     `json:"alive"`
	All   []string `json:"all,omitempty"`
	Now   string   `json:"now,omitempty"`
}

type MockSubscriptionInfo struct {
	Upload   uint64 `json:"Upload"`
	Download uint64 `json:"Download"`
	Total    uint64 `json:"Total"`
	Expire   int64  `json:"Expire"`
}

type MockMihomoProvider struct {
	Name             string                `json:"name"`
	Type             string                `json:"type"`
	VehicleType      string                `json:"vehicleType"`
	Proxies          []MockMihomoProxy     `json:"proxies"`
	SubscriptionInfo *MockSubscriptionInfo `json:"subscriptionInfo,omitempty"`
}

type MockRequestRecord struct {
	Method    string
	Path      string
	Query     string
	Auth      string
	IsWS      bool
	Timestamp time.Time
}

type mockWSClient struct {
	conn    net.Conn
	rw      *bufio.ReadWriter
	writeMu sync.Mutex
	closed  chan struct{}
	once    sync.Once
}

func (c *mockWSClient) close() {
	c.once.Do(func() {
		close(c.closed)
		_ = c.conn.Close()
	})
}

func (c *mockWSClient) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	var hdr [10]byte
	hdr[0] = 0x80 | (opcode & 0x0f)
	n := len(payload)
	var hdrLen int
	switch {
	case n <= 125:
		hdr[1] = byte(n)
		hdrLen = 2
	case n <= 65535:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(n))
		hdrLen = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(n))
		hdrLen = 10
	}

	if _, err := c.conn.Write(hdr[:hdrLen]); err != nil {
		return err
	}
	if n > 0 {
		if _, err := c.conn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

type MockMihomo struct {
	Server *httptest.Server

	mu             sync.Mutex
	secret         string
	rejectWSStatus int
	providerStatus int
	version        string
	versionMeta    bool
	conns          []MockMihomoConn
	queuedFrames   [][]MockMihomoConn
	proxies        map[string]MockMihomoProxy
	providers      map[string]MockMihomoProvider
	requests       []MockRequestRecord
	wsClients      map[*mockWSClient]struct{}
	wsConnectCount int
	connReqCount   int
	closed         bool
	wg             sync.WaitGroup
}

func newMockMihomo(t *testing.T, secret string) *MockMihomo {
	t.Helper()
	m := &MockMihomo{
		secret:      secret,
		version:     "v1.18.0-synthetic",
		versionMeta: true,
		conns:       []MockMihomoConn{},
		proxies:     map[string]MockMihomoProxy{},
		providers:   map[string]MockMihomoProvider{},
		wsClients:   map[*mockWSClient]struct{}{},
	}
	m.Server = httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	t.Cleanup(m.Close)
	return m
}

func (m *MockMihomo) URL() string {
	return m.Server.URL
}

func (m *MockMihomo) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.DisconnectWS()
	m.wg.Wait()
	m.Server.Close()
}

func (m *MockMihomo) SetRejectWS(statusCode int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rejectWSStatus = statusCode
}

func (m *MockMihomo) SetConnections(conns []MockMihomoConn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conns = cloneConns(conns)
}

func (m *MockMihomo) QueueWSFrames(frames ...[]MockMihomoConn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queuedFrames = make([][]MockMihomoConn, 0, len(frames))
	for _, f := range frames {
		m.queuedFrames = append(m.queuedFrames, cloneConns(f))
	}
}

func (m *MockMihomo) PushWSFrame(conns []MockMihomoConn) error {
	payload := encodeConnectionsPayload(conns)
	m.mu.Lock()
	m.conns = cloneConns(conns)
	clients := make([]*mockWSClient, 0, len(m.wsClients))
	for c := range m.wsClients {
		clients = append(clients, c)
	}
	m.mu.Unlock()

	var firstErr error
	for _, c := range clients {
		if err := c.writeFrame(0x1, payload); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *MockMihomo) DisconnectWS() {
	m.mu.Lock()
	clients := make([]*mockWSClient, 0, len(m.wsClients))
	for c := range m.wsClients {
		clients = append(clients, c)
		delete(m.wsClients, c)
	}
	m.mu.Unlock()

	for _, c := range clients {
		c.close()
	}
}

func (m *MockMihomo) SetProxies(proxies map[string]MockMihomoProxy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make(map[string]MockMihomoProxy, len(proxies))
	for k, v := range proxies {
		cp[k] = v
	}
	m.proxies = cp
}

func (m *MockMihomo) SetProviders(providers map[string]MockMihomoProvider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make(map[string]MockMihomoProvider, len(providers))
	for k, v := range providers {
		cp[k] = v
	}
	m.providers = cp
}

func (m *MockMihomo) SetProviderStatus(status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providerStatus = status
}

func (m *MockMihomo) SetVersion(version string, meta bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.version = version
	m.versionMeta = meta
}

func (m *MockMihomo) Requests() []MockRequestRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MockRequestRecord, len(m.requests))
	copy(out, m.requests)
	return out
}

func (m *MockMihomo) NonGetRequests() []MockRequestRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []MockRequestRecord
	for _, r := range m.requests {
		if r.Method != http.MethodGet {
			out = append(out, r)
		}
	}
	return out
}

func (m *MockMihomo) WSConnectCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.wsConnectCount
}

func (m *MockMihomo) ActiveWSCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.wsClients)
}

func (m *MockMihomo) ConnRequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connReqCount
}

func (m *MockMihomo) checkAuth(r *http.Request) bool {
	m.mu.Lock()
	secret := m.secret
	m.mu.Unlock()
	if secret == "" {
		return true
	}
	if r.Header.Get("Authorization") == "Bearer "+secret {
		return true
	}
	if r.URL.Query().Get("token") == secret {
		return true
	}
	return false
}

func (m *MockMihomo) serveHTTP(w http.ResponseWriter, r *http.Request) {
	isWS := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	m.mu.Lock()
	m.requests = append(m.requests, MockRequestRecord{
		Method:    r.Method,
		Path:      r.URL.Path,
		Query:     r.URL.RawQuery,
		Auth:      r.Header.Get("Authorization"),
		IsWS:      isWS,
		Timestamp: time.Now().UTC(),
	})
	m.mu.Unlock()

	if !m.checkAuth(r) {
		http.Error(w, `{"message":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	switch r.URL.Path {
	case "/connections":
		m.handleConnections(w, r, isWS)
	case "/proxies":
		m.mu.Lock()
		proxies := make(map[string]MockMihomoProxy, len(m.proxies))
		for k, v := range m.proxies {
			proxies[k] = v
		}
		m.mu.Unlock()
		writeJSONResponse(w, http.StatusOK, map[string]any{"proxies": proxies})
	case "/providers/proxies":
		m.mu.Lock()
		status := m.providerStatus
		providers := make(map[string]MockMihomoProvider, len(m.providers))
		for k, v := range m.providers {
			providers[k] = v
		}
		m.mu.Unlock()
		if status != 0 && status != http.StatusOK {
			http.Error(w, "provider error", status)
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]any{"providers": providers})
	case "/version":
		m.mu.Lock()
		ver := m.version
		meta := m.versionMeta
		m.mu.Unlock()
		writeJSONResponse(w, http.StatusOK, map[string]any{"meta": meta, "version": ver})
	default:
		http.NotFound(w, r)
	}
}

func (m *MockMihomo) handleConnections(w http.ResponseWriter, r *http.Request, isWS bool) {
	if !isWS {
		m.mu.Lock()
		m.connReqCount++
		conns := cloneConns(m.conns)
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(encodeConnectionsPayload(conns))
		return
	}

	m.mu.Lock()
	reject := m.rejectWSStatus
	m.mu.Unlock()
	if reject != 0 {
		http.Error(w, "websocket unsupported", reject)
		return
	}

	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}

	sum := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	handshake := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := rw.WriteString(handshake); err != nil {
		_ = conn.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return
	}

	client := &mockWSClient{
		conn:   conn,
		rw:     rw,
		closed: make(chan struct{}),
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = conn.Close()
		return
	}
	queued := m.queuedFrames
	m.queuedFrames = nil
	var initial []MockMihomoConn
	if len(queued) > 0 {
		initial = queued[0]
		m.conns = cloneConns(initial)
	} else {
		initial = cloneConns(m.conns)
	}
	m.mu.Unlock()

	// Write the startup baseline frame before exposing client in m.wsClients or
	// incrementing wsConnectCount/connReqCount so PushWSFrame cannot overtake it.
	if err := client.writeFrame(0x1, encodeConnectionsPayload(initial)); err != nil {
		client.close()
		return
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		client.close()
		return
	}
	m.wsClients[client] = struct{}{}
	m.wsConnectCount++
	m.connReqCount++
	m.wg.Add(1)
	if len(queued) > 1 {
		m.wg.Add(1)
	}
	m.mu.Unlock()

	if len(queued) > 1 {
		go func(frames [][]MockMihomoConn) {
			defer m.wg.Done()
			for _, frame := range frames {
				select {
				case <-client.closed:
					return
				case <-time.After(150 * time.Millisecond):
					m.mu.Lock()
					m.conns = cloneConns(frame)
					m.mu.Unlock()
					if err := client.writeFrame(0x1, encodeConnectionsPayload(frame)); err != nil {
						return
					}
				}
			}
		}(queued[1:])
	}

	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			delete(m.wsClients, client)
			m.mu.Unlock()
			client.close()
		}()
		for {
			opcode, payload, err := readWSFrame(client.rw.Reader)
			if err != nil {
				return
			}
			switch opcode {
			case 0x0, 0x1, 0x2: // Unexpected client-to-server WS data frame
				m.mu.Lock()
				m.requests = append(m.requests, MockRequestRecord{
					Method:    "WS_DATA",
					Path:      r.URL.Path,
					Query:     r.URL.RawQuery,
					Auth:      r.Header.Get("Authorization"),
					IsWS:      true,
					Timestamp: time.Now().UTC(),
				})
				m.mu.Unlock()
			case 0x8: // Close
				_ = client.writeFrame(0x8, nil)
				return
			case 0x9: // Ping
				_ = client.writeFrame(0xA, payload)
			}
		}
	}()
}

func readWSFrame(r *bufio.Reader) (byte, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	fin := (hdr[0] & 0x80) != 0
	rsv := hdr[0] & 0x70
	if rsv != 0 {
		return 0, nil, errors.New("ws non-zero rsv bits")
	}
	opcode := hdr[0] & 0x0f
	masked := (hdr[1] & 0x80) != 0
	if !masked {
		return 0, nil, errors.New("ws client frame must be masked")
	}
	length := uint64(hdr[1] & 0x7f)
	if (opcode&0x08) != 0 && (!fin || length > 125) {
		return 0, nil, errors.New("ws invalid control frame")
	}

	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}

	if length > 4<<20 {
		return 0, nil, errors.New("ws frame too large")
	}

	var maskKey [4]byte
	if _, err := io.ReadFull(r, maskKey[:]); err != nil {
		return 0, nil, err
	}

	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	for i := range payload {
		payload[i] ^= maskKey[i%4]
	}
	return opcode, payload, nil
}

func cloneConns(in []MockMihomoConn) []MockMihomoConn {
	if in == nil {
		return []MockMihomoConn{}
	}
	out := make([]MockMihomoConn, len(in))
	for i, c := range in {
		if c.Chains != nil {
			chains := make([]string, len(c.Chains))
			copy(chains, c.Chains)
			c.Chains = chains
		} else {
			c.Chains = []string{}
		}
		out[i] = c
	}
	return out
}

func encodeConnectionsPayload(conns []MockMihomoConn) []byte {
	conns = cloneConns(conns)
	var downTotal, upTotal uint64
	for _, c := range conns {
		downTotal += c.Download
		upTotal += c.Upload
	}
	raw, _ := json.Marshal(map[string]any{
		"downloadTotal": downTotal,
		"uploadTotal":   upTotal,
		"memory":        102400,
		"connections":   conns,
	})
	return raw
}

func writeJSONResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
