import { useEffect, useMemo, useState } from 'react';
import { Pressable, ScrollView, View } from 'react-native';
import { Check, Plus, Search, X } from 'lucide-react-native';
import { Field, Icon, Modal, Text } from '../../../components/ui';
import { cn } from '../../../utils/cn';
import { useSettingsStore, type Indicator } from '../../../stores/settingsStore';
import {
  INDICATOR_CATALOG,
  INDICATOR_CATEGORIES,
  INDICATOR_ORDER,
  searchIndicators,
  type IndicatorCategory,
  type IndicatorInfo,
} from './indicatorCatalog';

/**
 * The indicator picker (record 09), modelled on Deriv's: sections - what is
 * on the chart now, then Trend, Momentum and Volatility - with a search
 * across all of them.
 *
 * "Active" comes first and is where the picker opens when anything is on: the
 * commonest reason to open it again is to take something off. Every row says
 * what the indicator measures in a sentence, because a name like "Stochastic"
 * tells a trader who has not met it nothing.
 *
 * A dialog rather than a popover: six rows with a description each do not fit
 * above a toolbar on a phone, and the shared dialog is a tall bottom sheet.
 */
type Section = 'active' | IndicatorCategory;

export function IndicatorPicker({ open, onClose }: { open: boolean; onClose: () => void }) {
  const active = useSettingsStore((s) => s.indicators);
  const toggle = useSettingsStore((s) => s.toggleIndicator);
  const remove = useSettingsStore((s) => s.removeIndicator);
  const clear = useSettingsStore((s) => s.clearIndicators);

  const [section, setSection] = useState<Section>('trend');
  const [query, setQuery] = useState('');

  // Each opening starts where it is most useful, with the search cleared.
  useEffect(() => {
    if (!open) return;
    setQuery('');
    setSection(useSettingsStore.getState().indicators.length > 0 ? 'active' : 'trend');
  }, [open]);

  const activeInOrder = useMemo(() => INDICATOR_ORDER.filter((id) => active.includes(id)), [active]);
  const results = useMemo(() => searchIndicators(query), [query]);
  const searching = query.trim() !== '';

  const categoryItems =
    section === 'active'
      ? []
      : INDICATOR_ORDER.map((id) => INDICATOR_CATALOG[id]).filter((info) => info.category === section);

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Indicators"
      subtitle="Drawn from the candles on screen. Display only - they never change an order."
      toolbar={
        <View className="gap-3">
          {/* A row of chips on a phone. */}
          <ScrollView
            horizontal
            showsHorizontalScrollIndicator={false}
            accessibilityLabel="Indicator sections"
            contentContainerClassName="gap-1"
            keyboardShouldPersistTaps="handled"
          >
            <SectionButton
              label="Active"
              count={active.length}
              selected={!searching && section === 'active'}
              onPress={() => {
                setQuery('');
                setSection('active');
              }}
            />
            {INDICATOR_CATEGORIES.map((category) => (
              <SectionButton
                key={category.id}
                label={category.label}
                selected={!searching && section === category.id}
                onPress={() => {
                  setQuery('');
                  setSection(category.id);
                }}
              />
            ))}
          </ScrollView>

          <Field
            value={query}
            onChangeText={setQuery}
            placeholder="Search, e.g. RSI or moving average"
            accessibilityLabel="Search indicators"
            autoCorrect={false}
            autoCapitalize="none"
            returnKeyType="search"
            left={<Icon as={Search} size={16} className="text-content-400" />}
          />
        </View>
      }
    >
      <View className="min-h-[220px]">
        {searching ? (
          results.length > 0 ? (
            <IndicatorList items={results} active={active} onToggle={toggle} />
          ) : (
            <Text className="px-1 py-8 text-center text-sm text-content-400">No indicator matches "{query.trim()}".</Text>
          )
        ) : section === 'active' ? (
          activeInOrder.length > 0 ? (
            <>
              <View className="gap-1.5">
                {activeInOrder.map((id) => (
                  <ActiveRow key={id} info={INDICATOR_CATALOG[id]} onRemove={() => remove(id)} />
                ))}
              </View>
              <Pressable
                onPress={clear}
                accessibilityRole="button"
                className="mt-3 h-9 justify-center self-start rounded-lg px-2 active:bg-surface-850"
              >
                <Text className="text-xs font-semibold text-content-400">Remove all</Text>
              </Pressable>
            </>
          ) : (
            <View className="items-center gap-1 px-4 py-10">
              <Text className="text-center text-sm font-medium text-content-200">Nothing on the chart yet</Text>
              <Text className="max-w-xs text-center text-xs text-content-400">
                Add one from Trend, Momentum or Volatility, or search for it by name.
              </Text>
            </View>
          )
        ) : (
          <>
            <Text className="mb-2 px-1 text-[11px] text-content-400">
              {INDICATOR_CATEGORIES.find((category) => category.id === section)?.hint}
            </Text>
            <IndicatorList items={categoryItems} active={active} onToggle={toggle} />
          </>
        )}
      </View>
    </Modal>
  );
}

