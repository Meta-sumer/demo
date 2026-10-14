import { View } from 'react-native';
import { Tabs } from 'expo-router';
import { TopBar } from '../../../components/TopBar';
import { BottomNav } from '../../../components/BottomNav';
import { useTokenColor } from '../../../utils/theme-colors';

/** Signing in, or arriving from a deep link to a pushed screen, lands on Home. */
export const unstable_settings = { initialRouteName: 'home' };

/**
 * Primary destinations (brief section 5): Home · Markets · Trade · Trades ·
 * Account, under one top bar.
 *
 * The bar sits above the tabs rather than inside each screen, so the balance
 * never moves or re-mounts when the trader switches destination. Learn,
 * which the desktop rail lists, is reached from Home on a phone.
 */
export default function TabsLayout() {
  const background = useTokenColor('surface-950');
  return (
    <View style={{ flex: 1, backgroundColor: background }}>
      <TopBar />
      <Tabs
        tabBar={(props) => <BottomNav {...props} />}
        screenOptions={{ headerShown: false, sceneStyle: { backgroundColor: background } }}
      >
        <Tabs.Screen name="home" options={{ title: 'Home' }} />
        <Tabs.Screen name="markets" options={{ title: 'Markets' }} />
        <Tabs.Screen name="trade" options={{ title: 'Trade' }} />
        <Tabs.Screen name="trades" options={{ title: 'Trades' }} />
        <Tabs.Screen name="account" options={{ title: 'Account' }} />
      </Tabs>
    </View>
  );
}
