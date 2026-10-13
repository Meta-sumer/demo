import { View } from 'react-native';
import { Screen, ScreenHeader } from '../../components/Screen';
import { SettingsScreen } from '../../features/settings/SettingsScreen';

export default function SettingsRoute() {
  return (
    <View className="flex-1 bg-surface-950">
      <ScreenHeader title="Settings" fallbackHref="/account" />
      <Screen bottomInset>
        <SettingsScreen />
      </Screen>
    </View>
  );
}
