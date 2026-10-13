import { useEffect, useState } from 'react';
import { Pressable, View } from 'react-native';
import { useSettingsStore, type TradeMode } from '../stores/settingsStore';
import { fromMinor, toMinor } from '../utils/format';
import { cn } from '../utils/cn';
import { Switch, Text } from './ui';

/**
 * The two trading preferences, as one set of controls.
 *
 * They appear in Account → Preferences, on the Settings screen and in the
 * settings sheet. Rendering the same components in all three keeps the names
 * and the explanations identical, so "one-tap trading" never means something
 * slightly different depending on where it was switched.
 */

/** On = trades are placed without the confirmation step (`confirmBeforeTrade` off). */
export function OneTapSwitch({ className }: { className?: string }) {
  const confirmBeforeTrade = useSettingsStore((s) => s.confirmBeforeTrade);
  const toggleConfirm = useSettingsStore((s) => s.toggleConfirm);
  const on = !confirmBeforeTrade;

  return (
    <SwitchRow
      className={className}
      label="One-tap trading"
      description={
        on
          ? 'On: pressing UP or DOWN places the trade immediately, without the review step.'
          : 'Off: you review every trade and press Confirm Trade before it is placed.'
      }
      checked={on}
      onChange={toggleConfirm}
    />
  );
}

const MODES: Array<{ value: TradeMode; label: string; hint: string }> = [
  { value: 'beginner', label: 'Beginner', hint: 'One market, the essential controls' },
  { value: 'advanced', label: 'Advanced', hint: 'Market tabs, indicators, drawing tools' },
];

export function TradeModeControl({ className }: { className?: string }) {
  const tradeMode = useSettingsStore((s) => s.tradeMode);
  const setTradeMode = useSettingsStore((s) => s.setTradeMode);

  return (
    <View className={className}>
      <Text className="mb-1.5 text-sm font-medium text-content-100">Default trading mode</Text>
      <View accessibilityRole="radiogroup" accessibilityLabel="Default trading mode" className="flex-row gap-1.5">
        {MODES.map((mode) => {
          const selected = tradeMode === mode.value;
          return (
            <Pressable
              key={mode.value}
              accessibilityRole="radio"
              accessibilityState={{ checked: selected }}
              onPress={() => setTradeMode(mode.value)}
              className={cn(
                'flex-1 rounded-lg border px-3 py-2',
                selected ? 'border-accent bg-accent/10' : 'border-surface-700 bg-surface-850 active:border-surface-600',
              )}
            >
              <Text className="text-sm font-semibold text-content-100">{mode.label}</Text>
              <Text className="text-[11px] leading-4 text-content-400">{mode.hint}</Text>
            </Pressable>
          );
        })}
      </View>
    </View>
  );
}

export function SwitchRow({
  label,
  description,
  checked,
  onChange,
  className,
}: {
  label: string;
  description: string;
  checked: boolean;
  onChange: () => void;
  className?: string;
}) {
  return (
    <Pressable
      onPress={onChange}
      accessibilityRole="switch"
      accessibilityState={{ checked }}
      accessibilityLabel={label}
      className={cn(
        'flex-row items-center gap-4 rounded-lg border border-surface-700 bg-surface-850 px-4 py-3',
        className,
      )}
    >
      <View className="min-w-0 flex-1">
        <Text className="text-sm font-medium text-content-100">{label}</Text>
        <Text className="text-[11px] leading-4 text-content-400">{description}</Text>
      </View>
      <Switch value={checked} onValueChange={() => onChange()} accessibilityLabel={label} />
    </Pressable>
  );
}

/**
 * The default stake as text being typed.
 *
 * The store holds minor units; the field shows major ones. Typing "5." must
 * not be rewritten to "5" before the next digit arrives, so the field keeps
 * its own text and only follows the store when the stake changes from
 * somewhere else (the settings sheet and the Settings screen edit the same
 * value).
 */
export function useDefaultStakeDraft(): [string, (text: string) => void] {
  const defaultStake = useSettingsStore((s) => s.defaultStake);
  const setDefaultStake = useSettingsStore((s) => s.setDefaultStake);
  const [text, setText] = useState(() => String(fromMinor(defaultStake)));

  useEffect(() => {
    setText((current) => {
      const parsed = Number(current.replace(/[^0-9.]/g, ''));
      return Number.isFinite(parsed) && toMinor(parsed) === defaultStake ? current : String(fromMinor(defaultStake));
    });
  }, [defaultStake]);

  function onChangeText(value: string) {
    setText(value);
    const parsed = Number(value.replace(/[^0-9.]/g, ''));
    if (Number.isFinite(parsed)) setDefaultStake(toMinor(parsed));
  }

  return [text, onChangeText];
}
