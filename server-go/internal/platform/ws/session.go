package ws

import (
	"context"
	"encoding/json"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/authtoken"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/redisx"
)

// Websocket session revalidation.
//
// A socket authenticates once, at the handshake, but can stay open for hours.
// So it re-checks its token against the user's live state (status, role,
// permissions, session version) on every subscribe, every revalidateEvery,
// and at once when a revocation anywhere in the cluster asks for it
// (RevalidateUser). A socket whose session has ended is told why and closed;
// one whose permissions shrank loses the channels it may no longer read.

type sessionCheck int

const (
	sessionValid sessionCheck = iota
	sessionEnded
	// sessionUnknown: the session store could not be read. The socket is
	// kept, but a subscribe is refused (fail closed) until it can be.
	sessionUnknown
)

const revalidateTimeout = 3 * time.Second

func (c *client) revalidate(ctx context.Context) sessionCheck {
	ctx, cancel := context.WithTimeout(ctx, revalidateTimeout)
	defer cancel()
	claims := &authtoken.AccessClaims{Role: c.tokenRole, Ver: c.tokenVer}
	claims.Subject = c.userID
	state, err := authtoken.CheckSession(ctx, claims)
	if err != nil {
		ae, ok := apperr.As(err)
		if !ok || ae.StatusCode >= 500 {
			return sessionUnknown
		}
		code := "SESSION_REVOKED"
		if ae.Code == apperr.AccountDisabled {
			code = apperr.AccountDisabled
		}
		c.end(code, ae.Message)
		return sessionEnded
	}
	if state == nil {
		return sessionValid
	}
	perms := state.EffectivePermissions()
	h.mu.Lock()
	c.role = state.Role
	c.permissions = perms
	for ch := range c.channels {
		if !canSubscribe(c, ch) {
			delete(c.channels, ch)
			removeIndex(h.channelIndex, ch, c.id)
		}
	}
	h.mu.Unlock()
	return sessionValid
}

// revalidateInBackground revalidates the socket. Requests that arrive while
// one is running are coalesced into one more pass after it, so a revocation
// is never lost behind a check that read the state just before it.
func (c *client) revalidateInBackground() {
	c.recheckPending.Store(true)
	for {
		if !c.revalidating.CompareAndSwap(false, true) {
			return // the running pass will see recheckPending
		}
		for c.recheckPending.Swap(false) {
			select {
			case <-c.done:
				c.revalidating.Store(false)
				return
			default:
			}
			if c.revalidate(context.Background()) == sessionEnded {
				c.revalidating.Store(false)
				return
			}
		}
		c.revalidating.Store(false)
		if !c.recheckPending.Load() {
			return
		}
	}
}

// end asks the write loop to send a final connection.error and close.
func (c *client) end(code, message string) {
	select {
	case c.kill <- killFrame{code: code, message: message}:
	default: // already ending
	}
}

type controlMessage struct {
	Type   string `json:"type"`
	UserID string `json:"userId"`
}

const controlRevalidateUser = "revalidate-user"

// RevalidateUser makes every socket the user holds, on every instance,
// re-check its session now. Call it after revoking or changing a user's
// access, once the new state is in MongoDB and the session cache.
func RevalidateUser(userID string) {
	revalidateLocal(userID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		rc := redisx.R()
		if rc == nil {
			return
		}
		raw, _ := json.Marshal(controlMessage{Type: controlRevalidateUser, UserID: userID})
		if err := rc.Publish(ctx, config.PubSubControl, raw).Err(); err != nil {
			// Other instances still catch it on their periodic revalidation.
			logx.Error("could not publish a websocket revalidation", "userId", userID, "err", err.Error())
		}
	}()
}

func revalidateLocal(userID string) {
	h.mu.RLock()
	sockets := make([]*client, 0, len(h.userIndex[userID]))
	for _, c := range h.userIndex[userID] {
		sockets = append(sockets, c)
	}
	h.mu.RUnlock()
	for _, c := range sockets {
		go c.revalidateInBackground()
	}
}

// UserSocketCount is the number of sockets the user holds on this instance.
func UserSocketCount(userID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.userIndex[userID])
}

// consumeControl subscribes to the hub control channel.
func consumeControl(ctx context.Context) {
	rc := redisx.R()
	if rc == nil {
		return
	}
	sub := rc.Subscribe(ctx, config.PubSubControl)
	go func() {
		defer sub.Close()
		ch := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				var m controlMessage
				if err := json.Unmarshal([]byte(msg.Payload), &m); err != nil {
					continue
				}
				if m.Type == controlRevalidateUser && m.UserID != "" {
					revalidateLocal(m.UserID)
				}
			}
		}
	}()
}