function SectionButton({
  label,
  count,
  selected,
  onPress,
}: {
  label: string;
  count?: number;
  selected: boolean;
  onPress: () => void;
}) {
  return (
    <Pressable
      onPress={onPress}
      accessibilityRole="tab"
      accessibilityState={{ selected }}
      className={cn(
        'h-9 flex-row items-center justify-between gap-2 rounded-lg px-3',
        selected ? 'bg-surface-800' : 'active:bg-surface-850',
      )}
    >
      <Text className={cn('text-sm font-semibold', selected ? 'text-content-100' : 'text-content-400')}>{label}</Text>
      {count !== undefined ? (
        <View
          className={cn('h-5 min-w-[20px] items-center justify-center rounded-full px-1.5', count > 0 ? 'bg-accent' : 'bg-surface-800')}
        >
          <Text className={cn('text-[10px] font-bold tnum', count > 0 ? 'text-surface-950' : 'text-content-400')}>{count}</Text>
        </View>
      ) : null}
    </Pressable>
  );
}

function IndicatorList({
  items,
  active,
  onToggle,
}: {
  items: IndicatorInfo[];
  active: Indicator[];
  onToggle: (id: Indicator) => void;
}) {
  return (
    <View className="gap-1.5">
      {items.map((info) => {
        const on = active.includes(info.id);
        return (
          <Pressable
            key={info.id}
            onPress={() => onToggle(info.id)}
            accessibilityRole="button"
            accessibilityState={{ selected: on }}
            accessibilityLabel={(on ? 'Remove ' : 'Add ') + info.name}
            className={cn(
              'flex-row items-center gap-3 rounded-xl border px-3 py-2.5',
              on ? 'border-accent/50 bg-accent/10' : 'border-surface-800 active:bg-surface-850',
            )}
          >
            <Abbreviation info={info} />
            <View className="min-w-0 flex-1">
              <View className="flex-row flex-wrap items-baseline gap-x-1.5">
                <Text className="text-sm font-semibold text-content-100">{info.name}</Text>
                <Text className="text-[11px] font-medium text-content-400 tnum">{info.params}</Text>
              </View>
              <Text className="mt-0.5 text-[11px] leading-snug text-content-400">{info.description}</Text>
            </View>
            <View className={cn('h-8 flex-row items-center gap-1 rounded-lg px-2.5', on ? '' : 'bg-surface-800')}>
              <Icon as={on ? Check : Plus} size={14} strokeWidth={3} className={on ? 'text-accent' : 'text-content-200'} />
              <Text className={cn('text-[11px] font-bold', on ? 'text-accent' : 'text-content-200')}>{on ? 'Added' : 'Add'}</Text>
            </View>
          </Pressable>
        );
      })}
    </View>
  );
}

function ActiveRow({ info, onRemove }: { info: IndicatorInfo; onRemove: () => void }) {
  return (
    <View className="flex-row items-center gap-3 rounded-xl border border-surface-800 bg-surface-850/50 px-3 py-2">
      <Abbreviation info={info} />
      <View className="min-w-0 flex-1">
        <View className="flex-row flex-wrap items-baseline gap-x-1.5">
          <Text className="text-sm font-semibold text-content-100">{info.name}</Text>
          <Text className="text-[11px] font-medium text-content-400 tnum">{info.params}</Text>
        </View>
        <Text className="text-[11px] text-content-400">
          {info.placement === 'overlay' ? 'On the price' : 'In its own band under the price'}
        </Text>
      </View>
      <Pressable
        onPress={onRemove}
        accessibilityRole="button"
        accessibilityLabel={'Remove ' + info.name}
        className="h-9 w-9 items-center justify-center rounded-lg active:bg-surface-800"
      >
        <Icon as={X} size={16} className="text-content-400" />
      </Pressable>
    </View>
  );
}

function Abbreviation({ info }: { info: IndicatorInfo }) {
  return (
    <View className="h-9 w-12 items-center justify-center rounded-lg bg-surface-800">
      <Text className="text-[10px] font-black uppercase tracking-wide text-content-200">{info.short}</Text>
    </View>
  );
}
