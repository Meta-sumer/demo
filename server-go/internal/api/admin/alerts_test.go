package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/alerts"
)

// The Alerts screen's routes: reading needs reconciliation:read, resolving
// reconciliation:resolve, a resolution is audited, and the status view
// carries no address or credential.

func TestAlertRoutesPermissionsAndAudit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	models.EnsureIndexes(ctx)
	config.E.Alerts = config.DefaultAlertSettings()
	config.E.Alerts.EmailEnabled, config.E.Alerts.SMTPHost = true, "smtp.example.test"
	config.E.Alerts.SMTPUsername, config.E.Alerts.SMTPPassword = "mailer", "smtp-secret-value"
	config.E.Alerts.From, config.E.Alerts.To = "alerts@example.test", []string{"ops@example.test"}

	alerts.Raise(ctx, alerts.Alert{Code: "exposure.book_unavailable", Severity: "critical", Source: models.AlertSourceExposure,
		DedupKey: "exposure:book", Title: "Exposure book not READY", Detail: "status DIRTY"})
	var stored models.Alert
	if err := mongox.C(models.CollAlerts).FindOne(ctx, bson.M{"dedupKey": "exposure:book"}).Decode(&stored); err != nil {
		t.Fatal(err)
	}

	support := h.token(h.user(config.RoleSupport))
	finance := h.token(h.user(config.RoleFinance))
	superAdmin := h.user(config.RoleSuperAdmin)
	super := h.token(superAdmin)

	if code, _ := h.do(http.MethodGet, "/admin/alerts", support, nil); code != http.StatusForbidden {
		t.Fatalf("SUPPORT listed alerts: %d", code)
	}
	code, env := h.do(http.MethodGet, "/admin/alerts?status=open", finance, nil)
	if code != http.StatusOK {
		t.Fatalf("FINANCE list: %d", code)
	}
	var page struct {
		Items []models.AlertView `json:"items"`
		Total int64              `json:"total"`
	}
	if err := json.Unmarshal(env["data"], &page); err != nil || page.Total != 1 || page.Items[0].Severity != "critical" {
		t.Fatalf("list: %s %v", env["data"], err)
	}
	if code, _ := h.do(http.MethodGet, "/admin/alerts?severity=loud", finance, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad filter: %d", code)
	}

	code, env = h.do(http.MethodGet, "/admin/alerts/status", finance, nil)
	if code != http.StatusOK {
		t.Fatalf("status: %d", code)
	}
	raw := string(env["data"])
	for _, secret := range []string{"smtp-secret-value", "ops@example.test", "mailer", "smtp.example.test"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("status leaks %q: %s", secret, raw)
		}
	}
	var st struct {
		Alerting struct {
			EmailEnabled bool  `json:"emailEnabled"`
			Recipients   int   `json:"recipients"`
			Open         int64 `json:"open"`
		} `json:"alerting"`
		RateLimiter struct {
			Mode string `json:"mode"`
		} `json:"rateLimiter"`
		ExposureBook struct {
			Status string `json:"status"`
		} `json:"exposureBook"`
	}
	if err := json.Unmarshal(env["data"], &st); err != nil || !st.Alerting.EmailEnabled || st.Alerting.Recipients != 1 ||
		st.Alerting.Open != 1 || st.RateLimiter.Mode == "" || st.ExposureBook.Status == "" {
		t.Fatalf("status %s %v", raw, err)
	}

	path := "/admin/alerts/" + stored.ID.Hex() + "/resolve"
	if code, _ := h.do(http.MethodPost, path, finance, map[string]string{"note": "checked"}); code != http.StatusForbidden {
		t.Fatalf("FINANCE resolved: %d", code)
	}
	if code, _ := h.do(http.MethodPost, path, super, map[string]string{"note": "x"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("short note: %d", code)
	}
	if code, _ := h.do(http.MethodPost, path, super, map[string]string{"note": "Redis restarted, book rebuilt"}); code != http.StatusOK {
		t.Fatalf("resolve: %d", code)
	}
	if code, _ := h.do(http.MethodPost, path, super, map[string]string{"note": "again please"}); code != http.StatusConflict {
		t.Fatalf("second resolve: %d", code)
	}
	var row models.AuditLog
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := mongox.C(models.CollAuditLogs).FindOne(ctx, bson.M{"action": "alert.resolved", "resourceId": stored.ID.Hex()}).Decode(&row)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resolution not audited: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if row.ActorID == nil || *row.ActorID != superAdmin.ID {
		t.Fatalf("audit actor: %+v", row.ActorID)
	}
}
