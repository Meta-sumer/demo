import { useMemo, useState } from 'react';
import { Pressable, ScrollView, View } from 'react-native';
import { Building2, ChevronRight, Smartphone } from 'lucide-react-native';
import { Icon, Text } from '../../components/ui';
import { cn } from '../../utils/cn';
import { formatMoney } from '../../utils/format';
import type { PaymentMethod } from '../../types/api';

/**
 * Deposit method picker.
 *
 * Categories in a strip across the top, the methods themselves under it.
 * Every entry here is a method an operator has actually configured in the
 * admin console - there are no placeholder rails, because a customer who
 * picks one that does not exist has paid into nothing.
 *
 * The minimum is shown on the card rather than discovered on submit: it is the
 * one constraint that decides whether a method is usable at all.
 */
const CATEGORIES = [
  {
    type: 'UPI' as const,
    label: 'UPI',
    hint: 'Instant, from any UPI app',
    icon: Smartphone,
    tone: 'bg-up-solid',
  },
  {
    type: 'BANK' as const,
    label: 'Bank transfer',
    hint: 'NEFT, IMPS or RTGS',
    icon: Building2,
    tone: 'bg-accent',
  },
];

interface Props {
  methods: PaymentMethod[];
  selectedKey: string | null;
  onSelect: (key: string) => void;
  loading?: boolean;
}

export function MethodPicker({ methods, selectedKey, onSelect, loading }: Props) {
  const available = useMemo(
    () => CATEGORIES.filter((category) => methods.some((method) => method.type === category.type)),
    [methods],
  );

  // Follow the selection, so opening the picker lands on the chosen method's
  // group rather than resetting to the first one.
  const selectedType = methods.find((method) => method.key === selectedKey)?.type ?? null;
  const [activeType, setActiveType] = useState<PaymentMethod['type'] | null>(null);
  const active = activeType ?? selectedType ?? available[0]?.type ?? null;

  if (loading) {
    return (
      <View className="rounded-xl border border-surface-700 bg-surface-850 px-4 py-8">
        <Text className="text-center text-xs text-content-400">Loading payment methods…</Text>
      </View>
    );
  }

  if (methods.length === 0) {
    return (
      <View className="rounded-xl border border-warn/30 bg-warn/10 px-4 py-3">
        <Text className="text-xs text-warn">
          No deposit method is configured yet. Please contact support before sending any money.
        </Text>
      </View>
    );
  }

  const inActive = methods.filter((method) => method.type === active);

  return (
    <View className="gap-3">
      {/* Categories, as a strip that scrolls sideways if it has to. */}
      <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerClassName="gap-2">
        {available.map((category) => {
          const count = methods.filter((method) => method.type === category.type).length;
          const isActive = active === category.type;

          return (
            <Pressable
              key={category.type}
              onPress={() => setActiveType(category.type)}
              accessibilityRole="button"
              accessibilityState={{ selected: isActive }}
              accessibilityLabel={category.label + ', ' + count + (count === 1 ? ' method' : ' methods')}
              className={cn(
                'min-w-[180px] shrink-0 gap-2 rounded-xl border p-3',
                isActive ? 'border-transparent bg-accent' : 'border-surface-700 bg-surface-850 active:border-surface-600',
              )}
            >
              <View
                className={cn(
                  'h-8 w-8 items-center justify-center rounded-lg',
                  isActive ? 'bg-white/20' : category.tone,
                )}
              >
                <Icon as={category.icon} size={16} className="text-white" />
              </View>
              <View>
                <Text
                  className={cn(
                    'text-sm font-bold uppercase tracking-wide',
                    isActive ? 'text-surface-950' : 'text-content-100',
                  )}
                >
                  {category.label}
                </Text>
                <Text className={cn('text-[11px]', isActive ? 'text-white/80' : 'text-content-400')}>
                  {count} {count === 1 ? 'method' : 'methods'}
                </Text>
              </View>
            </Pressable>
          );
        })}
      </ScrollView>

      {/* Methods in the active category. */}
      <View className="min-w-0">
        <Text accessibilityRole="header" className="mb-2 text-sm font-bold text-content-100">
          {CATEGORIES.find((category) => category.type === active)?.label}{' '}
          <Text className="text-sm font-normal text-content-400">({inActive.length})</Text>
        </Text>

        <View className="gap-2">
          {inActive.map((method) => {
            const isSelected = method.key === selectedKey;

            return (
              <Pressable
                key={method.key}
                onPress={() => onSelect(method.key)}
                accessibilityRole="radio"
                accessibilityState={{ checked: isSelected }}
                accessibilityLabel={method.label + ', minimum ' + formatMoney(method.minAmount)}
                className={cn(
                  'flex-row items-center gap-3 rounded-xl border px-3 py-2.5',
                  isSelected ? 'border-accent bg-accent/10' : 'border-surface-700 bg-surface-850 active:border-surface-600',
                )}
              >
                <View
                  className={cn(
                    'h-9 w-9 shrink-0 items-center justify-center rounded-lg',
                    method.type === 'UPI' ? 'bg-up-solid' : 'bg-accent',
                  )}
                >
                  <Text className="text-[11px] font-black uppercase text-white">{method.label.slice(0, 2)}</Text>
                </View>
                <View className="min-w-0 flex-1">
                  <Text numberOfLines={1} className="text-sm font-semibold text-content-100">
                    {method.label}
                  </Text>
                  <Text className="text-[11px] text-content-400 tnum">Min. {formatMoney(method.minAmount)}</Text>
                </View>
                <Icon as={ChevronRight} size={16} className={isSelected ? 'text-accent' : 'text-content-400'} />
              </Pressable>
            );
          })}
        </View>
      </View>
    </View>
  );
}
