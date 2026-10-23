package config

import (
	"net/mail"
	"strings"
)

// Operational alerting by email (service/alerts). Off by default; when on, a
// misconfiguration stops the boot rather than leaving operators believing
// alerts reach them.

// Alert severities, lowest first.
const (
	AlertSeverityInfo     = "info"
	AlertSeverityWarning  = "warning"
	AlertSeverityCritical = "critical"
)

// AlertSeverities in increasing order.
var AlertSeverities = []string{AlertSeverityInfo, AlertSeverityWarning, AlertSeverityCritical}

// AlertSeverityRank orders severities (unknown = warning).
func AlertSeverityRank(s string) int {
	switch s {
	case AlertSeverityInfo:
		return 0
	case AlertSeverityCritical:
		return 2
	}
	return 1
}

// AlertSettings is the ALERT_* / SMTP_* block of Env.
type AlertSettings struct {
	EmailEnabled bool
	SMTPHost     string
	// SMTPPort 465 means implicit TLS; any other port must offer STARTTLS.
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	From         string
	To           []string
	// MinSeverity is the lowest severity that is emailed.
	MinSeverity string
	// MaxEmailsPerHour caps the messages sent (a digest counts once).
	MaxEmailsPerHour int

	// Monitor thresholds: a condition must last this long before it alerts.
	FeedStaleSeconds       int64
	BookUnavailableSeconds int64
	RateLimiterSeconds     int64
	SettlementStuckMinutes int64
	MonitorIntervalSeconds int64
	DigestThreshold        int
	SMTPTimeoutSeconds     int64
	EmailSubjectPrefix     string
}

// Alerts returns the alert settings, or the defaults (email off) when the
// configuration was assembled without them (tests).
func Alerts() AlertSettings {
	if E == nil || E.Alerts.SMTPPort == 0 {
		return DefaultAlertSettings()
	}
	return E.Alerts
}

// DefaultAlertSettings are the documented defaults with email off.
func DefaultAlertSettings() AlertSettings {
	return AlertSettings{
		SMTPPort: 587, MinSeverity: AlertSeverityWarning, MaxEmailsPerHour: 20, To: []string{},
		FeedStaleSeconds: 60, BookUnavailableSeconds: 30, RateLimiterSeconds: 60, SettlementStuckMinutes: 10,
		MonitorIntervalSeconds: 10, DigestThreshold: 3, SMTPTimeoutSeconds: 20, EmailSubjectPrefix: "[wintratrade]",
	}
}

// ParseAddressList reads a comma or semicolon separated list of addresses.
func ParseAddressList(raw string) ([]string, error) {
	out := []string{}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' }) {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		a, err := mail.ParseAddress(f)
		if err != nil {
			return nil, err
		}
		out = append(out, a.Address)
	}
	return out, nil
}

func loadAlerts(l *loader, e *Env) {
	d := DefaultAlertSettings()
	a := AlertSettings{
		EmailEnabled: l.boolean("ALERT_EMAIL_ENABLED", false),
		SMTPHost:     strings.TrimSpace(l.str("SMTP_HOST", "")),
		SMTPPort:     int(l.integer("SMTP_PORT", int64(d.SMTPPort), 1)),
		SMTPUsername: l.str("SMTP_USERNAME", ""),
		SMTPPassword: l.str("SMTP_PASSWORD", ""),
		From:         strings.TrimSpace(l.str("ALERT_EMAIL_FROM", "")),
		MinSeverity:  l.enum("ALERT_MIN_SEVERITY", d.MinSeverity, AlertSeverities...),

		MaxEmailsPerHour:       int(l.integer("ALERT_EMAIL_MAX_PER_HOUR", int64(d.MaxEmailsPerHour), 1)),
		FeedStaleSeconds:       l.integer("ALERT_FEED_STALE_SECONDS", d.FeedStaleSeconds, 5),
		BookUnavailableSeconds: l.integer("ALERT_BOOK_UNAVAILABLE_SECONDS", d.BookUnavailableSeconds, 5),
		RateLimiterSeconds:     l.integer("ALERT_RATE_LIMITER_SECONDS", d.RateLimiterSeconds, 5),
		SettlementStuckMinutes: l.integer("ALERT_SETTLEMENT_STUCK_MINUTES", d.SettlementStuckMinutes, 2),
		MonitorIntervalSeconds: d.MonitorIntervalSeconds,
		DigestThreshold:        d.DigestThreshold,
		SMTPTimeoutSeconds:     d.SMTPTimeoutSeconds,
		EmailSubjectPrefix:     l.str("ALERT_EMAIL_SUBJECT_PREFIX", d.EmailSubjectPrefix),
	}
	to, err := ParseAddressList(l.str("ALERT_EMAIL_TO", ""))
	if err != nil {
		l.fail("ALERT_EMAIL_TO", "Invalid email address list: "+err.Error())
		to = []string{}
	}
	a.To = to
	if a.SMTPPort > 65535 {
		l.fail("SMTP_PORT", "Number must be less than or equal to 65535")
	}
	validateAlerts(l, &a)
	e.Alerts = a
}

// validateAlerts: enabled alerting must be able to send, or the boot fails.
func validateAlerts(l *loader, a *AlertSettings) {
	if a.From != "" {
		if addr, err := mail.ParseAddress(a.From); err != nil {
			l.fail("ALERT_EMAIL_FROM", "Invalid email address")
		} else {
			a.From = addr.Address
		}
	}
	if (a.SMTPUsername == "") != (a.SMTPPassword == "") {
		l.fail("SMTP_USERNAME", "SMTP_USERNAME and SMTP_PASSWORD must be set together")
	}
	if !a.EmailEnabled {
		return
	}
	if a.SMTPHost == "" {
		l.fail("SMTP_HOST", "SMTP_HOST is required when ALERT_EMAIL_ENABLED=true")
	}
	if a.From == "" {
		l.fail("ALERT_EMAIL_FROM", "ALERT_EMAIL_FROM is required when ALERT_EMAIL_ENABLED=true")
	}
	if len(a.To) == 0 {
		l.fail("ALERT_EMAIL_TO", "ALERT_EMAIL_TO needs at least one address when ALERT_EMAIL_ENABLED=true")
	}
}
