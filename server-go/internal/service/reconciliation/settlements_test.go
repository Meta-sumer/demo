package reconciliation

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
)

// A settlement that fails every time used to be retried forever with no
// report. It is now a reconciliation check and an operational alert.

func (f *fixture) trade(status models.TradeStatus, expiry time.Time, updated time.Time) bson.ObjectID {
	f.t.Helper()
	id := bson.NewObjectID()
	if _, err := mongox.C(models.CollBinaryTrades).InsertOne(f.ctx, bson.M{
		"_id": id, "reference": "BT-" + id.Hex()[18:], "userId": f.uid, "symbol": "BTC/USD", "status": string(status),
		"accountType": models.AccountLive, "expiryTimestamp": expiry.UnixMilli(), "createdAt": updated, "updatedAt": updated,
	}); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func TestStuckSettlementsCheck(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	after := 10 * time.Minute

	// Not stuck: settling for 2 minutes; open and expired 1 minute ago (still
	// inside the price defer window); open and not yet expired; finished.
	f.trade(models.TradeStatusSettling, now.Add(-3*time.Minute), now.Add(-2*time.Minute))
	f.trade(models.TradeStatusOpen, now.Add(-time.Minute), now.Add(-2*time.Minute))
	f.trade(models.TradeStatusOpen, now.Add(time.Hour), now.Add(-time.Hour))
	f.trade(models.TradeStatusWon, now.Add(-time.Hour), now.Add(-time.Hour))

	c, err := stuckSettlements(f.ctx, now, after)
	if err != nil || !c.Passed || c.Actual != 0 || c.Name != "trades_stuck_settling" {
		t.Fatalf("healthy book: %+v %v", c, err)
	}

	// Stuck: settling for 11 minutes; open 15 minutes after its expiry.
	settling := f.trade(models.TradeStatusSettling, now.Add(-20*time.Minute), now.Add(-11*time.Minute))
	open := f.trade(models.TradeStatusOpen, now.Add(-15*time.Minute), now.Add(-16*time.Minute))
	c, err = stuckSettlements(f.ctx, now, after)
	if err != nil || c.Passed || c.Actual != 2 || c.Difference != 2 {
		t.Fatalf("stuck trades: %+v %v", c, err)
	}
	trades := c.Details["trades"].([]map[string]any)
	if len(trades) != 2 || trades[0]["tradeId"] != settling.Hex() || trades[1]["tradeId"] != open.Hex() {
		t.Fatalf("details (oldest expiry first): %+v", trades)
	}
	if c.Details["olderThanMinutes"] != int64(10) {
		t.Fatalf("threshold: %v", c.Details["olderThanMinutes"])
	}
	n, sample, err := FindStuckSettlements(f.ctx, now, after, 1)
	if err != nil || n != 2 || len(sample) != 1 || sample[0].Status != "SETTLING" {
		t.Fatalf("find: %d %+v %v", n, sample, err)
	}
}

func TestRunAlertBecomesAnOperationalAlert(t *testing.T) {
	f := newFixture(t)
	models.EnsureIndexes(f.ctx)
	saved := config.E
	config.E = &config.Env{Alerts: config.DefaultAlertSettings()}
	t.Cleanup(func() { config.E = saved })

	f.trade(models.TradeStatusSettling, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))
	run, err := RunReconciliation(f.ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != models.ReconciliationAlert {
		t.Fatalf("run status %s (%v)", run.Status, run.Alerts)
	}
	RaiseRunAlert(f.ctx, run)
	RaiseRunAlert(f.ctx, run)
	var rows []models.Alert
	cur, err := mongox.C(models.CollAlerts).Find(f.ctx, bson.M{"source": models.AlertSourceReconciliation})
	if err != nil {
		t.Fatal(err)
	}
	if err := cur.All(f.ctx, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Severity != "critical" || rows[0].Occurrences != 2 ||
		rows[0].DedupKey != "reconciliation:alert:trades_stuck_settling" {
		t.Fatalf("operational alert: %+v", rows)
	}
}
