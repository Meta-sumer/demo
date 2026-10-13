import { useState } from 'react';
import { Pressable, View } from 'react-native';
import { Segmented, Sheet, Text } from '../../components/ui';
import { useNow } from '../../hooks/useCountdown';
import { cn } from '../../utils/cn';
import { useClockFormatter } from './tradeBits';
import type { DurationChip } from './ticketModel';

/**
 * Every duration an asset offers (reference record 10).
 *
 * The ticket's row carries four chips; this is behind its "More" chip. Each
 * option shows its payout, because the payout is part of the choice - a longer
 * duration usually pays differently. The "Duration | Ends at" switch is
 * Quotex's SWITCH TIME: the same options written as the clock time the trade
 * would end if it were accepted now, ticking with the clock, for the trader
 * who thinks "until 14:30" rather than "for 15 minutes". It changes how the
 * options are written, never what they are: the trade still runs for the
 * duration from the moment the platform accepts it.
 *
 * A bottom sheet, rising from the ticket that opened it.
 */

type Mode = 'duration' | 'endsAt';

/** Remembered while the app is open: a trader who reads end times keeps them. */
let lastMode: Mode = 'duration';

const MODES = [
  { value: 'duration', label: 'Duration' },
  { value: 'endsAt', label: 'Ends at' },
] as const;

interface Props {
  open: boolean;
  onClose: () => void;
  chips: DurationChip[];
  selected: number;
  onSelect: (seconds: number) => void;
}

export function DurationSheet({ open, onClose, chips, selected, onSelect }: Props) {
  const [mode, setModeState] = useState<Mode>(lastMode);

  function setMode(next: Mode) {
    lastMode = next;
    setModeState(next);
  }

  return (
    <Sheet
      open={open}
      onClose={onClose}
      title="Duration"
      subtitle="Every duration this market offers, with its payout."
      toolbar={<Segmented options={MODES} value={mode} onChange={setMode} accessibilityLabel="Show as" />}
    >
      <Options
        chips={chips}
        selected={selected}
        mode={mode}
        onSelect={(seconds) => {
          onSelect(seconds);
          onClose();
        }}
      />
    </Sheet>
  );
}

/**
 * The grid itself. Mounted only while the sheet is, so the one-second clock
 * the end times need is not running behind a closed sheet.
 */
function Options({
  chips,
  selected,
  mode,
  onSelect,
}: {
  chips: DurationChip[];
  selected: number;
  mode: Mode;
  onSelect: (seconds: number) => void;
}) {
  const now = useNow(1000);
  const clock = useClockFormatter();
  const endsAt = mode === 'endsAt';

  return (
    <View className="px-4 pb-4">
      <View className="-m-[3px] flex-row flex-wrap">
        {chips.map((chip) => {
          const isSelected = chip.seconds === selected;
          const unavailable = chip.unavailableReason !== null;
          const payout = chip.payoutPercent !== null ? chip.payoutPercent + '%' : 'N/A';
          return (
            <View key={chip.seconds} className="w-1/3 p-[3px]">
              <Pressable
                disabled={unavailable}
                onPress={() => onSelect(chip.seconds)}
                accessibilityRole="button"
                accessibilityState={{ selected: isSelected, disabled: unavailable }}
                accessibilityLabel={chip.label + ', ' + payout + ' payout'}
                accessibilityHint={chip.unavailableReason ?? undefined}
                className={cn(
                  'w-full items-center rounded-lg border px-1 py-2',
                  isSelected ? 'border-accent bg-accent/15' : 'border-surface-700 bg-surface-850 active:border-surface-600',
                  unavailable && 'opacity-40',
                )}
              >
                <Text className={cn('text-sm font-bold leading-tight tnum', isSelected ? 'text-content-100' : 'text-content-200')}>
                  {endsAt ? clock(now + chip.seconds * 1000) : chip.label}
                </Text>
                <Text
                  className={cn(
                    'mt-0.5 text-[10px] font-semibold leading-tight tnum',
                    unavailable ? 'text-content-400' : isSelected ? 'text-accent' : 'text-up',
                  )}
                >
                  {endsAt ? <Text className="text-[10px] font-medium text-content-400">{chip.label} · </Text> : null}
                  {payout}
                </Text>
              </Pressable>
            </View>
          );
        })}
      </View>
      <Text className="mt-3 text-[11px] leading-snug text-content-400">
        {endsAt
          ? 'Counted from now. The exact expiry is set when your trade is accepted.'
          : 'The payout is fixed when your trade is accepted.'}
      </Text>
    </View>
  );
}
