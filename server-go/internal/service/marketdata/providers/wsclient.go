package providers

import (
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"binarytrading/internal/platform/logx"
)

// wsClient is the reconnecting websocket shared by the Binance and generic
// providers. It reproduces the ws-library lifecycle the TS providers rely on:
// a socket "exists" from the moment it is opened (readyState CONNECTING), is
// connected once the handshake completes, and a close schedules a reconnect
// with exponential backoff (2s, 4s, ... capped at 30s) unless shutting down.
type wsClient struct {
	base   *Base
	url    func() string
	header http.Header
	// onOpen runs after CONNECTED has been emitted.
	onOpen    func()
	onMessage func([]byte)
	// announceRetry emits RECONNECTING "retrying in Nms" when a reconnect is
	// scheduled (Binance does, the generic adapter does not).
	announceRetry bool
	// closeDetail puts "closed <code> <reason>" on the DISCONNECTED status.
	closeDetail bool

	mu           sync.Mutex
	conn         *websocket.Conn
	opening      bool
	gen          int
	attempts     int
	timer        *time.Timer
	shuttingDown bool

	wmu sync.Mutex
}

var dialer = &websocket.Dialer{
	Proxy:            http.ProxyFromEnvironment,
	HandshakeTimeout: 15 * time.Second,
}

func (c *wsClient) hasSocketLocked() bool { return c.conn != nil || c.opening }

func (c *wsClient) hasSocket() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hasSocketLocked()
}

func (c *wsClient) isOpen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

func (c *wsClient) connect() {
	c.mu.Lock()
	if c.hasSocketLocked() {
		c.mu.Unlock()
		return
	}
	c.shuttingDown = false
	c.mu.Unlock()
	c.open()
}

func (c *wsClient) open() {
	c.mu.Lock()
	state := StateConnecting
	if c.attempts > 0 {
		state = StateReconnecting
	}
	c.opening = true
	c.gen++
	gen := c.gen
	c.mu.Unlock()

	c.base.emitStatus(state, "")
	url := c.url()
	go c.dial(gen, url)
}

func (c *wsClient) dial(gen int, url string) {
	conn, _, err := dialer.Dial(url, c.header)

	c.mu.Lock()
	if gen != c.gen {
		// Disconnected (or superseded) while the handshake was in flight.
		c.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		return
	}
	if err != nil {
		c.opening = false
		c.mu.Unlock()
		logx.Error("market feed socket error", "provider", c.base.name, "err", err.Error())
		c.base.emitStatus(StateFailed, err.Error())
		c.closed(gen, 1006, "")
		return
	}
	c.conn = conn
	c.opening = false
	c.attempts = 0
	c.mu.Unlock()

	c.base.emitStatus(StateConnected, "")
	if c.onOpen != nil {
		c.onOpen()
	}
	// The default ping handler answers pings promptly (Binance disconnects
	// sockets that do not).
	go c.read(gen, conn)
}

func (c *wsClient) read(gen int, conn *websocket.Conn) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			code, reason := 1006, ""
			var ce *websocket.CloseError
			if errors.As(err, &ce) {
				code, reason = ce.Code, ce.Text
			}
			c.mu.Lock()
			current := gen == c.gen && c.conn == conn
			if current {
				c.conn = nil
			}
			c.mu.Unlock()
			_ = conn.Close()
			if current {
				if ce == nil {
					logx.Error("market feed socket error", "provider", c.base.name, "err", err.Error())
					c.base.emitStatus(StateFailed, err.Error())
				}
				c.closed(gen, code, reason)
			}
			return
		}
		c.onMessage(data)
	}
}

// closed mirrors the ws 'close' event.
func (c *wsClient) closed(gen, code int, reason string) {
	msg := ""
	if c.closeDetail {
		msg = "closed " + strconv.Itoa(code) + " " + reason
	}
	c.base.emitStatus(StateDisconnected, msg)
	c.mu.Lock()
	shutting := c.shuttingDown || gen != c.gen
	c.mu.Unlock()
	if !shutting {
		c.scheduleReconnect()
	}
}

func (c *wsClient) scheduleReconnect() {
	c.mu.Lock()
	if c.timer != nil {
		c.mu.Unlock()
		return
	}
	c.attempts++
	delay := 1000 * (1 << min(c.attempts, 5))
	if delay > 30_000 {
		delay = 30_000
	}
	c.timer = time.AfterFunc(time.Duration(delay)*time.Millisecond, func() {
		c.mu.Lock()
		c.timer = nil
		if c.shuttingDown || c.hasSocketLocked() {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		c.open()
	})
	c.mu.Unlock()
	if c.announceRetry {
		c.base.emitStatus(StateReconnecting, "retrying in "+strconv.Itoa(delay)+"ms")
	}
}

func (c *wsClient) disconnect() {
	c.mu.Lock()
	c.shuttingDown = true
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	conn := c.conn
	c.conn = nil
	c.opening = false
	c.gen++
	c.mu.Unlock()
	if conn != nil {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		_ = conn.Close()
	}
	c.base.emitStatus(StateDisconnected, "")
}

// send writes a JSON message when the socket is open; otherwise it is dropped.
func (c *wsClient) send(v any) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := conn.WriteJSON(v); err != nil {
		logx.Warn("market feed send failed", "provider", c.base.name, "err", err.Error())
	}
}
