package usdt

import (
	"context"
	"fmt"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/micro"
	"binarytrading/internal/service/beex"
)

// Boot prepares the USDT wallet at process start, on every instance (API
// and workers): the network rows, and - when USDT is enabled - the provider,
// so a provider that cannot be built stops the boot instead of failing the
// first customer. config.Load has already refused live mode without keys.
func Boot(ctx context.Context) error {
	if err := models.SeedUsdtNetworks(ctx); err != nil {
		return fmt.Errorf("seed networks: %w", err)
	}
	if !Enabled() {
		logx.Info("USDT payments disabled (USDT_ENABLED=false)")
		return nil
	}
	p, err := beex.Get()
	if err != nil {
		return err
	}
	logx.Info("USDT payments enabled", "provider", p.Name(), "mode", p.Mode(), "configured", p.Configured(),
		"reviewAbove", micro.Format(reviewAbove()), "creditBasis", creditBasis(),
		"transferToTrading", TransferToTradingEnabled(), "acceptTestMode", env().BeexAcceptTestMode)
	if Sandbox() {
		msg := "USDT is running against the BeeX sandbox: no real money moves"
		if config.IsProduction() {
			msg += " (ALLOW_BEEX_SANDBOX_IN_PRODUCTION=true). Add a ck_live_ BEEX_API_KEY and remove the sandbox switch to go live."
		}
		logx.Warn(msg, "provider", p.Name(), "mode", p.Mode())
	}
	return nil
}
