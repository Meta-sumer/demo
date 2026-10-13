import { useEffect, useState, type ComponentType, type ReactNode } from 'react';
import { Pressable, View } from 'react-native';
import { Check, ChevronDown, Globe, Minus, Monitor, Moon, Plus, RotateCcw, Sun, SunMoon } from 'lucide-react-native';
import {
  GRID_OPACITY_MAX,
  timeframeLabel,
  TIMEFRAMES,
  useSettingsStore,
  type ChartType,
} from '../stores/settingsStore';
import { useThemeStore, type ThemePreference } from '../stores/themeStore';
import { useWorkspaceStore } from '../stores/workspaceStore';
import { useChartPalette } from '../utils/theme-colors';
import { TIME_ZONES, zoneLabel } from '../utils/time';
import { formatExpiry, formatMoney } from '../utils/format';
import { cn } from '../utils/cn';
import { OneTapSwitch, TradeModeControl, useDefaultStakeDraft } from './TradingPreferences';
import { Field as TextField, Icon, Select, Sheet, Text, type IconGlyphProps } from './ui';

/**
 * Settings panel.
 *
 * On the web this is a drawer that opens beside the icon rail and takes width
 * from the workspace, so the chart stays visible while it is adjusted. A phone
 * has no width to give, so here it is a tall sheet over whatever is on screen:
 * it rises from the bottom, and a drag on its header, a tap on the veil or
 * the system back gesture sends it away. It reads `settingsOpen` from the
 * workspace store, so every "Chart settings" button opens the same one.
 */
const TEMPLATES: Array<{
  value: ThemePreference;
  label: string;
  icon: ComponentType<IconGlyphProps>;
}> = [
  { value: 'light', label: 'Light Mode', icon: Sun },
  { value: 'twilight', label: 'Twilight', icon: SunMoon },
  { value: 'dark', label: 'Full Night', icon: Moon },
  { value: 'system', label: 'System', icon: Monitor },
];

const CHART_TYPES: Array<{ value: ChartType; label: string }> = [
  { value: 'candles', label: 'Candles' },
  { value: 'bars', label: 'Bars' },
  { value: 'line', label: 'Line' },
  { value: 'area', label: 'Area' },
];

const TIMEZONE_OPTIONS = TIME_ZONES.map((zone) => ({ value: zone as string, label: zoneLabel(zone) }));

