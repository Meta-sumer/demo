import { useRef } from 'react';
import { ActivityIndicator, Pressable, View } from 'react-native';
import { Check } from 'lucide-react-native';
import { formatPrice } from '../../utils/format';
import { cn } from '../../utils/cn';
import { Icon, Sheet, Text } from '../../components/ui';
import { ContractInfo, type ContractSummary } from './ContractInfo';
import type { Direction } from '../../types/api';

/**
 * Confirmation (brief UX-11).
 *
 * The last look before money moves: the same contract information the ticket
 * shows, now with the direction filled in, and exactly two ways out - "Confirm
 * Trade" and "Edit". Nothing else is worded as an action.
 *
 * A bottom sheet. The veil is light on purpose: the order preview is on the
 * chart behind it, and seeing where the trade would sit is half of confirming
 * it. A tap on the veil, a drag down or the back gesture is Edit - never while
 * a request is in flight.
 *
 * The sheet does not send anything itself. It reports Confirm and Edit to the
 * ticket, which owns the one mutation path - fresh idempotency key, the price
 * on screen at the moment of the press, and the re-quote when the price moved.
 */
const VEIL_ALPHA = 0.3;

interface Props {
  direction: Direction | null;
  summary: ContractSummary;
  /** A request is in flight. */
  pending: boolean;
  /** Why the trade cannot be sent right now (market closed, prices disconnected...). */
  blockedReason: string | null;
  /** The server refused because the price moved; Confirm will use this price. */
  reQuotePrice: number | null;
  pricePrecision: number;
  /** A refusal other than a price move, in the server's words. */
  error: string | null;
  dontAskAgain: boolean;
  onDontAskAgainChange: (value: boolean) => void;
  onConfirm: () => void;
  onEdit: () => void;
}

export function ConfirmTradeSheet({
  direction,
  summary,
  pending,
  blockedReason,
  reQuotePrice,
  pricePrecision,
  error,
  dontAskAgain,
  onDontAskAgainChange,
  onConfirm,
  onEdit,
}: Props) {
  const open = direction !== null;
  // The last direction shown, so the sheet does not go blank while it slides out.
  const shownRef = useRef<Direction>('UP');
  if (direction) shownRef.current = direction;
  const shown = shownRef.current;
  const up = shown === 'UP';
  const confirmDisabled = pending || !!blockedReason;

  return (
    <Sheet
      open={open}
      onClose={() => (pending ? undefined : onEdit())}
      title={'Confirm your ' + shown + ' trade'}
      heading={
        <Text className="text-base font-bold text-content-100">
          Confirm your <Text className={cn('text-base font-bold', up ? 'text-up' : 'text-down')}>{shown}</Text> trade
        </Text>
      }
      subtitle={
        up
          ? 'You win if the price at expiry is above the entry price.'
          : 'You win if the price at expiry is below the entry price.'
      }
      scrimAlpha={VEIL_ALPHA}
      bodyClassName="px-4 pb-3 pt-1"
      footer={
        <View className="flex-row gap-2">
          <Pressable
            onPress={onConfirm}
            disabled={confirmDisabled}
            accessibilityRole="button"
            accessibilityState={{ disabled: confirmDisabled, busy: pending }}
            className={cn(
              'h-12 flex-1 flex-row items-center justify-center gap-2 rounded-xl active:opacity-90',
              up ? 'bg-up-solid' : 'bg-down-solid',
              confirmDisabled && 'opacity-40',
            )}
          >
            {pending ? <ActivityIndicator size="small" color="#fff" /> : null}
            <Text className="text-sm font-bold text-white">Confirm Trade</Text>
          </Pressable>
          <Pressable
            onPress={onEdit}
            disabled={pending}
            accessibilityRole="button"
            accessibilityState={{ disabled: pending }}
            className={cn(
              'h-12 items-center justify-center rounded-xl border border-surface-700 bg-surface-850 px-5 active:bg-surface-800',
              pending && 'opacity-40',
            )}
          >
            <Text className="text-sm font-semibold text-content-200">Edit</Text>
          </Pressable>
        </View>
      }
    >
      <ContractInfo summary={{ ...summary, direction: shown }} variant="confirm" />

      {reQuotePrice !== null ? (
        <View accessibilityRole="alert" className="mt-3 rounded-lg border border-warn/40 bg-warn/10 px-3 py-2">
          <Text className="text-xs font-semibold text-warn">The price moved before your order landed</Text>
          <Text className="mt-0.5 text-xs text-content-300">
            Nothing was placed. {shown} is now at{' '}
            <Text className="text-xs font-bold tnum text-content-100">{formatPrice(reQuotePrice, pricePrecision)}</Text>.
            Confirm Trade places it at this price.
          </Text>
        </View>
      ) : null}

      {error ? (
        <View accessibilityRole="alert" className="mt-3 rounded-lg border border-down/30 bg-down/10 px-3 py-2">
          <Text className="text-xs text-down">Trade not placed: {error}</Text>
        </View>
      ) : null}

      {blockedReason ? (
        <View className="mt-3 rounded-lg border border-down/30 bg-down/10 px-3 py-2">
          <Text className="text-xs font-medium text-down">{blockedReason}</Text>
        </View>
      ) : null}

      <Pressable
        onPress={() => onDontAskAgainChange(!dontAskAgain)}
        accessibilityRole="checkbox"
        accessibilityState={{ checked: dontAskAgain }}
        className="mt-3 flex-row items-center gap-2 self-start py-1"
      >
        <View
          className={cn(
            'h-4 w-4 items-center justify-center rounded border',
            dontAskAgain ? 'border-accent-solid bg-accent-solid' : 'border-surface-600 bg-surface-850',
          )}
        >
          {dontAskAgain ? <Icon as={Check} size={12} strokeWidth={3} className="text-surface-950" /> : null}
        </View>
        <Text className="text-xs text-content-300">{"Don't ask again (one-tap trading)"}</Text>
      </Pressable>
    </Sheet>
  );
}
