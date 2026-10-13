import { useEffect } from 'react';
import { Navigate, Route, Routes } from 'react-router-dom';
import { useAuthStore } from './stores/authStore';
import { setUnauthorisedHandler } from './services/http';
import { watchSystemTheme } from './stores/themeStore';
import { Layout } from './components/Layout';
import { PageContainer } from './components/PageContainer';
import { ProtectedRoute, PublicOnlyRoute } from './components/ProtectedRoute';
import { LandingPage } from './pages/Landing';
import { PrivacyPage, TermsPage } from './pages/Legal';
import { LoginPage } from './pages/Login';
import { RegisterPage } from './pages/Register';
import { HomePage } from './pages/Home';
import { TradePage } from './pages/Trade';
import { MarketsPage } from './pages/Markets';
import { TradesPage } from './pages/Trades';
import { TradeDetailPage } from './pages/TradeDetail';
import { WalletPage } from './pages/Wallet';
import { UsdtWalletPage } from './pages/UsdtWallet';
import { UsdtDepositStatusPage } from './pages/UsdtDepositStatus';
import { TransferPage } from './pages/Transfer';
import { AccountPage } from './pages/Profile';
import { SettingsPage } from './pages/Settings';
import { SupportPage } from './pages/Support';
import { LearnPage } from './pages/Learn';
import { OnboardingPage } from './pages/Onboarding';

/**
 * Routes.
 *
 * The signed-in destinations follow the brief's navigation (section 5):
 * Home, Markets, Trade, Trades, Learn and Account. Older addresses - bookmarks,
 * links in notifications - redirect to where their content lives now rather
 * than breaking: /dashboard is Home, /assets is Markets, /profile is Account,
 * /support is the chat inside Account.
 */
export function App() {
  const hydrate = useAuthStore((s) => s.hydrate);

  // Keeps the "system" theme following the OS while the tab is open.
  useEffect(() => watchSystemTheme(), []);

  useEffect(() => {
    void hydrate();
    // When a refresh finally fails, drop the session rather than looping.
    setUnauthorisedHandler(() => {
      useAuthStore.setState({ status: 'anonymous', user: null, wallet: null });
    });
  }, [hydrate]);

  return (
    <Routes>
      <Route
        path="/login"
        element={
          <PublicOnlyRoute>
            <LoginPage />
          </PublicOnlyRoute>
        }
      />
      <Route
        path="/register"
        element={
          <PublicOnlyRoute>
            <RegisterPage />
          </PublicOnlyRoute>
        }
      />

      {/* Full screen, outside the shell: five short steps that end in the
          demo account, with nothing else competing for attention. */}
      <Route
        path="/onboarding"
        element={
          <ProtectedRoute>
            <OnboardingPage />
          </ProtectedRoute>
        }
      />

      <Route
        element={
          <ProtectedRoute>
            <Layout />
          </ProtectedRoute>
        }
      >
        <Route path="/home" element={<PageContainer><HomePage /></PageContainer>} />
        <Route path="/markets" element={<PageContainer><MarketsPage /></PageContainer>} />
        <Route path="/trade" element={<TradePage />} />
        <Route path="/trades" element={<PageContainer><TradesPage /></PageContainer>} />
        <Route path="/trades/:id" element={<PageContainer><TradeDetailPage /></PageContainer>} />
        <Route path="/learn" element={<PageContainer><LearnPage /></PageContainer>} />
        <Route path="/account" element={<PageContainer><AccountPage /></PageContainer>} />
        <Route path="/account/support" element={<PageContainer><SupportPage /></PageContainer>} />
        <Route path="/wallet" element={<PageContainer><WalletPage /></PageContainer>} />
        <Route path="/wallet/usdt" element={<PageContainer><UsdtWalletPage /></PageContainer>} />
        {/* The payment provider's return address (successUrl). */}
        <Route path="/wallet/usdt/success" element={<PageContainer><UsdtDepositStatusPage /></PageContainer>} />
        <Route path="/deposit" element={<PageContainer><TransferPage kind="DEPOSIT" /></PageContainer>} />
        <Route path="/withdraw" element={<PageContainer><TransferPage kind="WITHDRAWAL" /></PageContainer>} />
        <Route path="/settings" element={<PageContainer><SettingsPage /></PageContainer>} />

        <Route path="/dashboard" element={<Navigate to="/home" replace />} />
        <Route path="/assets" element={<Navigate to="/markets" replace />} />
        <Route path="/profile" element={<Navigate to="/account" replace />} />
        <Route path="/support" element={<Navigate to="/account/support" replace />} />
      </Route>

      {/* The root is the front door for a visitor and Home for a trader.
          `PublicOnlyRoute` already sends an authenticated session to /home,
          so one route covers both without a second redirect hop. */}
      <Route
        path="/"
        element={
          <PublicOnlyRoute>
            <LandingPage />
          </PublicOnlyRoute>
        }
      />
      {/* Readable signed in or out: registration asks a visitor to accept
          both before they have an account. */}
      <Route path="/terms" element={<TermsPage />} />
      <Route path="/privacy" element={<PrivacyPage />} />

      <Route path="*" element={<Navigate to="/home" replace />} />
    </Routes>
  );
}
