import { useEffect, useMemo, useState } from 'react';
import { Pressable, View } from 'react-native';
import { CalendarDays, ChevronLeft, ChevronRight } from 'lucide-react-native';
import { cn } from '../../utils/cn';
import { dayKey } from '../../utils/dayGroups';
import { Button, Icon, IconButton, Label, Sheet, Text } from '../../components/ui';
import { useTimeZone } from '../trade/tradeBits';

/**
 * The Trades filter's date range - the phone form of the web's two
 * `<input type="date">` boxes.
 *
 * Values are plain calendar days ("2026-10-12"), exactly what a date input
 * gives, so the page widens them to whole days the same way it always has.
 * Each box opens a month calendar in a sheet; the start cannot be after the
 * end and the end cannot be before the start, as the inputs' min and max
 * enforced.
 */
export interface DateRange {
  from: string;
  to: string;
}

type Edge = 'from' | 'to';

export function DateRangeField({
  value,
  onChange,
  className,
}: {
  value: DateRange;
  onChange: (range: DateRange) => void;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  // Kept apart from `open` so the sheet keeps its title while it slides away.
  const [edge, setEdge] = useState<Edge>('from');

  function edit(next: Edge) {
    setEdge(next);
    setOpen(true);
  }

  function set(day: string) {
    onChange(edge === 'from' ? { ...value, from: day } : { ...value, to: day });
    setOpen(false);
  }

  return (
    <View className={cn('min-w-0', className)}>
      <Label>Date range</Label>
      <View className="flex-row items-center gap-1.5">
        <DateBox label="From date" value={value.from} onPress={() => edit('from')} />
        <Text className="text-content-400">–</Text>
        <DateBox label="To date" value={value.to} onPress={() => edit('to')} />
      </View>

      <CalendarSheet
        open={open}
        onClose={() => setOpen(false)}
        title={edge === 'from' ? 'From date' : 'To date'}
        value={edge === 'from' ? value.from : value.to}
        min={edge === 'to' ? value.from || undefined : undefined}
        max={edge === 'from' ? value.to || undefined : undefined}
        onPick={set}
        onClear={() => set('')}
      />
    </View>
  );
}

function DateBox({ label, value, onPress }: { label: string; value: string; onPress: () => void }) {
  return (
    <Pressable
      onPress={onPress}
      accessibilityRole="button"
      accessibilityLabel={label + ': ' + (value ? formatDay(value) : 'not set')}
      className="min-h-[44px] min-w-0 flex-1 flex-row items-center gap-2 rounded-lg border border-surface-700 bg-surface-850 px-3 active:border-surface-600"
    >
      <Icon as={CalendarDays} size={15} className="text-content-400" />
      <Text numberOfLines={1} className={cn('min-w-0 flex-1 text-sm tnum', value ? 'text-content-100' : 'text-content-400')}>
        {value ? formatDay(value) : label}
      </Text>
    </Pressable>
  );
}

const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];

function CalendarSheet({
  open,
  onClose,
  title,
  value,
  min,
  max,
  onPick,
  onClear,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  value: string;
  min?: string;
  max?: string;
  onPick: (day: string) => void;
  onClear: () => void;
}) {
  const zone = useTimeZone();
  const today = dayKey(Date.now(), zone);
  const [month, setMonth] = useState(() => (value || today).slice(0, 7));

  // Open on the month of the chosen day, else the nearest bound, else this month.
  useEffect(() => {
    if (open) setMonth((value || max || min || today).slice(0, 7));
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  const weeks = useMemo(() => monthWeeks(month), [month]);

  return (
    <Sheet
      open={open}
      onClose={onClose}
      title={title}
      footer={
        value ? (
          <Button variant="ghost" onPress={onClear}>
            Clear date
          </Button>
        ) : undefined
      }
    >
      <View className="px-3 pb-3">
        <View className="flex-row items-center justify-between pb-2">
          <IconButton icon={ChevronLeft} accessibilityLabel="Previous month" onPress={() => setMonth(shiftMonth(month, -1))} />
          <Text className="text-sm font-semibold text-content-100">{monthLabel(month)}</Text>
          <IconButton icon={ChevronRight} accessibilityLabel="Next month" onPress={() => setMonth(shiftMonth(month, 1))} />
        </View>

        <View className="flex-row">
          {WEEKDAYS.map((day) => (
            <Text key={day} className="flex-1 pb-1 text-center text-[10px] font-medium uppercase text-content-400">
              {day}
            </Text>
          ))}
        </View>

        {weeks.map((week, row) => (
          <View key={row} className="flex-row">
            {week.map((day, column) => {
              if (!day) return <View key={column} className="flex-1" />;
              const selected = day === value;
              const disabled = (!!min && day < min) || (!!max && day > max);
              return (
                <View key={column} className="flex-1 items-center py-0.5">
                  <Pressable
                    onPress={() => onPick(day)}
                    disabled={disabled}
                    accessibilityRole="button"
                    accessibilityLabel={formatDay(day)}
                    accessibilityState={{ selected, disabled }}
                    className={cn(
                      'h-10 w-10 items-center justify-center rounded-full',
                      selected ? 'bg-accent-solid' : day === today ? 'border border-accent/60' : 'active:bg-surface-850',
                      disabled && 'opacity-30',
                    )}
                  >
                    <Text
                      className={cn(
                        'text-sm tnum',
                        selected ? 'font-bold text-surface-950' : day === today ? 'font-semibold text-accent' : 'text-content-100',
                      )}
                    >
                      {Number(day.slice(8))}
                    </Text>
                  </Pressable>
                </View>
              );
            })}
          </View>
        ))}
      </View>
    </Sheet>
  );
}

/* ------------------------------------------------------- calendar arithmetic */

/** Calendar keys are read as UTC dates, so no zone or daylight-saving shift can move a day. */
function keyDate(key: string): Date {
  const [year, month, day] = key.split('-').map(Number) as [number, number, number];
  return new Date(Date.UTC(year, month - 1, day ?? 1));
}

/** "12 Oct 2026". */
function formatDay(key: string): string {
  return new Intl.DateTimeFormat('en-GB', { timeZone: 'UTC', day: 'numeric', month: 'short', year: 'numeric' }).format(
    keyDate(key),
  );
}

/** "October 2026" for "2026-10". */
function monthLabel(month: string): string {
  return new Intl.DateTimeFormat('en-GB', { timeZone: 'UTC', month: 'long', year: 'numeric' }).format(
    keyDate(month + '-01'),
  );
}

function shiftMonth(month: string, by: number): string {
  const date = keyDate(month + '-01');
  date.setUTCMonth(date.getUTCMonth() + by);
  return date.toISOString().slice(0, 7);
}

/** The month as weeks from Monday, with nulls before the 1st and after the last day. */
function monthWeeks(month: string): Array<Array<string | null>> {
  const first = keyDate(month + '-01');
  const lead = (first.getUTCDay() + 6) % 7;
  const length = new Date(Date.UTC(first.getUTCFullYear(), first.getUTCMonth() + 1, 0)).getUTCDate();
  const cells: Array<string | null> = Array.from({ length: lead }, () => null);
  for (let day = 1; day <= length; day += 1) cells.push(month + '-' + String(day).padStart(2, '0'));
  while (cells.length % 7 !== 0) cells.push(null);
  const weeks: Array<Array<string | null>> = [];
  for (let start = 0; start < cells.length; start += 7) weeks.push(cells.slice(start, start + 7));
  return weeks;
}
