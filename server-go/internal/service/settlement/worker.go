package settlement

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/redisx"
)

// Settlement worker (spec section 18).
//
// Unlike the other workers this one is NOT leader-elected: settlement is
// idempotent by construction, so every instance can consume the queue and the
// platform settles faster under load. BRPOP hands each trade to exactly one
// consumer, and the guards in SettleTrade cover the rest.

var (
	workerMu   sync.Mutex
	running    bool
	connection *redis.Client
	stopLoop   context.CancelFunc
	loopDone   chan struct{}
)

func isRunning() bool {
	workerMu.Lock()
	defer workerMu.Unlock()
	return running
}

func currentConnection() *redis.Client {
	workerMu.Lock()
	defer workerMu.Unlock()
	return connection
}

// Start launches the BRPOP consumer (no-op when WORKERS_ENABLED=false or
// already running).
func Start() {
	workerMu.Lock()
	defer workerMu.Unlock()
	if (config.E != nil && !config.E.WorkersEnabled) || running {
		return
	}
	running = true
	ctx, cancel := context.WithCancel(context.Background())
	stopLoop = cancel
	loopDone = make(chan struct{})
	done := loopDone
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				logx.Error("settlement worker loop crashed", "panic", r)
				workerMu.Lock()
				running = false
				workerMu.Unlock()
			}
		}()
		consume(ctx)
	}()
	logx.Info("settlement worker started")
}

// Stop ends the consumer. Closing the dedicated connection interrupts the
// in-flight BRPOP so shutdown is not delayed by its timeout; a settlement
// already in progress is allowed to finish (bounded by ctx).
func Stop(ctx context.Context) {
	workerMu.Lock()
	running = false
	cancel, done := stopLoop, loopDone
	stopLoop, loopDone = nil, nil
	if connection != nil {
		_ = connection.Close()
		connection = nil
	}
	workerMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
	logx.Info("settlement worker stopped")
}

func batchSize() int {
	if config.E != nil && config.E.SettlementBatchSize > 0 {
		return config.E.SettlementBatchSize
	}
	return 200
}

func consume(ctx context.Context) {
	workerMu.Lock()
	connection = redisx.NewConnection()
	workerMu.Unlock()

	for isRunning() {
		conn := currentConnection()
		if conn == nil {
			break
		}
		err := func() error {
			popped, err := conn.BRPop(ctx, time.Second, config.RedisKeys.SettlementQueue).Result()
			if err != nil {
				if redisx.IsNil(err) {
					return nil
				}
				return err
			}
			settleOne(popped[1])

			// Drain anything else that is already waiting before blocking again.
			batch, err := drain(ctx, conn, batchSize())
			if err != nil {
				return err
			}
			for _, id := range batch {
				settleOne(id)
			}
			return nil
		}()
		if err == nil {
			continue
		}
		if !isRunning() {
			break
		}
		logx.Error("settlement consumer error - backing off", "err", err.Error())
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
		}
		if errors.Is(err, redis.ErrClosed) {
			workerMu.Lock()
			if running {
				connection = redisx.NewConnection()
			}
			workerMu.Unlock()
		}
	}
}

func drain(ctx context.Context, conn *redis.Client, limit int) ([]string, error) {
	ids := []string{}
	for i := 0; i < limit; i++ {
		id, err := conn.RPop(ctx, config.RedisKeys.SettlementQueue).Result()
		if err != nil {
			if redisx.IsNil(err) {
				break
			}
			return nil, err
		}
		if id == "" {
			break
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// settleOne runs on a background context so a shutdown signal cannot abort a
// settlement half way (the Node loop also finishes its current trade).
func settleOne(tradeID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	outcome, err := SettleTrade(ctx, tradeID, "")
	if err != nil {
		logx.Error("settlement failed - trade left for the sweeper", "tradeId", tradeID, "err", err.Error())
		return
	}
	if outcome.Status == StatusSkippedNoPrice {
		// Put it back so the next pass can try again once a price arrives.
		if c := currentConnection(); c != nil {
			if err := c.LPush(ctx, config.RedisKeys.SettlementQueue, tradeID).Err(); err != nil {
				logx.Error("settlement failed - trade left for the sweeper", "tradeId", tradeID, "err", err.Error())
			}
		}
	}
}
