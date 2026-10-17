package config

// SecurityRedisKeys are the session-revocation keys. Kept beside RedisKeys
// rather than in it so the shared struct does not change shape.
var SecurityRedisKeys = struct {
	// JSON {s: status, r: role, p: stored permissions, v: session version},
	// a short-lived cache of what the auth middleware checks on every request.
	SessionState func(userID string) string
}{
	SessionState: func(id string) string { return "auth:session:" + id },
}

// PubSubControl carries hub control messages between instances (for example
// "re-check this user's sockets after a revocation"). Separate from
// PubSubBroadcast so older instances never mistake one for an event.
const PubSubControl = "pubsub:control"
