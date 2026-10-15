import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Keyboard, Pressable, TextInput, View } from 'react-native';
import { Minus, Plus } from 'lucide-react-native';
import { Button, Icon, Sheet, Text } from '../../components/ui';
import { formatMoney, formatMoneyCompact } from '../../utils/format';
import { cn } from '../../utils/cn';
import { useTokenColor } from '../../utils/theme-colors';
import { DemoTag } from './tradeBits';
import type { AmountPreset } from './ticketModel';
import type { AccountType } from '../../types/api';

/**
 * Setting the investment (UX-08), from the ticket's Amount box (laid out the
 * way Binomo's mobile amount sheet is).
 *
 * The sheet only draws: the amount is the ticket's - its text, its limits,
 * its presets and what is wrong with it are all worked out there, exactly as
 * they were when the field sat in the ticket itself, and handed in. So the
 * box, this sheet and the request the ticket sends can never disagree.
 *
 * The field takes focus as the sheet settles, with what is in it selected, so
 * typing replaces the amount rather than appending to it.
 */
const FOCUS_DELAY_MS = 320;

interface Props {
  open: boolean;
  onClose: () => void;
  /** What is in the field, as typed. */
  text: string;
  onChangeText: (text: string) => void;
  currency: string;
  currencySymbol: string;
  /** The amount has a problem; the message says which. */
  invalid: boolean;
  canDecrease: boolean;
  canIncrease: boolean;
  onDecrease: () => void;
  onIncrease: () => void;
  presets: AmountPreset[];
  /** The valid amount, to mark the preset it matches. */
  stake: number | null;
  onPreset: (minor: number) => void;
  account: AccountType;
  available: number | null;
  /** Under the presets: what is wrong with the amount, or the range allowed. */
  message: ReactNode;
}

export function AmountSheet(props: Props) {
  const { open, onClose, account, available, currency } = props;

  function done() {
    Keyboard.dismiss();
    onClose();
  }

  return (
    <Sheet
      open={open}
      onClose={onClose}
      title="Amount"
      subtitle={
        <View className="mt-0.5 flex-row items-center gap-1">
          <DemoTag account={account} />
          <Text className="text-xs text-content-400">
            Available{' '}
            <Text className="text-xs font-semibold tnum text-content-200">
              {available !== null ? formatMoney(available, currency) : '--'}
            </Text>
          </Text>
        </View>
      }
      footer={
        <Button size="lg" onPress={done} textClassName="font-bold">
          Done
        </Button>
      }
    >
      <Body {...props} />
    </Sheet>
  );
}

/** Mounted with the sheet, so it can take focus as the sheet settles. */
function Body({
  text,
  onChangeText,
  currency,
  currencySymbol,
  invalid,
  canDecrease,
  canIncrease,
  onDecrease,
  onIncrease,
  presets,
  stake,
  onPreset,
  message,
}: Props) {
  const inputRef = useRef<TextInput | null>(null);
  const [focused, setFocused] = useState(false);
  const placeholder = useTokenColor('content-400');
  const selection = useTokenColor('accent');

  useEffect(() => {
    const timer = setTimeout(() => inputRef.current?.focus(), FOCUS_DELAY_MS);
    return () => clearTimeout(timer);
  }, []);

  return (
    <View className="px-4 pb-3 pt-1">
      <View
        className={cn(
          'h-16 flex-row items-center gap-1 rounded-xl border bg-surface-850 px-1.5',
          invalid ? 'border-down/60' : focused ? 'border-accent' : 'border-surface-700',
        )}
      >
        <StepButton label="Decrease investment" disabled={!canDecrease} onPress={onDecrease} icon={Minus} />
        {/* The symbol is its own element rather than part of the value: a
            currency prefix inside the field would be editable, and deleting
            it - or typing in front of it - would fight the caret on every
            keystroke. Tapping anywhere across the middle focuses the field. */}
        <Pressable
          onPress={() => inputRef.current?.focus()}
          accessible={false}
          className="min-w-0 flex-1 flex-row items-center justify-center gap-1"
        >
          <Text importantForAccessibility="no" className="text-3xl font-bold tnum text-content-100">
            {currencySymbol}
          </Text>
          <TextInput
            ref={inputRef}
            value={text}
            onChangeText={onChangeText}
            onFocus={() => setFocused(true)}
            onBlur={() => setFocused(false)}
            keyboardType="decimal-pad"
            autoComplete="off"
            autoCorrect={false}
            selectTextOnFocus
            accessibilityLabel="Investment amount"
            accessibilityHint={invalid ? 'The amount needs changing; the reason is under the field' : undefined}
            placeholder="0"
            placeholderTextColor={placeholder}
            selectionColor={selection}
            maxFontSizeMultiplier={1.25}
            className="min-w-[40px] max-w-[200px] p-0 text-left text-3xl font-bold text-content-100"
            style={{ fontVariant: ['tabular-nums'] }}
          />
        </Pressable>
        <StepButton label="Increase investment" disabled={!canIncrease} onPress={onIncrease} icon={Plus} />
      </View>

      {presets.length > 0 ? (
        <View className="mt-3 flex-row flex-wrap gap-2">
          {presets.map((preset) => {
            const chosen = stake === preset.value;
            return (
              <Pressable
                key={(preset.isMax ? 'max:' : '') + preset.value}
                onPress={() => onPreset(preset.value)}
                accessibilityRole="button"
                accessibilityState={{ selected: chosen }}
                accessibilityHint={
                  preset.isMax ? 'The most you can invest now: ' + formatMoney(preset.value, currency) : undefined
                }
                className={cn(
                  'min-w-[64px] items-center rounded-lg border px-3 py-2',
                  chosen ? 'border-accent/60 bg-accent/20' : 'border-surface-700 bg-surface-800 active:bg-surface-700',
                )}
              >
                <Text className={cn('text-xs font-semibold tnum', chosen ? 'text-content-100' : 'text-content-200')}>
                  {preset.isMax ? 'Max' : formatMoneyCompact(preset.value, currency)}
                </Text>
              </Pressable>
            );
          })}
        </View>
      ) : null}

      <View className="mt-2.5">{message}</View>
    </View>
  );
}

function StepButton({
  label,
  disabled,
  onPress,
  icon,
}: {
  label: string;
  disabled: boolean;
  onPress: () => void;
  icon: typeof Minus;
}) {
  return (
    <Pressable
      onPress={onPress}
      disabled={disabled}
      accessibilityRole="button"
      accessibilityLabel={label}
      accessibilityState={{ disabled }}
      // Unboxed: inside a field of its own, a bordered button competes with
      // the value it is there to nudge.
      className={cn('h-12 w-12 items-center justify-center rounded-lg active:bg-surface-800', disabled && 'opacity-30')}
    >
      <Icon as={icon} size={20} className="text-content-200" />
    </Pressable>
  );
}