export function SettingsDrawer() {
  const open = useWorkspaceStore((s) => s.settingsOpen);
  const setOpen = useWorkspaceStore((s) => s.setSettingsOpen);
  const preference = useThemeStore((s) => s.preference);
  const resolved = useThemeStore((s) => s.resolved);
  const setPreference = useThemeStore((s) => s.setPreference);
  const settings = useSettingsStore();
  const palette = useChartPalette();
  const [stakeText, onStakeText] = useDefaultStakeDraft();

  return (
    <Sheet open={open} onClose={() => setOpen(false)} title="Settings" size="large" bodyClassName="gap-5 p-4">
      <Field label="Language">
        <TextField
          value="English"
          editable={false}
          accessibilityLabel="Language"
          left={<Icon as={Globe} size={16} className="text-content-400" />}
          right={<Icon as={ChevronDown} size={16} className="text-content-400" />}
        />
        <Hint>English is the only bundled locale - no other translation ships yet.</Hint>
      </Field>

      <Field label="Timezone">
        <Select
          title="Timezone"
          value={settings.timezone}
          options={TIMEZONE_OPTIONS}
          onChange={settings.setTimezone}
        />
        <Hint>Changes how times are written. Expiries are always decided in UTC on the server.</Hint>
      </Field>

      {/* The same two controls as Account → Preferences and the
          Settings screen, so their names and meaning never drift. */}
      <View>
        <SectionTitle>Trading</SectionTitle>
        <View className="gap-3">
          <TradeModeControl />
          <OneTapSwitch />
        </View>
      </View>

      <View>
        <SectionTitle>Template</SectionTitle>
        <View className="gap-2" accessibilityRole="radiogroup" accessibilityLabel="Colour theme">
          {TEMPLATES.map((template) => {
            const active = preference === template.value;
            return (
              <Pressable
                key={template.value}
                accessibilityRole="radio"
                accessibilityState={{ checked: active }}
                onPress={() => setPreference(template.value)}
                className={cn(
                  'flex-row items-center gap-3 rounded-full border px-4 py-3',
                  active ? 'border-accent bg-surface-850' : 'border-surface-700 bg-surface-850/60 active:border-surface-600',
                )}
              >
                <Icon as={template.icon} size={16} className={active ? 'text-accent' : 'text-content-400'} />
                <Text numberOfLines={1} className="min-w-0 flex-1 text-sm font-semibold text-content-100">
                  {template.label}
                  {template.value === 'system' && active ? (
                    <Text className="text-sm font-normal text-content-400">{' (' + resolved + ')'}</Text>
                  ) : null}
                </Text>
                <View
                  className={cn(
                    'h-4 w-4 rounded-full border-2',
                    active ? 'border-accent bg-accent' : 'border-surface-600',
                  )}
                />
              </Pressable>
            );
          })}
        </View>
      </View>

      <View>
        <View className="mb-2 flex-row items-center justify-between">
          <SectionTitle className="mb-0">Platform</SectionTitle>
          <Pressable
            onPress={settings.resetChartAppearance}
            hitSlop={8}
            accessibilityRole="button"
            className="flex-row items-center gap-1 rounded px-1 py-0.5 active:opacity-70"
          >
            <Icon as={RotateCcw} size={12} className="text-content-400" />
            <Text className="text-[10px] font-semibold text-content-400">Reset</Text>
          </Pressable>
        </View>

        <View className="gap-3">
          <Field label="Grid's opacity">
            <Stepper
              label="Grid's opacity"
              value={settings.gridOpacity}
              min={0}
              max={GRID_OPACITY_MAX}
              onChange={settings.setGridOpacity}
            />
          </Field>

          <CheckRow
            label="Auto-scrolling"
            description="Automatic graphic scrolling"
            checked={settings.autoScroll}
            onChange={settings.toggleAutoScroll}
          />
          <CheckRow
            label="Performance Mode"
            description="Use optimized rendering for chart and candles"
            checked={settings.performanceMode}
            onChange={settings.togglePerformanceMode}
          />
          <CheckRow
            label="Sound on settlement"
            description="A short cue when a trade settles"
            checked={settings.soundEnabled}
            onChange={settings.toggleSound}
          />
          <CheckRow
            label="Short order label"
            description="Direction and stake beside each entry line"
            checked={settings.showOrderLabels}
            onChange={settings.toggleOrderLabels}
          />
          <CheckRow
            label="Hide trade results"
            description="Clear entry lines, arrows and boundaries off the chart"
            checked={settings.hideTradeResults}
            onChange={settings.toggleHideTradeResults}
          />
        </View>
      </View>

      <View>
        <SectionTitle>Graph</SectionTitle>
        <View className="gap-3">
          <Field label="Chart type">
            <View className="flex-row gap-1">
              {CHART_TYPES.map((type) => {
                const active = settings.chartType === type.value;
                return (
                  <Pressable
                    key={type.value}
                    accessibilityRole="radio"
                    accessibilityState={{ checked: active }}
                    onPress={() => settings.setChartType(type.value)}
                    className={cn(
                      'flex-1 items-center rounded-lg px-1 py-2',
                      active ? 'bg-accent' : 'bg-surface-850 active:bg-surface-800',
                    )}
                  >
                    <Text className={cn('text-[11px] font-semibold', active ? 'text-surface-950' : 'text-content-400')}>
                      {type.label}
                    </Text>
                  </Pressable>
                );
              })}
            </View>
          </Field>

          <Field label="Timeframe">
            <View className="flex-row flex-wrap gap-1">
              {TIMEFRAMES.map((seconds) => {
                const active = settings.timeframe === seconds;
                return (
                  <Pressable
                    key={seconds}
                    accessibilityRole="radio"
                    accessibilityState={{ checked: active }}
                    onPress={() => settings.setTimeframe(seconds)}
                    className={cn(
                      'grow basis-[18%] items-center rounded-lg px-1 py-2',
                      active ? 'bg-accent' : 'bg-surface-850 active:bg-surface-800',
                    )}
                  >
                    <Text
                      className={cn('text-[11px] font-semibold tnum', active ? 'text-surface-950' : 'text-content-400')}
                    >
                      {timeframeLabel(seconds)}
                    </Text>
                  </Pressable>
                );
              })}
            </View>
          </Field>

          <Field label="Graph colours">
            <ColourOverrides
              up={settings.upColorOverride ?? palette.up}
              down={settings.downColorOverride ?? palette.down}
              upOverridden={settings.upColorOverride !== null}
              downOverridden={settings.downColorOverride !== null}
              onUp={settings.setUpColor}
              onDown={settings.setDownColor}
            />
          </Field>
        </View>
      </View>

      <View>
        <SectionTitle>Defaults</SectionTitle>
        <View className="gap-3">
          <Field label="Default investment">
            <TextField
              keyboardType="decimal-pad"
              inputClassName="tnum"
              accessibilityLabel="Default investment"
              value={stakeText}
              onChangeText={onStakeText}
            />
            <Hint>Pre-filled on the ticket: {formatMoney(settings.defaultStake)}</Hint>
          </Field>

          <Field label="Default expiry">
            <View className="flex-row gap-1">
              {[30, 60, 300].map((seconds) => {
                const active = settings.defaultExpirySeconds === seconds;
                return (
                  <Pressable
                    key={seconds}
                    accessibilityRole="radio"
                    accessibilityState={{ checked: active }}
                    onPress={() => settings.setDefaultExpiry(seconds)}
                    className={cn(
                      'flex-1 items-center rounded-lg px-2 py-2',
                      active ? 'bg-accent' : 'bg-surface-850 active:bg-surface-800',
                    )}
                  >
                    <Text className={cn('text-xs font-semibold', active ? 'text-surface-950' : 'text-content-400')}>
                      {formatExpiry(seconds)}
                    </Text>
                  </Pressable>
                );
              })}
            </View>
          </Field>
        </View>
      </View>
    </Sheet>
  );
}

