import { useState, type ComponentType, type ReactNode } from 'react';
import { Pressable, View } from 'react-native';
import {
  Activity,
  AreaChart,
  BarChart3,
  CandlestickChart,
  Check,
  ChevronDown,
  ChevronRight,
  Ellipsis,
  Flag,
  LineChart,
  Maximize2,
  Minimize2,
  PenLine,
  Redo2,
  Settings2,
  Trash2,
  Undo2,
} from 'lucide-react-native';
import { Icon, Sheet, Switch, Text, type IconGlyphProps } from '../../components/ui';
import { cn } from '../../utils/cn';
import { useTokenColor, withAlpha } from '../../utils/theme-colors';
import {
  TIMEFRAMES,
  timeframeLabel,
  timeframeName,
  useSettingsStore,
  type ChartType,
  type TradeMode,
} from '../../stores/settingsStore';
import { selectCanRedo, selectCanUndo, selectDrawings, useDrawingsStore } from '../../stores/drawingsStore';
import { useWorkspaceStore } from '../../stores/workspaceStore';
import { DRAWING_TOOLS, DrawingIcon, toolInfo } from './chart/drawingTools';
import type { ChartLook } from './chart/engine/colors';
import type { Asset } from '../../types/api';

/**
 * Chart toolbar: a bar under the chart, the way TradingView's mobile app lays
 * it out.
 *
 *   [GBPUSD v] [1m] [pen] [...]  |  [undo] [redo]            [fullscreen]
 *
 * The symbol opens the market selector, the timeframe its sheet, the pen the
 * drawing tools, and "..." everything about how the chart is drawn - the
 * chart type, the expiry line, the indicators and the chart settings. Undo
 * and redo step through the drawings on this instrument. Fullscreen sits on
 * the far right in both modes: it is how a phone gets a chart big enough to
 * read.
 *
 * Beginner mode leaves out the drawing tools, undo and redo and the
 * indicators (brief UX-06: do not put every tool on the beginner screen), and
 * offers only the three chart types a newcomer reads without help.
 *
 * Zoom is a pinch; "back to latest" appears on the chart itself once the view
 * has left the live edge.
 */
const CHART_TYPES: Array<{ value: ChartType; label: string; icon: ComponentType<IconGlyphProps> }> = [
  { value: 'candles', label: 'Candles', icon: CandlestickChart },
  { value: 'bars', label: 'Bars', icon: BarChart3 },
  { value: 'line', label: 'Line', icon: LineChart },
  { value: 'area', label: 'Area', icon: AreaChart },
];

/** Bars read like candles without the bodies, so they wait for advanced mode. */
export const BEGINNER_CHART_TYPES: readonly ChartType[] = ['candles', 'line', 'area'];

/** The bar's height; the chart reserves it under the plot. */
export const TOOLBAR_HEIGHT = 44;

interface Props {
  asset: Asset | null;
  mode: TradeMode;
  look: ChartLook;
  fullscreen: boolean;
  onToggleFullscreen: () => void;
  /** The indicator picker is a dialog the chart renders; this only asks for it. */
  indicatorsOpen: boolean;
  onOpenIndicators: () => void;
  /**
   * Runs something that opens outside the chart - the market selector, the
   * settings drawer - after leaving fullscreen, which would otherwise sit on
   * top of it.
   */
  onOutside: (action: () => void) => void;
}

type Menu = 'timeframe' | 'more' | 'draw';

