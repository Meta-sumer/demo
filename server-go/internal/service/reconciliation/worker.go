package reconciliation

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/service/alerts"
)

// Periodic ledger reconciliation (spec section 42).

var (
	workerMu sync.Mutex
	recLoop  *leader.Loop
)

func tick(ctx context.Context) error {
	run, err := RunReconciliation(ctx, 0)
	if err != nil {
		alerts.Raise(ctx, alerts.Alert{
			Code: "reconciliation.run_error", Severity: config.AlertSeverityWarning, Source: models.AlertSourceReconciliation,
			DedupKey: "reconciliation:error", Title: "Reconciliation could not run or record its result",
			Detail: "The scheduled reconciliation returned an error: " + err.Error(),
		})
		return err
	}
	if run.Status != "OK" {
		logx.Error("RECONCILIATION_ALERT", "reference", run.Reference, "alerts", run.Alerts)
	}
	RaiseRunAlert(ctx, run)
	return nil
}

// RaiseRunAlert turns a run that is not OK into an operational alert. A
// mismatch is critical and stays open until an operator resolves it: money
// that disagrees is never closed automatically. The key is the set of failing
// checks, so the same mismatch found again only bumps the counter, and a new
// kind of mismatch is a new alert (and a new email).
func RaiseRunAlert(ctx context.Context, run *ReconciliationResult) {
	switch run.Status {
	case models.ReconciliationAlert:
		failing := []string{}
		for _, c := range run.Checks {
			if !c.Passed {
				failing = append(failing, c.Name)
			}
		}
		sort.Strings(failing)
		alerts.Raise(ctx, alerts.Alert{
			Code: "reconciliation.mismatch", Severity: config.AlertSeverityCritical, Source: models.AlertSourceReconciliation,
			DedupKey: "reconciliation:alert:" + strings.Join(failing, ","),
			Title:    "Reconciliation mismatch: " + strings.Join(failing, ", "),
			Detail: "Run " + run.Reference + " found:\n" + strings.Join(run.Alerts, "\n") +
				"\nOpen the run in Governance > Reconciliation; runbook: production-readiness.md section 6.5.",
		})
	case models.ReconciliationFailed:
		alerts.Raise(ctx, alerts.Alert{
			Code: "reconciliation.failed", Severity: config.AlertSeverityWarning, Source: models.AlertSourceReconciliation,
			DedupKey: "reconciliation:failed", Title: "Reconciliation run aborted",
			Detail: "Run " + run.Reference + ": " + strings.Join(run.Alerts, "; ") + "\nThe checks did not complete; read the server log.",
		})
	}
}

// Start runs the reconciliation leader loop (no-op when WORKERS_ENABLED=false).
func Start() {
	if config.E != nil && !config.E.WorkersEnabled {
		return
	}
	workerMu.Lock()
	defer workerMu.Unlock()
	if recLoop == nil {
		interval := int64(300_000)
		if config.E != nil {
			interval = config.E.ReconciliationIntervalMs
		}
		recLoop = leader.New(leader.Options{
			Name:     "reconciliation",
			Interval: time.Duration(interval) * time.Millisecond,
			LockTTL:  time.Duration(max(60_000, interval)) * time.Millisecond,
			OnTick:   tick,
		})
	}
	recLoop.Start()
}

// Stop ends the loop and releases leadership.
func Stop(ctx context.Context) {
	workerMu.Lock()
	l := recLoop
	workerMu.Unlock()
	if l != nil {
		l.Stop()
	}
}