/**
 * Labelled control.
 *
 * The label sits on the field's top border the way the reference console does,
 * which keeps the name readable once a value is filled in.
 */
function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <View className="rounded-xl border border-surface-700 bg-surface-850/60 px-3 pb-3 pt-2">
      <Text className="mb-1.5 text-[11px] font-medium text-content-400">{label}</Text>
      {children}
    </View>
  );
}

function Hint({ children }: { children: ReactNode }) {
  return <Text className="mt-1.5 text-[10px] leading-[14px] text-content-400">{children}</Text>;
}

function SectionTitle({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <Text
      accessibilityRole="header"
      className={cn('mb-2 text-[10px] font-bold uppercase tracking-widest text-content-400', className)}
    >
      {children}
    </Text>
  );
}

/** Numeric dial with the decrement and increment buttons either side. */
function Stepper({
  label,
  value,
  min,
  max,
  onChange,
}: {
  label: string;
  value: number;
  min: number;
  max: number;
  onChange: (value: number) => void;
}) {
  return (
    <View className="flex-row items-center gap-2">
      <Pressable
        onPress={() => onChange(value - 1)}
        disabled={value <= min}
        accessibilityRole="button"
        accessibilityLabel="Decrease"
        className={cn(
          'h-8 w-8 items-center justify-center rounded-lg bg-surface-800 active:bg-surface-700',
          value <= min && 'opacity-40',
        )}
      >
        <Icon as={Minus} size={14} className="text-content-300" />
      </Pressable>
      <Text accessibilityLabel={label + ': ' + value} className="flex-1 text-center text-base font-bold tnum text-content-100">
        {value}
      </Text>
      <Pressable
        onPress={() => onChange(value + 1)}
        disabled={value >= max}
        accessibilityRole="button"
        accessibilityLabel="Increase"
        className={cn(
          'h-8 w-8 items-center justify-center rounded-lg bg-surface-800 active:bg-surface-700',
          value >= max && 'opacity-40',
        )}
      >
        <Icon as={Plus} size={14} className="text-content-300" />
      </Pressable>
    </View>
  );
}

/** Checkbox row: a square control, a label, and the reason it exists. */
function CheckRow({
  label,
  description,
  checked,
  onChange,
}: {
  label: string;
  description: string;
  checked: boolean;
  onChange: () => void;
}) {
  return (
    <Pressable
      onPress={onChange}
      accessibilityRole="checkbox"
      accessibilityState={{ checked }}
      className="flex-row items-start gap-3 rounded-xl px-1 py-1.5 active:bg-surface-850/60"
    >
      <View
        className={cn(
          'mt-0.5 h-4 w-4 items-center justify-center rounded border-2',
          checked ? 'border-accent bg-accent' : 'border-surface-600',
        )}
      >
        {checked ? <Icon as={Check} size={12} strokeWidth={3} color="#ffffff" /> : null}
      </View>
      <View className="min-w-0 flex-1">
        <Text className="text-sm font-semibold text-content-100">{label}</Text>
        <Text className="text-[11px] leading-4 text-content-400">{description}</Text>
      </View>
    </Pressable>
  );
}

/**
 * A small fixed palette for the colour overrides.
 *
 * The web offers the browser's colour picker; a phone has none built in, so
 * the override is chosen from these swatches or typed as a hex value.
 */
const SWATCHES = [
  '#22c55e',
  '#10b981',
  '#14b8a6',
  '#3b82f6',
  '#6366f1',
  '#a855f7',
  '#ec4899',
  '#ef4444',
  '#f97316',
  '#eab308',
  '#ffffff',
  '#94a3b8',
] as const;

const HEX = /^#[0-9a-fA-F]{6}$/;

