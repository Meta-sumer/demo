import { useMemo } from 'react';
import { Pressable, ScrollView, View } from 'react-native';
import { Sheet, Text } from '../../components/ui';
import { useNow } from '../../hooks/useCountdown';
import { cn } from '../../utils/cn';
import { useClockFormatter } from './tradeBits';
import { durationWords, expiryGroups, type DurationChip } from './ticketModel';

/**
 * Every duration an asset offers, as the clock time the trade would end at
 * (reference record 10; laid out the way Binomo's mobile "Time" sheet is).
 *
 * Sections by length - up to five minutes, up to an hour, longer - each a row
 * of choices that scrolls sideways when it runs out of width. A choice reads
 * as its end time, with its duration and payout under it: the payout is part
 * of the choice, since a longer duration usually pays differently.
 *
 * The end times are live. This platform's expiry is relative - the trade runs
 * for the duration from the moment the platform accepts it, not to a fixed
 * minute mark - so each time is "now + duration", to the second, ticking with
 * the clock, in the zone the trader chose for the chart. What the trader reads
 * is what the trade will use, give or take the moment it takes to accept it,
 * and the note under the sections says so.
 *
 * A bottom sheet, rising from the ticket's Time box.
 */
interface Props {
  open: boolean;
  onClose: () => void;
  chips: DurationChip[];
  selected: number;
  onSelect: (seconds: number) => void;
}

export function TimeSheet({ open, onClose, chips, selected, onSelect }: Props) {
  return (
    <Sheet open={open} onClose={onClose} title="Time">
      <Options
        chips={chips}
        selected={selected}
        onSelect={(seconds) => {
          onSelect(seconds);
          onClose();
        }}
      />
    </Sheet>
  );
}

/**
 * The sections themselves. Mounted only while the sheet is, so the one-second
 * clock the end times need is not running behind a closed sheet.
 */
function Options({
  chips,
  selected,
  onSelect,
}: {
  chips: DurationChip[];
  selected: number;
  onSelect: (seconds: number) => void;
}) {
  const now = useNow(1000);
  const clock = useClockFormatter();
  const groups = useMemo(() => expiryGroups(chips), [chips]);

  if (chips.length === 0) {
    return <Text className="px-4 pb-4 text-xs text-content-400">No durations available for this asset.</Text>;
  }

  return (
    <View className="gap-4 pb-4 pt-1">
      {groups.map((group) => (
        <View key={group.key} accessibilityLabel={group.label}>
          <Text accessibilityRole="header" className="mb-2 px-4 text-xs font-medium text-content-400">
            {group.label}
          </Text>
          <ScrollView
            horizontal
            showsHorizontalScrollIndicator={false}
            contentContainerClassName="gap-2 px-4"
            keyboardShouldPersistTaps="handled"
          >
            {group.chips.map((chip) => {
              const isSelected = chip.seconds === selected;
              const unavailable = chip.unavailableReason !== null;
              const endsAt = clock(now + chip.seconds * 1000);
              const words = durationWords(chip.seconds);
              const payout = chip.payoutPercent !== null ? chip.payoutPercent + '%' : 'N/A';
              return (
                <Pressable
                  key={chip.seconds}
                  disabled={unavailable}
                  onPress={() => onSelect(chip.seconds)}
                  accessibilityRole="button"
                  accessibilityState={{ selected: isSelected, disabled: unavailable }}
                  accessibilityLabel={'Ends at ' + endsAt + ', ' + words + ', ' + payout + ' payout'}
                  accessibilityHint={chip.unavailableReason ?? undefined}
                  className={cn(
                    'min-w-[100px] items-center rounded-xl border px-3 py-2.5',
                    isSelected
                      ? 'border-accent-solid bg-accent-solid'
                      : 'border-surface-700 bg-surface-800 active:bg-surface-700',
                    unavailable && 'opacity-40',
                  )}
                >
                  <Text
                    className={cn('text-base font-bold tnum', isSelected ? 'text-surface-950' : 'text-content-100')}
                  >
                    {endsAt}
                  </Text>
                  <Text
                    className={cn(
                      'mt-0.5 text-[11px] font-medium tnum',
                      isSelected ? 'text-surface-950' : 'text-content-400',
                    )}
                  >
                    {words} ·{' '}
                    <Text
                      className={cn(
                        'text-[11px] font-semibold tnum',
                        isSelected ? 'text-surface-950' : unavailable ? 'text-content-400' : 'text-up',
                      )}
                    >
                      {payout}
                    </Text>
                  </Text>
                </Pressable>
              );
            })}
          </ScrollView>
        </View>
      ))}

      <View className="gap-1 px-4">
        {chips.some((chip) => chip.unavailableReason) ? (
          <Text className="text-[11px] text-content-400">Greyed durations have no payout on this asset.</Text>
        ) : null}
        <Text className="text-[11px] leading-snug text-content-400">
          Counted from now. The exact expiry is set when your trade is accepted.
        </Text>
        <Text className="text-[11px] leading-snug text-content-400">
          The payout is fixed when your trade is accepted.
        </Text>
      </View>
    </View>
  );
}
