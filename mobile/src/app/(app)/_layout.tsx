import { View } from 'react-native';
import { Stack, useSegments } from 'expo-router';
import { useRealtime } from '../../hooks/useRealtime';
import { Toaster } from '../../components/ui';
import { TransferModal } from '../../features/transfer/TransferModal';
import { SettingsDrawer } from '../../components/SettingsDrawer';
import { TradeResultToasts } from '../../features/trade/TradeResultToasts';
import { useTokenColor } from '../../utils/theme-colors';

/**
 * The signed-in shell.
 *
 * Mounted once for the whole session: the websocket bridge lives here, and so
 * do the overlays that have to reach every screen - chart settings, the deposit and
 * withdrawal flow (opened over whatever is on screen, so funding an account
 * never costs sight of a position), settled-trade results and toasts.
 */
export default function AppLayout() {
  useRealtime();
  const segments = useSegments();
  const background = useTokenColor('surface-950');
  const onTabs = (segments as string[]).includes('(tabs)');

  return (
    <View style={{ flex: 1 }}>
      <Stack
        screenOptions={{ headerShown: false, contentStyle: { backgroundColor: background }, animation: 'slide_from_right' }}
      >
        <Stack.Screen name="(tabs)" />
      </Stack>

      <SettingsDrawer />
      <TransferModal />
      <TradeResultToasts bottomOffset={onTabs ? 64 : 0} />
      <Toaster bottomOffset={onTabs ? 64 : 0} />
    </View>
  );
}
