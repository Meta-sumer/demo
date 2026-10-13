import { Pressable, View } from 'react-native';
import { useQuery } from '@tanstack/react-query';
import { api } from '../../services/api';
import { useSettingsStore } from '../../stores/settingsStore';
import { useThemeStore } from '../../stores/themeStore';
import { formatExpiry, formatMoney } from '../../utils/format';
import { cn } from '../../utils/cn';
import { FeedBadge, Field, Label, Panel, Skeleton, Text } from '../../components/ui';
import { ThemeToggle } from '../../components/ThemeToggle';
import { OneTapSwitch, SwitchRow, TradeModeControl, useDefaultStakeDraft } from '../../components/TradingPreferences';

/** Trading preferences plus a live view of platform feed health. */
export function SettingsScreen() {
  const settings = useSettingsStore();
  const themePreference = useThemeStore((s) => s.preference);
  const resolvedTheme = useThemeStore((s) => s.resolved);
  const [stakeText, onStakeText] = useDefaultStakeDraft();
  const healthQuery = useQuery({
    queryKey: ['market', 'health'],
    queryFn: api.market.health,
    refetchInterval: 3000,
  });

  return (
    <View className="gap-3">
      <Panel className="p-5">
        <Text accessibilityRole="header" className="text-sm font-semibold text-content-100">
          Appearance
        </Text>
        <View className="mt-4 gap-3 rounded-lg border border-surface-700 bg-surface-850 px-4 py-3">
          <View>
            <Text className="text-sm font-medium text-content-100">Colour theme</Text>
            <Text className="text-[11px] text-content-400">
              {themePreference === 'system'
                ? 'Following your device, currently ' + resolvedTheme
                : 'Always ' + themePreference}
            </Text>
          </View>
          <ThemeToggle />
        </View>
      </Panel>

      <Panel className="p-5">
        <Text accessibilityRole="header" className="text-sm font-semibold text-content-100">
          Trading preferences
        </Text>

        <View className="mt-4 gap-4">
          <Field
            label="Default stake"
            keyboardType="decimal-pad"
            inputClassName="tnum"
            value={stakeText}
            onChangeText={onStakeText}
            hint={'Pre-filled on the trade ticket: ' + formatMoney(settings.defaultStake)}
          />

          <View>
            <Label>Default expiry</Label>
            <View className="flex-row flex-wrap gap-1">
              {[30, 60, 300].map((seconds) => {
                const active = settings.defaultExpirySeconds === seconds;
                return (
                  <Pressable
                    key={seconds}
                    accessibilityRole="radio"
                    accessibilityState={{ checked: active }}
                    onPress={() => settings.setDefaultExpiry(seconds)}
                    className={cn(
                      'rounded-lg border px-3 py-1.5',
                      active ? 'border-accent bg-accent/15' : 'border-surface-700 bg-surface-850 active:bg-surface-800',
                    )}
                  >
                    <Text className={cn('text-sm font-medium', active ? 'text-content-100' : 'text-content-300')}>
                      {formatExpiry(seconds)}
                    </Text>
                  </Pressable>
                );
              })}
            </View>
          </View>

          <SwitchRow
            label="Sound on settlement"
            description="Play a short cue when a trade settles."
            checked={settings.soundEnabled}
            onChange={settings.toggleSound}
          />
          <OneTapSwitch />
          <TradeModeControl />
        </View>
      </Panel>

      <Panel className="overflow-hidden">
        <View className="border-b border-surface-800 px-5 py-3">
          <Text accessibilityRole="header" className="text-sm font-semibold text-content-100">
            Market data health
          </Text>
          <Text className="text-[11px] text-content-400">
            When a feed is stale or offline, new trades on that asset are paused by the platform.
          </Text>
        </View>

        {healthQuery.isLoading ? (
          <View className="gap-2 p-4">
            {Array.from({ length: 4 }).map((_, index) => (
              <Skeleton key={index} className="h-10 w-full" />
            ))}
          </View>
        ) : (
          <View>
            {(healthQuery.data ?? []).map((feed, index) => (
              <View
                key={feed.assetId}
                className={cn('flex-row items-center gap-3 px-5 py-2.5', index > 0 && 'border-t border-surface-800/70')}
              >
                <Text numberOfLines={1} className="flex-1 text-sm font-medium text-content-100">
                  {feed.symbol}
                </Text>
                <Text className="text-xs text-content-400 tnum">{feed.lastPrice !== null ? feed.lastPrice : '--'}</Text>
                <FeedBadge status={feed.status} latencyMs={feed.totalLatencyMs} />
              </View>
            ))}
          </View>
        )}
      </Panel>
    </View>
  );
}
