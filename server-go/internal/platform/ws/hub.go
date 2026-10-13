package ws

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/authtoken"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/service/metrics"
)

// Authenticated websocket hub (spec section 26). A socket only ever receives
// the channels it is authorised for.

const (
	maxPayload     = 64 * 1024
	heartbeatEvery = 30 * time.Second
	writeTimeout   = 10 * time.Second
	sendBuffer     = 256
)

type client struct {
	id          string
	conn        *websocket.Conn
	userID      string
	role        string
	permissions []string
	channels    map[string]bool // guarded by hub.mu
	alive       atomic.Bool
	send        chan []byte
	closeOnce   sync.Once
	done        chan struct{}
}

type hub struct {
	mu           sync.RWMutex
	clients      map[string]*client
	channelIndex map[string]map[string]*client
	userIndex    map[string]map[string]*client
	cancel       context.CancelFunc
}

var h = &hub{
	clients:      map[string]*client{},
	channelIndex: map[string]map[string]*client{},
	userIndex:    map[string]map[string]*client{},
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Authentication is the access token, not the Origin header.
	CheckOrigin: func(*http.Request) bool { return true },
}

// Start subscribes to the broadcast bus.
func Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	h.cancel = cancel
	consume(ctx, deliver)
	logx.Info("websocket server attached at /ws")
}

// Stop closes every socket.
func Stop() {
	if h.cancel != nil {
		h.cancel()
	}
	h.mu.Lock()
	all := make([]*client, 0, len(h.clients))
	for _, c := range h.clients {
		all = append(all, c)
	}
	h.mu.Unlock()
	for _, c := range all {
		_ = c.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "Server shutting down"),
			time.Now().Add(time.Second))
		c.close()
	}
}

type Stats struct {
	Connections int `json:"connections"`
	Channels    int `json:"channels"`
	Users       int `json:"users"`
}

func GetStats() Stats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return Stats{Connections: len(h.clients), Channels: len(h.channelIndex), Users: len(h.userIndex)}
}

// Handler upgrades GET /ws?token=... after authenticating the token.
func Handler(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	total := len(h.clients)
	h.mu.RUnlock()
	if total >= config.E.WSMaxConnectionsTotal {
		http.Error(w, "Server connection limit reached", http.StatusServiceUnavailable)
		return
	}

	token := r.URL.Query().Get("token")
	if token == "" {
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			token = a[7:]
		}
	}
	if token == "" {
		http.Error(w, "Missing access token", http.StatusUnauthorized)
		return
	}
	claims, err := authtoken.VerifyAccess(token)
	if err != nil {
		http.Error(w, "Invalid access token", http.StatusUnauthorized)
		return
	}

	h.mu.RLock()
	existing := len(h.userIndex[claims.Subject])
	h.mu.RUnlock()
	if existing >= config.E.WSMaxConnectionsPerUser {
		http.Error(w, "Too many websocket connections for this account", http.StatusTooManyRequests)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // the upgrader already wrote the error response
	}
	perms := claims.Permissions
	if perms == nil {
		perms = []string{}
	}
	register(conn, claims.Subject, claims.Role, perms)
}

func register(conn *websocket.Conn, userID, role string, permissions []string) {
	c := &client{
		id:          uuid.NewString(),
		conn:        conn,
		userID:      userID,
		role:        role,
		permissions: permissions,
		channels:    map[string]bool{},
		send:        make(chan []byte, sendBuffer),
		done:        make(chan struct{}),
	}
	c.alive.Store(true)

	h.mu.Lock()
	h.clients[c.id] = c
	addIndex(h.userIndex, c.userID, c)
	h.mu.Unlock()

	// A socket is always subscribed to its own private channel.
	subscribe(c, []string{config.ChUser(c.userID)})

	go c.writeLoop()
	go c.readLoop()

	h.mu.RLock()
	channels := channelList(c)
	h.mu.RUnlock()
	c.sendEvent(config.EvConnected, map[string]any{
		"socketId":   c.id,
		"userId":     c.userID,
		"role":       c.role,
		"serverTime": time.Now().UnixMilli(),
		"channels":   channels,
	})
}

func (c *client) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
		h.mu.Lock()
		delete(h.clients, c.id)
		removeIndex(h.userIndex, c.userID, c.id)
		for ch := range c.channels {
			removeIndex(h.channelIndex, ch, c.id)
		}
		h.mu.Unlock()
	})
}

func (c *client) readLoop() {
	defer c.close()
	c.conn.SetReadLimit(maxPayload)
	c.conn.SetPongHandler(func(string) error {
		c.alive.Store(true)
		return nil
	})
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		c.handleMessage(raw)
	}
}

func (c *client) writeLoop() {
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	defer c.close()
	for {
		select {
		case <-c.done:
			return
		case frame := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := c.conn.WriteMessage(websocket.TextMessage, frame); err != nil {
				logx.Debug("websocket send failed", "socketId", c.id, "err", err.Error())
				return
			}
		case <-ticker.C:
			if !c.alive.Load() {
				return
			}
			c.alive.Store(false)
			if err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				return
			}
		}
	}
}