/**
 * Both colour overrides, side by side, sharing one picker under them: tapping
 * a swatch opens the picker for that colour, tapping it again closes it.
 */
function ColourOverrides({
  up,
  down,
  upOverridden,
  downOverridden,
  onUp,
  onDown,
}: {
  up: string;
  down: string;
  upOverridden: boolean;
  downOverridden: boolean;
  onUp: (hex: string | null) => void;
  onDown: (hex: string | null) => void;
}) {
  const [editing, setEditing] = useState<'up' | 'down' | null>(null);
  const toggle = (which: 'up' | 'down') => setEditing((current) => (current === which ? null : which));

  return (
    <View>
      <View className="flex-row gap-2">
        <ColourField
          label="Up"
          value={up}
          overridden={upOverridden}
          editing={editing === 'up'}
          onToggle={() => toggle('up')}
          onChange={onUp}
        />
        <ColourField
          label="Down"
          value={down}
          overridden={downOverridden}
          editing={editing === 'down'}
          onToggle={() => toggle('down')}
          onChange={onDown}
        />
      </View>
      {editing ? (
        <ColourPicker
          key={editing}
          label={editing === 'up' ? 'Up' : 'Down'}
          value={editing === 'up' ? up : down}
          onChange={editing === 'up' ? onUp : onDown}
        />
      ) : null}
    </View>
  );
}

/**
 * Colour override.
 *
 * Defaults to following the theme; the swatch only becomes an override once the
 * trader picks something, so switching template still repaints the chart for
 * anyone who never touched this.
 */
function ColourField({
  label,
  value,
  overridden,
  editing,
  onToggle,
  onChange,
}: {
  label: string;
  value: string;
  overridden: boolean;
  editing: boolean;
  onToggle: () => void;
  onChange: (hex: string | null) => void;
}) {
  return (
    <View className="min-w-0 flex-1">
      <Text className="mb-1 text-[10px] font-medium text-content-400">{label}</Text>
      <View className="flex-row items-center gap-1.5">
        <Pressable
          onPress={onToggle}
          accessibilityRole="button"
          accessibilityLabel={label + ' colour'}
          accessibilityState={{ expanded: editing }}
          className={cn(
            'h-7 w-9 rounded-md border bg-surface-900 p-0.5',
            editing ? 'border-accent' : 'border-surface-700',
          )}
        >
          <View className="flex-1 rounded" style={{ backgroundColor: value }} />
        </Pressable>
        <Pressable
          onPress={() => onChange(null)}
          disabled={!overridden}
          accessibilityRole="button"
          className={cn('min-w-0 flex-1 rounded-md px-1 py-1', !overridden && 'opacity-50')}
        >
          <Text numberOfLines={1} className="text-[10px] font-medium text-content-400">
            {overridden ? 'Use theme' : 'Theme'}
          </Text>
        </Pressable>
      </View>
    </View>
  );
}

function ColourPicker({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (hex: string) => void;
}) {
  const [draft, setDraft] = useState(value);
  const [error, setError] = useState<string | null>(null);

  // A swatch tapped (or the override reset) moves the hex field with it.
  useEffect(() => {
    setDraft((current) => (current.toLowerCase() === value.toLowerCase() ? current : value));
  }, [value]);

  function onDraft(text: string) {
    const next = text.startsWith('#') ? text : '#' + text;
    setDraft(next);
    setError(null);
    if (HEX.test(next)) onChange(next.toLowerCase());
  }

  return (
    <View className="mt-2.5 rounded-lg border border-surface-700 bg-surface-900 p-2.5">
      <View className="flex-row flex-wrap gap-2">
        {SWATCHES.map((swatch) => {
          const selected = swatch === value.toLowerCase();
          return (
            <Pressable
              key={swatch}
              onPress={() => onChange(swatch)}
              accessibilityRole="radio"
              accessibilityLabel={label + ' colour ' + swatch}
              accessibilityState={{ checked: selected }}
              className={cn('h-7 w-7 rounded-full border-2 p-0.5', selected ? 'border-accent' : 'border-transparent')}
            >
              <View className="flex-1 rounded-full border border-surface-700" style={{ backgroundColor: swatch }} />
            </Pressable>
          );
        })}
      </View>
      <TextField
        className="mt-2.5"
        value={draft}
        onChangeText={onDraft}
        onBlur={() => {
          if (!HEX.test(draft)) setError('Use six hex digits, like #22c55e');
        }}
        autoCapitalize="none"
        autoCorrect={false}
        maxLength={7}
        accessibilityLabel={label + ' colour hex value'}
        inputClassName="tnum"
        error={error}
        left={<View className="h-4 w-4 rounded" style={{ backgroundColor: HEX.test(draft) ? draft : value }} />}
      />
    </View>
  );
}