export function ChartToolbar({
  asset,
  mode,
  look,
  fullscreen,
  onToggleFullscreen,
  indicatorsOpen,
  onOpenIndicators,
  onOutside,
}: Props) {
  const advanced = mode === 'advanced';
  const assetId = asset?.id ?? null;
  const [open, setOpen] = useState<Menu | null>(null);
  const [confirmClear, setConfirmClear] = useState(false);

  const timeframe = useSettingsStore((s) => s.timeframe);
  const setTimeframe = useSettingsStore((s) => s.setTimeframe);
  const chartType = useSettingsStore((s) => s.chartType);
  const setChartType = useSettingsStore((s) => s.setChartType);
  const indicatorCount = useSettingsStore((s) => s.indicators.length);
  const showExpiryGuide = useSettingsStore((s) => s.showExpiryGuide);
  const toggleExpiryGuide = useSettingsStore((s) => s.toggleExpiryGuide);
  const setPickerOpen = useWorkspaceStore((s) => s.setAssetPickerOpen);
  const setSettingsOpen = useWorkspaceStore((s) => s.setSettingsOpen);

  const drawings = useDrawingsStore((s) => selectDrawings(s, assetId));
  const armed = useDrawingsStore((s) => (assetId && s.armed?.assetId === assetId ? s.armed.kind : null));
  const selectedId = useDrawingsStore((s) => s.selectedId);
  const canUndo = useDrawingsStore((s) => selectCanUndo(s, assetId));
  const canRedo = useDrawingsStore((s) => selectCanRedo(s, assetId));
  const arm = useDrawingsStore((s) => s.arm);
  const disarm = useDrawingsStore((s) => s.disarm);
  const removeDrawing = useDrawingsStore((s) => s.remove);
  const clearDrawings = useDrawingsStore((s) => s.clear);
  const undo = useDrawingsStore((s) => s.undo);
  const redo = useDrawingsStore((s) => s.redo);
  const selectedHere = selectedId !== null && drawings.some((drawing) => drawing.id === selectedId);

  const tileInk = useTokenColor('content-300');

  const close = () => {
    setOpen(null);
    setConfirmClear(false);
  };

  const toggle = (name: Menu) => {
    setConfirmClear(false);
    setOpen(open === name ? null : name);
  };

  const types = advanced ? CHART_TYPES : CHART_TYPES.filter((entry) => BEGINNER_CHART_TYPES.includes(entry.value));
  const activeType = types.find((entry) => entry.value === chartType) ?? types[0]!;
  const symbol = asset?.symbol.replace('/', '') ?? '--';

  return (
    <View
      className="flex-row items-center px-1"
      style={{ height: TOOLBAR_HEIGHT, backgroundColor: look.background, borderTopWidth: 1, borderTopColor: look.rule }}
    >
      <BarButton label={'Market: ' + symbol} onPress={() => onOutside(() => setPickerOpen(true))} disabled={!asset}>
        <Text numberOfLines={1} className="text-[13px] font-bold" style={{ color: look.strongText, maxWidth: 104 }}>
          {symbol}
        </Text>
        <Icon as={ChevronDown} size={14} color={look.text} />
      </BarButton>

      <BarButton label={'Timeframe: ' + timeframeName(timeframe)} onPress={() => toggle('timeframe')} active={open === 'timeframe'} look={look}>
        <Text className="text-[13px] font-semibold tnum" style={{ color: open === 'timeframe' ? look.accent : look.strongText }}>
          {timeframeLabel(timeframe)}
        </Text>
      </BarButton>

      {advanced ? (
        /* While a tool waits for its taps the button shows that tool, and
           pressing it puts the tool down - the same way out as the hint's
           Cancel. */
        armed ? (
          <BarButton
            label={'Stop drawing: ' + toolInfo(armed).name}
            onPress={() => {
              close();
              disarm();
            }}
            active
            look={look}
          >
            <DrawingIcon kind={armed} size={18} color={look.accent} />
          </BarButton>
        ) : (
          <BarButton label="Drawing tools" onPress={() => toggle('draw')} active={open === 'draw'} look={look} badge={drawings.length || undefined}>
            <Icon as={PenLine} size={18} color={open === 'draw' ? look.accent : look.text} />
          </BarButton>
        )
      ) : null}

      <BarButton label="Chart options" onPress={() => toggle('more')} active={open === 'more'} look={look}>
        <Icon as={Ellipsis} size={18} color={open === 'more' ? look.accent : look.text} />
      </BarButton>

      {advanced ? (
        <>
          <View className="mx-1 h-5 w-px" style={{ backgroundColor: look.rule }} />
          <BarButton label="Undo" onPress={() => assetId && undo(assetId)} disabled={!canUndo}>
            <Icon as={Undo2} size={18} color={look.text} />
          </BarButton>
          <BarButton label="Redo" onPress={() => assetId && redo(assetId)} disabled={!canRedo}>
            <Icon as={Redo2} size={18} color={look.text} />
          </BarButton>
        </>
      ) : null}

      <View className="flex-1" />

      <BarButton
        label={fullscreen ? 'Exit fullscreen' : 'Fullscreen chart'}
        onPress={() => {
          close();
          onToggleFullscreen();
        }}
      >
        <Icon as={fullscreen ? Minimize2 : Maximize2} size={18} color={look.text} />
      </BarButton>

      <Sheet open={open === 'timeframe'} onClose={close} title="Timeframe" bodyClassName="px-4 pb-2">
        {/* Two columns of full names. Ten abbreviations in three columns asked
            the reader to decode each one; there is room here not to. */}
        <View className="-mx-0.5 flex-row flex-wrap">
          {TIMEFRAMES.map((seconds) => (
            <View key={seconds} className="w-1/2 p-0.5">
              <Pressable
                accessibilityRole="radio"
                accessibilityState={{ checked: timeframe === seconds }}
                onPress={() => {
                  setTimeframe(seconds);
                  close();
                }}
                className={cn(
                  'items-center rounded-lg px-2 py-2.5',
                  timeframe === seconds ? 'bg-accent' : 'bg-surface-850 active:bg-surface-800',
                )}
              >
                <Text
                  numberOfLines={1}
                  className={cn('text-xs font-semibold', timeframe === seconds ? 'text-surface-950' : 'text-content-300')}
                >
                  {timeframeName(seconds)}
                </Text>
              </Pressable>
            </View>
          ))}
        </View>
        <Text className="mt-2 text-[11px] leading-snug text-content-400">
          Frames under a minute are rebuilt from the last 15 minutes of ticks, so their history is short.
        </Text>
      </Sheet>

      <Sheet open={open === 'more'} onClose={close} title="Chart" bodyClassName="px-2 pb-2">
        <Text className="mb-1 px-2 text-[11px] font-bold uppercase tracking-widest text-content-400">Chart type</Text>
        <View className="gap-0.5">
          {types.map((entry) => (
            <MenuRow
              key={entry.value}
              selected={activeType.value === entry.value}
              onPress={() => {
                setChartType(entry.value);
                close();
              }}
            >
              <Icon as={entry.icon} size={16} className="text-content-400" />
              <Text
                className={cn(
                  'flex-1 text-sm font-medium',
                  activeType.value === entry.value ? 'text-content-100' : 'text-content-300',
                )}
              >
                {entry.label}
              </Text>
            </MenuRow>
          ))}
        </View>

        {/* Chart settings that belong next to how the chart is drawn, the way
            the reference platforms keep "show area" under chart type. */}
        <View className="mt-2 gap-0.5 border-t border-surface-800 pt-2">
          <Pressable
            accessibilityRole="switch"
            accessibilityState={{ checked: showExpiryGuide }}
            onPress={toggleExpiryGuide}
            className="flex-row items-start gap-2.5 rounded-lg px-2 py-2 active:bg-surface-850"
          >
            <View className="mt-0.5">
              <Icon as={Flag} size={16} className="text-content-400" />
            </View>
            <View className="min-w-0 flex-1">
              <Text className="text-sm font-medium text-content-100">Expiry line</Text>
              <Text className="text-xs leading-snug text-content-400">Where a trade placed now would end</Text>
            </View>
            <Switch value={showExpiryGuide} onValueChange={toggleExpiryGuide} accessibilityLabel="Expiry line" />
          </Pressable>

          {advanced ? (
            <LinkRow
              icon={Activity}
              label={indicatorCount > 0 ? 'Indicators (' + indicatorCount + ' on)' : 'Indicators'}
              active={indicatorsOpen}
              onPress={() => {
                close();
                onOpenIndicators();
              }}
            />
          ) : null}

          <LinkRow
            icon={Settings2}
            label="Chart settings"
            onPress={() => {
              close();
              onOutside(() => setSettingsOpen(true));
            }}
          />
        </View>
      </Sheet>

      <Sheet open={open === 'draw'} onClose={close} title="Drawing tools" bodyClassName="px-4 pb-2">
        <View className="-mx-0.5 flex-row flex-wrap">
          {DRAWING_TOOLS.map((tool) => (
            <View key={tool.kind} className="w-1/3 p-0.5">
              <Pressable
                disabled={!assetId}
                accessibilityRole="button"
                accessibilityLabel={tool.name}
                onPress={() => {
                  if (!assetId) return;
                  arm(tool.kind, assetId);
                  close();
                }}
                className={cn(
                  'items-center gap-1 rounded-lg bg-surface-850 px-1 py-2.5 active:bg-surface-800',
                  !assetId && 'opacity-40',
                )}
              >
                <DrawingIcon kind={tool.kind} size={20} color={tileInk} />
                <Text className="text-[11px] font-semibold text-content-300">{tool.short}</Text>
              </Pressable>
            </View>
          ))}
        </View>

        <View className="mt-2 gap-0.5 border-t border-surface-800 pt-2">
          <ActionRow
            disabled={!selectedHere}
            onPress={() => {
              if (assetId && selectedId) removeDrawing(assetId, selectedId);
            }}
          >
            Delete selected
          </ActionRow>
          {confirmClear ? (
            <View className="flex-row items-center gap-1 rounded-lg bg-down/10 px-2 py-1.5">
              <Text className="min-w-0 flex-1 text-xs font-medium text-content-200">
                Clear {drawings.length} drawing{drawings.length === 1 ? '' : 's'}?
              </Text>
              <Pressable
                accessibilityRole="button"
                onPress={() => {
                  if (assetId) clearDrawings(assetId);
                  close();
                }}
                className="rounded-md bg-down-solid px-3 py-1.5 active:opacity-80"
              >
                <Text className="text-xs font-bold text-white">Clear</Text>
              </Pressable>
              <Pressable
                accessibilityRole="button"
                onPress={() => setConfirmClear(false)}
                className="rounded-md px-3 py-1.5 active:bg-surface-850"
              >
                <Text className="text-xs font-semibold text-content-400">Keep</Text>
              </Pressable>
            </View>
          ) : (
            <ActionRow disabled={drawings.length === 0} onPress={() => setConfirmClear(true)}>
              {'Clear all' + (drawings.length > 0 ? ' (' + drawings.length + ')' : '')}
            </ActionRow>
          )}
        </View>
        <Text className="mt-2 text-[11px] leading-snug text-content-400">
          Saved on this device for this asset. Select a drawing to delete it; undo brings it back.
        </Text>
      </Sheet>
    </View>
  );
}

