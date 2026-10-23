package config

import (
	"strings"
	"testing"
)

// Enabled email alerting must be able to send, or the server refuses to boot.

func alertIssues(env map[string]string, t *testing.T) (AlertSettings, string) {
	t.Helper()
	for _, k := range []string{"ALERT_EMAIL_ENABLED", "SMTP_HOST", "SMTP_PORT", "SMTP_USERNAME", "SMTP_PASSWORD",
		"ALERT_EMAIL_FROM", "ALERT_EMAIL_TO", "ALERT_MIN_SEVERITY", "ALERT_EMAIL_MAX_PER_HOUR"} {
		t.Setenv(k, env[k])
	}
	l := &loader{}
	e := &Env{}
	loadAlerts(l, e)
	return e.Alerts, strings.Join(l.issues, "\n")
}

func TestAlertEmailDisabledNeedsNothing(t *testing.T) {
	a, got := alertIssues(map[string]string{}, t)
	if got != "" {
		t.Fatalf("disabled: %q", got)
	}
	if a.EmailEnabled || a.SMTPPort != 587 || a.MinSeverity != AlertSeverityWarning || len(a.To) != 0 {
		t.Fatalf("defaults: %+v", a)
	}
	if a.FeedStaleSeconds != 60 || a.BookUnavailableSeconds != 30 || a.SettlementStuckMinutes != 10 || a.RateLimiterSeconds != 60 {
		t.Fatalf("thresholds: %+v", a)
	}
}

func TestAlertEmailEnabledRequiresAWorkingSetup(t *testing.T) {
	_, got := alertIssues(map[string]string{"ALERT_EMAIL_ENABLED": "true"}, t)
	for _, k := range []string{"SMTP_HOST", "ALERT_EMAIL_FROM", "ALERT_EMAIL_TO"} {
		if !strings.Contains(got, k) {
			t.Fatalf("missing %s issue in %q", k, got)
		}
	}
	_, got = alertIssues(map[string]string{"ALERT_EMAIL_ENABLED": "true", "SMTP_HOST": "smtp.example.com",
		"ALERT_EMAIL_FROM": "alerts@example.com", "ALERT_EMAIL_TO": "ops@example.com", "SMTP_USERNAME": "user"}, t)
	if !strings.Contains(got, "SMTP_USERNAME") {
		t.Fatalf("username without password accepted: %q", got)
	}
	_, got = alertIssues(map[string]string{"ALERT_EMAIL_ENABLED": "true", "SMTP_HOST": "smtp.example.com",
		"ALERT_EMAIL_FROM": "not an address", "ALERT_EMAIL_TO": "ops@example.com, @bad"}, t)
	if !strings.Contains(got, "ALERT_EMAIL_FROM") || !strings.Contains(got, "ALERT_EMAIL_TO") {
		t.Fatalf("bad addresses accepted: %q", got)
	}
	_, got = alertIssues(map[string]string{"ALERT_EMAIL_ENABLED": "true", "SMTP_HOST": "smtp.example.com",
		"ALERT_EMAIL_FROM": "a@example.com", "ALERT_EMAIL_TO": "b@example.com", "ALERT_MIN_SEVERITY": "loud"}, t)
	if !strings.Contains(got, "ALERT_MIN_SEVERITY") {
		t.Fatalf("unknown severity accepted: %q", got)
	}
	_, got = alertIssues(map[string]string{"ALERT_EMAIL_ENABLED": "true", "SMTP_HOST": "smtp.example.com", "SMTP_PORT": "70000",
		"ALERT_EMAIL_FROM": "a@example.com", "ALERT_EMAIL_TO": "b@example.com"}, t)
	if !strings.Contains(got, "SMTP_PORT") {
		t.Fatalf("port out of range accepted: %q", got)
	}
}

func TestAlertEmailValidSetup(t *testing.T) {
	a, got := alertIssues(map[string]string{"ALERT_EMAIL_ENABLED": "true", "SMTP_HOST": "smtp.example.com", "SMTP_PORT": "465",
		"SMTP_USERNAME": "u", "SMTP_PASSWORD": "p", "ALERT_EMAIL_FROM": "Alerts <alerts@example.com>",
		"ALERT_EMAIL_TO": "ops@example.com; risk@example.com", "ALERT_MIN_SEVERITY": "critical"}, t)
	if got != "" {
		t.Fatalf("valid setup refused: %q", got)
	}
	if a.From != "alerts@example.com" || len(a.To) != 2 || a.To[1] != "risk@example.com" || a.SMTPPort != 465 || a.MinSeverity != "critical" {
		t.Fatalf("%+v", a)
	}
	if AlertSeverityRank(AlertSeverityCritical) <= AlertSeverityRank(AlertSeverityWarning) ||
		AlertSeverityRank(AlertSeverityWarning) <= AlertSeverityRank(AlertSeverityInfo) {
		t.Fatal("severity order")
	}
}
