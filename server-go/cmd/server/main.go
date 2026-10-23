// Command server is the process bootstrap (server/src/index.ts + app.ts).
//
// Order matters: datastores, then configuration, then HTTP, then the
// websocket hub, and only then the workers - a worker must never start
// against a half initialised process.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	adminapi "binarytrading/internal/api/admin"
	assetsapi "binarytrading/internal/api/assets"
	authapi "binarytrading/internal/api/auth"
	binaryapi "binarytrading/internal/api/binary"
	healthapi "binarytrading/internal/api/health"
	marketdataapi "binarytrading/internal/api/marketdata"
	notificationsapi "binarytrading/internal/api/notifications"
	riskapi "binarytrading/internal/api/risk"
	supportapi "binarytrading/internal/api/support"
	transfersapi "binarytrading/internal/api/transfers"
	usdtapi "binarytrading/internal/api/usdt"
	usersapi "binarytrading/internal/api/users"
	walletapi "binarytrading/internal/api/wallet"
	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/hedging"
	mdproviders "binarytrading/internal/service/marketdata/providers"
	"binarytrading/internal/service/pricinghedge"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/service/usdt"
	"binarytrading/internal/workers"
)

const shutdownTimeout = 15 * time.Second

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	logx.Init(cfg.LogLevel, cfg.NodeEnv)

	if err := run(cfg); err != nil {
		logx.Error("failed to start server", "err", err.Error())
		os.Exit(1)
	}
}

// newRouter builds the HTTP surface. Routes only parse, authorise and
// delegate; security headers, CORS, the body-size cap and the single error
// handler are applied here once for every route (spec sections 33-34).
func newRouter() http.Handler {
	r := chi.NewRouter()
	// Fastify answers both unknown paths and unknown methods with its 404
	// handler. Set before mounting so every sub-router inherits them.
	r.NotFound(httpx.NotFound)
	r.MethodNotAllowed(httpx.NotFound)
	r.Use(httpx.Recoverer, httpx.RequestContext, httpx.SecurityHeaders, httpx.CORS, httpx.BodyLimit)

	healthapi.Mount(r)
	r.Route("/auth", authapi.Mount)
	r.Route("/users", usersapi.Mount)
	r.Route("/assets", assetsapi.Mount)
	r.Route("/market-data", marketdataapi.Mount)
	r.Route("/binary", binaryapi.Mount)
	r.Route("/wallet", walletapi.Mount)
	// The USDT wallet (BeeX) and its signed webhook, which is public.
	r.Route("/wallet/usdt", usdtapi.Mount)
	usdtapi.MountWebhook(r)
	r.Route("/transfers", transfersapi.Mount)
	r.Route("/notifications", notificationsapi.Mount)
	r.Route("/support", supportapi.Mount)
	r.Route("/risk", riskapi.Mount)
	r.Route("/admin", adminapi.Mount)

	// The websocket hub sits beside the router, as the Node server's ws
	// server sits on the raw HTTP server's upgrade event: the upgrade needs
	// the connection's own (hijackable) ResponseWriter. A plain GET /ws that
	// is not an upgrade falls through to the router and its 404.
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/ws" && websocket.IsWebSocketUpgrade(req) {
			// The server read timeout would otherwise cut the socket off.
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Time{})
			_ = rc.SetWriteDeadline(time.Time{})
			ws.Handler(w, req)
			return
		}
		r.ServeHTTP(w, req)
	})
}

func run(cfg *config.Env) error {
	ctx := context.Background()

	if err := mongox.Connect(ctx, cfg.MongoURI, cfg.EnableMongoTransactions); err != nil {
		return fmt.Errorf("mongodb: %w", err)
	}
	if err := models.Migrate(ctx); err != nil {
		return fmt.Errorf("migration: %w", err)
	}
	models.EnsureIndexes(ctx)
	if err := redisx.Connect(ctx, cfg.RedisURL); err != nil {
		return fmt.Errorf("redis: %w", err)
	}

	// Materialises version 1 of the risk configuration on a fresh database
	// and fails fast if the configuration is unusable.
	riskCfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return fmt.Errorf("risk configuration: %w", err)
	}
	logx.Info("risk configuration loaded", "version", riskCfg.Version, "drawPolicy", string(riskCfg.DrawPolicy))

	// USDT networks, and the BeeX provider when USDT is enabled.
	if err := usdt.Boot(ctx); err != nil {
		return fmt.Errorf("usdt: %w", err)
	}
	// Launch scope (APPLY_LAUNCH_SCOPE=true): EUR/USD, GBP/USD and XAU/USD
	// are switched off, idempotently and audited.
	if err := assets.ApplyLaunchScopeAtBoot(ctx, cfg.ApplyLaunchScope); err != nil {
		return fmt.Errorf("launch scope: %w", err)
	}

	marketProvider, err := mdproviders.GetMarketDataProvider()
	if err != nil {
		return err
	}
	// Pricing reads hedge availability and cost from the hedging service, and
	// the hedge target uses the pricing engine's volatility (audit F-10).
	pricinghedge.Install()
	hedgeName, liveHedging := hedging.ProviderInfo()
	logx.Info("providers initialised",
		"marketProvider", marketProvider.Name(),
		"authoritativeFeed", marketProvider.Authoritative(),
		"hedgeProvider", hedgeName,
		"liveHedging", liveHedging)

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           newRouter(),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout, as in Node: proof downloads may be slow.
		ReadTimeout:    60 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 64 << 10,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	hubCtx, stopHub := context.WithCancel(context.Background())
	defer stopHub()
	ws.Start(hubCtx)

	if err := workers.Start(ctx); err != nil {
		return fmt.Errorf("workers: %w", err)
	}

	logx.Info("binary trading server ready", "port", cfg.Port, "host", cfg.Host, "env", cfg.NodeEnv)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-serveErr:
		return err
	case s := <-sig:
		shutdown(srv, stopHub, s.String())
	}
	return nil
}

// shutdown stops taking new work first, then drains, then releases
// connections. A hung step does not keep the process alive past the timeout.
func shutdown(srv *http.Server, stopHub context.CancelFunc, signal string) {
	logx.Info("shutting down", "signal", signal)
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	timer := time.AfterFunc(shutdownTimeout, func() {
		logx.Error("graceful shutdown timed out - forcing exit")
		os.Exit(1)
	})

	workers.Stop(ctx)
	ws.Stop()
	stopHub()
	failed := false
	if err := srv.Shutdown(ctx); err != nil {
		logx.Error("error during shutdown", "err", err.Error())
		failed = true
	}
	if err := redisx.Disconnect(); err != nil {
		logx.Error("error during shutdown", "err", err.Error())
		failed = true
	}
	if err := mongox.Disconnect(ctx); err != nil {
		logx.Error("error during shutdown", "err", err.Error())
		failed = true
	}
	timer.Stop()
	if failed {
		os.Exit(1)
	}
	logx.Info("shutdown complete")
	os.Exit(0)
}
