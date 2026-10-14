import '../../global.css';
import { useEffect, useState } from 'react';
import { Stack } from 'expo-router';
import * as SplashScreen from 'expo-splash-screen';
import { GestureHandlerRootView } from 'react-native-gesture-handler';
import { SafeAreaProvider } from 'react-native-safe-area-context';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ThemeRoot } from '../theme/ThemeRoot';
import { useAuthStore } from '../stores/authStore';
import { initTokens, setUnauthorisedHandler } from '../services/http';
import { useTokenColor } from '../utils/theme-colors';
import { API_URL } from '../config';

void SplashScreen.preventAutoHideAsync();

// The deployed API sleeps when idle and takes a while to answer its first
// request. Knocking now means it is usually awake by the time someone has
// typed their password. Nothing waits on the answer.
fetch(API_URL + '/health').catch(() => undefined);

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Live data arrives over the websocket, so aggressive refetching on focus
      // would only duplicate work.
      refetchOnWindowFocus: false,
      staleTime: 5_000,
      retry: (failureCount, error) => {
        const status = (error as { status?: number }).status ?? 0;
        if (status >= 400 && status < 500) return false;
        return failureCount < 2;
      },
    },
  },
});

export default function RootLayout() {
  return (
    <GestureHandlerRootView style={{ flex: 1 }}>
      <SafeAreaProvider>
        <QueryClientProvider client={queryClient}>
          <ThemeRoot>
            <Navigator />
          </ThemeRoot>
        </QueryClientProvider>
      </SafeAreaProvider>
    </GestureHandlerRootView>
  );
}

/**
 * Routes.
 *
 * The signed-in destinations follow the brief's navigation: Home, Markets,
 * Trade, Trades and Account as tabs, with everything else pushed over them.
 * Which half of the app is reachable is decided by the session alone: when it
 * flips - signing in, signing out, a refresh that finally fails - the router
 * moves to the first screen the new state allows.
 *
 * The splash screen stays up until the stored session has been checked, so a
 * signed-in trader never sees the welcome screen flash past.
 */
function Navigator() {
  const status = useAuthStore((s) => s.status);
  const [booted, setBooted] = useState(false);
  const background = useTokenColor('surface-950');

  useEffect(() => {
    // When a refresh finally fails, drop the session rather than looping.
    setUnauthorisedHandler(() => {
      useAuthStore.setState({ status: 'anonymous', user: null, wallet: null });
    });
    void initTokens()
      .then(() => useAuthStore.getState().hydrate())
      .finally(() => setBooted(true));
  }, []);

  useEffect(() => {
    if (booted) void SplashScreen.hideAsync();
  }, [booted]);

  if (!booted) return null;

  const authenticated = status === 'authenticated';

  return (
    <Stack screenOptions={{ headerShown: false, contentStyle: { backgroundColor: background }, animation: 'slide_from_right' }}>
      <Stack.Protected guard={authenticated}>
        <Stack.Screen name="(app)" options={{ animation: 'fade' }} />
        {/* Full screen, outside the tabs: five short steps that end in the
            demo account, with nothing else competing for attention. */}
        <Stack.Screen name="onboarding" options={{ animation: 'fade' }} />
      </Stack.Protected>

      <Stack.Protected guard={!authenticated}>
        <Stack.Screen name="welcome" options={{ animation: 'fade' }} />
        <Stack.Screen name="login" />
        <Stack.Screen name="register" />
      </Stack.Protected>

      <Stack.Screen name="index" />
      {/* Readable signed in or out: registration asks a visitor to accept
          both before they have an account. */}
      <Stack.Screen name="terms" />
      <Stack.Screen name="privacy" />
    </Stack>
  );
}
