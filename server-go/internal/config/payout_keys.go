package config

// PayoutGenerationKey is a Redis counter bumped on every payout configuration
// change. Each instance compares it with the value its cached payout table was
// loaded under, so a change is seen everywhere at once rather than when the
// cache TTL runs out. Namespaced by database, so deployments (and test
// databases) sharing one Redis do not invalidate each other.
func PayoutGenerationKey(database string) string { return "cfg:payouts:gen:" + database }
