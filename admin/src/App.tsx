import { useEffect } from 'react';
import { Navigate, Route, Routes } from 'react-router-dom';
import { useAuthStore } from './stores/authStore';
import { setUnauthorisedHandler } from './services/http';
import { watchSystemTheme } from './stores/themeStore';
import { AdminLayout } from './components/AdminLayout';
import { ProtectedRoute, RequirePermission } from './components/ProtectedRoute';
import { AdminLoginPage } from './pages/Login';
import { DashboardPage } from './pages/Dashboard';
import { UsersPage } from './pages/Users';
import { UserDetailPage } from './pages/UserDetail';
import { AssetsPage } from './pages/Assets';
import { AssetDetailPage } from './pages/AssetDetail';
import { PayoutsPage } from './pages/Payouts';
import { TradesPage } from './pages/Trades';
import { TradeDetailPage } from './pages/TradeDetail';
import { ExposurePage } from './pages/Exposure';
import { RiskPage } from './pages/Risk';
import { HedgingPage } from './pages/Hedging';
import { MarketDataPage } from './pages/MarketData';
import { WalletsPage } from './pages/Wallets';
import { TransfersPage } from './pages/Transfers';
import { PaymentMethodsPage } from './pages/PaymentMethods';
import { ReportsPage } from './pages/Reports';
import { AuditLogsPage } from './pages/AuditLogs';
import { ReconciliationPage } from './pages/Reconciliation';
import { AlertsPage } from './pages/Alerts';
import { SettingsPage } from './pages/Settings';
import { SecurityPage } from './pages/Security';
import { SupportPage } from './pages/Support';
import { UsdtDashboardPage } from './pages/usdt/UsdtDashboard';
import { UsdtDepositsPage } from './pages/usdt/UsdtDeposits';
import { UsdtWithdrawalsPage } from './pages/usdt/UsdtWithdrawals';
import { UsdtWebhookEventsPage } from './pages/usdt/UsdtWebhookEvents';
import { UsdtNetworksPage } from './pages/usdt/UsdtNetworks';

/** Route table mirroring the paths in spec section 19. */
const GUARDED_ROUTES: Array<{ path: string; permission: string; element: React.ReactNode }> = [
  { path: '/admin/users', permission: 'users:read', element: <UsersPage /> },
  { path: '/admin/users/:id', permission: 'users:read', element: <UserDetailPage /> },
  { path: '/admin/assets', permission: 'assets:read', element: <AssetsPage /> },
  { path: '/admin/assets/:id', permission: 'assets:read', element: <AssetDetailPage /> },
  { path: '/admin/payouts', permission: 'payouts:read', element: <PayoutsPage /> },
  { path: '/admin/binary/trades', permission: 'trades:read', element: <TradesPage /> },
  { path: '/admin/binary/trades/:id', permission: 'trades:read', element: <TradeDetailPage /> },
  { path: '/admin/exposure', permission: 'exposure:read', element: <ExposurePage /> },
  { path: '/admin/risk', permission: 'risk:read', element: <RiskPage /> },
  { path: '/admin/hedging', permission: 'hedge:read', element: <HedgingPage /> },
  { path: '/admin/market-data', permission: 'market-data:read', element: <MarketDataPage /> },
  { path: '/admin/support', permission: 'support:read', element: <SupportPage /> },
  { path: '/admin/wallets', permission: 'wallets:read', element: <WalletsPage /> },
  { path: '/admin/deposits', permission: 'deposits:read', element: <TransfersPage kind="DEPOSIT" /> },
  { path: '/admin/withdrawals', permission: 'withdrawals:read', element: <TransfersPage kind="WITHDRAWAL" /> },
  { path: '/admin/payment-methods', permission: 'deposits:read', element: <PaymentMethodsPage /> },
  { path: '/admin/usdt', permission: 'deposits:read', element: <UsdtDashboardPage /> },
  { path: '/admin/usdt/deposits', permission: 'deposits:read', element: <UsdtDepositsPage /> },
  { path: '/admin/usdt/withdrawals', permission: 'withdrawals:read', element: <UsdtWithdrawalsPage /> },
  { path: '/admin/usdt/webhook-events', permission: 'deposits:read', element: <UsdtWebhookEventsPage /> },
  { path: '/admin/usdt/networks', permission: 'deposits:read', element: <UsdtNetworksPage /> },
  { path: '/admin/reports', permission: 'reports:read', element: <ReportsPage /> },
  { path: '/admin/reconciliation', permission: 'reconciliation:read', element: <ReconciliationPage /> },
  { path: '/admin/alerts', permission: 'reconciliation:read', element: <AlertsPage /> },
  { path: '/admin/audit-logs', permission: 'audit:read', element: <AuditLogsPage /> },
  { path: '/admin/settings', permission: 'risk:read', element: <SettingsPage /> },
];

export function App() {
  const hydrate = useAuthStore((s) => s.hydrate);

  // Keeps the "system" theme following the OS while the tab is open.
  useEffect(() => watchSystemTheme(), []);

  useEffect(() => {
    void hydrate();
    setUnauthorisedHandler(() => {
      useAuthStore.setState({ status: 'anonymous', user: null });
    });
  }, [hydrate]);

  return (
    <Routes>
      <Route path="/admin/login" element={<AdminLoginPage />} />

      <Route
        element={
          <ProtectedRoute>
            <AdminLayout />
          </ProtectedRoute>
        }
      >
        <Route path="/admin/dashboard" element={<DashboardPage />} />
        {/* Every admin manages their own two-factor here; no permission needed. */}
        <Route path="/admin/security" element={<SecurityPage />} />

        {GUARDED_ROUTES.map((route) => (
          <Route
            key={route.path}
            path={route.path}
            element={<RequirePermission permission={route.permission}>{route.element}</RequirePermission>}
          />
        ))}
      </Route>

      <Route path="*" element={<Navigate to="/admin/dashboard" replace />} />
    </Routes>
  );
}