function BarButton({
  children,
  label,
  onPress,
  active,
  disabled,
  badge,
  look,
}: {
  children: ReactNode;
  label: string;
  onPress: () => void;
  /** Its menu is open, or its state is on. */
  active?: boolean;
  disabled?: boolean;
  badge?: number;
  look?: ChartLook;
}) {
  return (
    <Pressable
      onPress={onPress}
      disabled={disabled}
      accessibilityRole="button"
      accessibilityLabel={label}
      accessibilityState={{ disabled: !!disabled, selected: !!active }}
      hitSlop={2}
      className={cn('h-9 flex-row items-center gap-1 rounded-md px-2.5 active:opacity-60', disabled && 'opacity-30')}
      style={active && look ? { backgroundColor: withAlpha(look.accent, 0.12) } : undefined}
    >
      {children}
      {badge ? (
        <View className="absolute right-0.5 top-0.5 h-3.5 min-w-[14px] items-center justify-center rounded-full bg-accent px-1">
          <Text className="text-[9px] font-bold leading-[12px] text-surface-950">{badge}</Text>
        </View>
      ) : null}
    </Pressable>
  );
}

function MenuRow({ selected, onPress, children }: { selected: boolean; onPress: () => void; children: ReactNode }) {
  return (
    <Pressable
      accessibilityRole="radio"
      accessibilityState={{ checked: selected }}
      onPress={onPress}
      className={cn('flex-row items-center gap-2.5 rounded-lg px-2 py-2.5', selected ? 'bg-accent/15' : 'active:bg-surface-850')}
    >
      {children}
      <View style={{ opacity: selected ? 1 : 0 }}>
        <Icon as={Check} size={14} className="text-accent" />
      </View>
    </Pressable>
  );
}

function LinkRow({
  icon,
  label,
  active,
  onPress,
}: {
  icon: ComponentType<IconGlyphProps>;
  label: string;
  active?: boolean;
  onPress: () => void;
}) {
  return (
    <Pressable
      accessibilityRole="button"
      onPress={onPress}
      className={cn('flex-row items-center gap-2.5 rounded-lg px-2 py-2.5', active ? 'bg-accent/15' : 'active:bg-surface-850')}
    >
      <Icon as={icon} size={16} className="text-content-400" />
      <Text className="flex-1 text-sm font-medium text-content-100">{label}</Text>
      <Icon as={ChevronRight} size={16} className="text-content-400" />
    </Pressable>
  );
}

function ActionRow({ disabled, onPress, children }: { disabled?: boolean; onPress: () => void; children: string }) {
  return (
    <Pressable
      accessibilityRole="button"
      disabled={disabled}
      onPress={onPress}
      className={cn('flex-row items-center gap-2 rounded-lg px-2 py-2.5 active:bg-surface-850', disabled && 'opacity-40')}
    >
      <Icon as={Trash2} size={14} className="text-content-300" />
      <Text className="text-sm font-medium text-content-300">{children}</Text>
    </Pressable>
  );
}
