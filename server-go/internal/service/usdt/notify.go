package usdt

import (
	"context"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/micro"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/notifications"
)

// Kinds carried by the usdt.updated event.
const (
	KindDeposit    = "deposit"
	KindWithdrawal = "withdrawal"
	KindTransfer   = "transfer"
)

// UpdatedEvent is the usdt.updated payload.
type UpdatedEvent struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Status string `json:"status"`
}

// emitUpdated tells the owner's sessions a USDT record changed.
func emitUpdated(userID, kind, id, status string) {
	ws.Emit([]string{config.ChUser(userID)}, config.EvUsdtUpdated, UpdatedEvent{Kind: kind, ID: id, Status: status})
}

// notify stores an in-app notification without holding up the caller.
func notify(userID string, in notifications.Input) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := notifications.CreateNotification(ctx, userID, in); err != nil {
			logx.Error("usdt notification failed", "userId", userID, "type", in.Type, "err", err.Error())
		}
	}()
}

func usdt(v int64) string { return micro.Format(v) + " USDT" }

func notifyDepositCredited(d *models.UsdtDeposit, amount int64) {
	notify(d.UserID.Hex(), notifications.Input{
		Type:  "USDT_DEPOSIT_CREDITED",
		Title: "USDT deposit received",
		Body:  usdt(amount) + " has been added to your USDT wallet.",
		Data:  map[string]any{"kind": KindDeposit, "id": d.ID.Hex(), "amount": amount, "network": d.Network},
	})
}

func notifyWithdrawalCompleted(w *models.UsdtWithdrawal) {
	notify(w.UserID.Hex(), notifications.Input{
		Type:  "USDT_WITHDRAWAL_COMPLETED",
		Title: "USDT withdrawal sent",
		Body:  "Your withdrawal of " + usdt(w.Amount) + " has been sent.",
		Data:  map[string]any{"kind": KindWithdrawal, "id": w.ID.Hex(), "amount": w.Amount, "network": w.Network},
	})
}

func notifyWithdrawalFailed(w *models.UsdtWithdrawal, status string) {
	title := "USDT withdrawal failed"
	if status == models.WithdrawalRejected {
		title = "USDT withdrawal rejected"
	}
	notify(w.UserID.Hex(), notifications.Input{
		Type:  "USDT_WITHDRAWAL_FAILED",
		Title: title,
		Body:  "Your withdrawal of " + usdt(w.Amount) + " was not sent. The full amount is back in your USDT wallet.",
		Data:  map[string]any{"kind": KindWithdrawal, "id": w.ID.Hex(), "amount": w.Amount, "network": w.Network, "status": status},
	})
}
