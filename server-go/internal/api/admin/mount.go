// Package admin is the administration API mounted at /admin: the Node
// server's adminCoreRoutes, adminUserRoutes, adminAssetRoutes,
// adminTradingRoutes, adminFinanceRoutes, adminPaymentMethodRoutes and
// adminSupportRoutes, which Fastify registers as separate plugins sharing the
// /admin prefix.
//
// Each Fastify plugin applies authenticate / requireAdmin / the admin rate
// limit to its own routes only, so here every plugin becomes a chi Group (or
// a sub-route whose middleware sits in a Group): POST /admin/login stays
// public, and an unknown /admin path is a 404 rather than a 401, as in
// Fastify, where plugin hooks never run for unmatched routes.
package admin

import (
	"github.com/go-chi/chi/v5"

	authapi "binarytrading/internal/api/auth"
	supportapi "binarytrading/internal/api/support"
	"binarytrading/internal/platform/httpx"
)

// Mount registers every /admin route. Call it as r.Route("/admin", admin.Mount).
func Mount(r chi.Router) {
	// Admin sign-in is the ordinary login flow with an extra role gate.
	authapi.MountAdminLogin(r)

	// adminCoreRoutes (secured part), adminTradingRoutes and
	// adminFinanceRoutes share the /admin prefix and identical plugin hooks.
	r.Group(func(r chi.Router) {
		r.Use(httpx.Authenticate, httpx.RequireAdmin, httpx.RateLimit(httpx.RateLimits.Admin))
		mountCore(r)
		mountTrading(r)
		mountFinance(r)
	})

	r.Route("/users", func(r chi.Router) { r.Group(mountUsers) })
	r.Route("/assets", func(r chi.Router) { r.Group(mountAssets) })
	r.Route("/payment-methods", func(r chi.Router) { r.Group(mountPaymentMethods) })
	r.Route("/support", func(r chi.Router) { r.Group(supportapi.MountAdmin) })
	// Admin two-factor authentication and forced sign-out.
	r.Route("/security", func(r chi.Router) { r.Group(mountSecurity) })
}