// enqueue never blocks: a consumer too slow to drain its buffer is dropped
// rather than stalling delivery to everyone else.
func (c *client) enqueue(frame []byte) {
	select {
	case <-c.done:
	case c.send <- frame:
	default:
		logx.Warn("websocket consumer too slow - closing", "socketId", c.id)
		go c.close()
	}
}

func (c *client) sendEvent(event string, payload any) {
	frame, err := json.Marshal(map[string]any{"event": event, "payload": payload, "serverTime": time.Now().UnixMilli()})
	if err != nil {
		return
	}
	c.enqueue(frame)
}

func (c *client) handleMessage(raw []byte) {
	var msg struct {
		Type     string `json:"type"`
		Channels any    `json:"channels"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		c.sendEvent(config.EvError, map[string]any{"code": "BAD_FRAME", "message": "Frames must be JSON"})
		return
	}
	switch msg.Type {
	case "ping":
		c.alive.Store(true)
		c.sendEvent(config.EvPong, map[string]any{"serverTime": time.Now().UnixMilli()})
	case "subscribe":
		requested := toChannelList(msg.Channels)
		accepted := subscribe(c, requested)
		acceptedSet := map[string]bool{}
		for _, a := range accepted {
			acceptedSet[a] = true
		}
		rejected := []string{}
		for _, r := range requested {
			if !acceptedSet[r] {
				rejected = append(rejected, r)
			}
		}
		h.mu.RLock()
		channels := channelList(c)
		h.mu.RUnlock()
		c.sendEvent(config.EvSubscribed, map[string]any{"channels": channels, "accepted": accepted, "rejected": rejected})
	case "unsubscribe":
		own := config.ChUser(c.userID)
		h.mu.Lock()
		for _, ch := range toChannelList(msg.Channels) {
			if ch == own {
				continue
			}
			delete(c.channels, ch)
			removeIndex(h.channelIndex, ch, c.id)
		}
		channels := channelList(c)
		h.mu.Unlock()
		c.sendEvent(config.EvSubscribed, map[string]any{"channels": channels})
	default:
		c.sendEvent(config.EvError, map[string]any{"code": "UNKNOWN_TYPE", "message": "Unsupported frame type"})
	}
}

func subscribe(c *client, channels []string) []string {
	accepted := []string{}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range channels {
		if !canSubscribe(c, ch) {
			continue
		}
		c.channels[ch] = true
		addIndex(h.channelIndex, ch, c)
		accepted = append(accepted, ch)
	}
	return accepted
}

// canSubscribe is channel-level authorisation - the reason one user cannot
// read another's book.
func canSubscribe(c *client, ch string) bool {
	switch {
	case strings.HasPrefix(ch, "market:"):
		return true
	case strings.HasPrefix(ch, "user:"):
		return ch == config.ChUser(c.userID)
	case ch == config.ChAdminRisk:
		return config.HasPermission(c.role, c.permissions, config.PermRiskRead) ||
			config.HasPermission(c.role, c.permissions, config.PermExposureRead)
	case ch == config.ChAdminMarket:
		return config.HasPermission(c.role, c.permissions, config.PermMarketDataRead)
	case ch == config.ChAdminSupport:
		return config.HasPermission(c.role, c.permissions, config.PermSupportRead)
	case ch == config.ChAdminOps:
		return config.IsAdminRole(c.role)
	}
	return false
}

func deliver(env Envelope) {
	h.mu.RLock()
	targets := map[string]*client{}
	for _, ch := range env.Channels {
		for id, c := range h.channelIndex[ch] {
			targets[id] = c
		}
	}
	h.mu.RUnlock()
	if len(targets) == 0 {
		return
	}
	payload := env.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}
	frame, err := json.Marshal(struct {
		Event      string          `json:"event"`
		Payload    json.RawMessage `json:"payload"`
		EmittedAt  int64           `json:"emittedAt"`
		ServerTime int64           `json:"serverTime"`
	}{env.Event, payload, env.EmittedAt, time.Now().UnixMilli()})
	if err != nil {
		return
	}
	for _, c := range targets {
		c.enqueue(frame)
	}
	metrics.RecordLatency(config.LatWSDelivery, float64(time.Now().UnixMilli()-env.EmittedAt))
}

func addIndex(index map[string]map[string]*client, key string, c *client) {
	set := index[key]
	if set == nil {
		set = map[string]*client{}
		index[key] = set
	}
	set[c.id] = c
}

func removeIndex(index map[string]map[string]*client, key, id string) {
	set := index[key]
	if set == nil {
		return
	}
	delete(set, id)
	if len(set) == 0 {
		delete(index, key)
	}
}

func channelList(c *client) []string {
	out := make([]string, 0, len(c.channels))
	for ch := range c.channels {
		out = append(out, ch)
	}
	return out
}

func toChannelList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return []string{}
	}
	out := []string{}
	for _, e := range arr {
		if s, ok := e.(string); ok && len(s) <= 128 {
			out = append(out, s)
			if len(out) == 100 {
				break
			}
		}
	}
	return out
}

// ClientIP is exported for handlers that need the same derivation.
func ClientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return strings.TrimSpace(strings.Split(f, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
