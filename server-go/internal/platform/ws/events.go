// Package ws is the websocket hub and the cross-instance event bus.
//
// Producers call Emit. The envelope travels over Redis pub/sub so every server
// instance - Go or Node, the envelope format is shared - delivers it to the
// sockets it holds (spec sections 26-27).
package ws

import (
	"context"
	"encoding/json"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/redisx"
)

type Envelope struct {
	Channels  []string        `json:"channels"`
	Event     string          `json:"event"`
	Payload   json.RawMessage `json:"payload"`
	EmittedAt int64           `json:"emittedAt"`
}

// Publish sends an event and waits for Redis to accept it.
func Publish(ctx context.Context, channels []string, event string, payload any) {
	if len(channels) == 0 {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		logx.Error("failed to encode broadcast payload", "event", event, "err", err.Error())
		return
	}
	env := Envelope{Channels: channels, Event: event, Payload: body, EmittedAt: time.Now().UnixMilli()}
	raw, _ := json.Marshal(env)
	if err := redisx.R().Publish(ctx, config.PubSubBroadcast, raw).Err(); err != nil {
		logx.Error("failed to publish broadcast", "event", event, "err", err.Error())
	}
}

// EnvelopeFor encodes a broadcast for a caller that publishes it itself, in a
// Redis pipeline of its own, so the event stays in order with the writes
// around it (PUBLISH config.PubSubBroadcast <the bytes>).
func EnvelopeFor(channels []string, event string, payload json.RawMessage) ([]byte, error) {
	return json.Marshal(Envelope{Channels: channels, Event: event, Payload: payload, EmittedAt: time.Now().UnixMilli()})
}

// Emit is fire-and-forget for hot paths that must not wait on Redis. The
// payload is encoded before returning, so callers may reuse their values.
func Emit(channels []string, event string, payload any) {
	if len(channels) == 0 {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		logx.Error("failed to encode broadcast payload", "event", event, "err", err.Error())
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		Publish(ctx, channels, event, json.RawMessage(body))
	}()
}

// consume subscribes to the broadcast channel and hands envelopes to deliver.
func consume(ctx context.Context, deliver func(Envelope)) {
	sub := redisx.R().Subscribe(ctx, config.PubSubBroadcast)
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
				var env Envelope
				if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
					logx.Warn("received malformed broadcast envelope", "err", err.Error())
					continue
				}
				deliver(env)
			}
		}
	}()
	logx.Info("broadcast consumer started")
}
