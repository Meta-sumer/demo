import { useState, type ComponentType, type ReactNode } from 'react';
import { Pressable, View, type LayoutChangeEvent } from 'react-native';
import {
  Activity,
  AreaChart,
  BarChart3,
  CandlestickChart,
  Check,
  Flag,
  LineChart,
  Maximize2,
  Minimize2,
  PenLine,
  Trash2,
} from 'lucide-react-native';
import { Icon, Sheet, Switch, Text, type IconGlyphProps } from '../../components/ui';
import { cn } from '../../utils/cn';
import { useTokenColor } from '../../utils/theme-colors';
import {
  TIMEFRAMES,
  timeframeLabel,
  timeframeName,
  useSettingsStore,
  type ChartType,
  type TradeMode,
} from '../../stores/settingsStore';
import { selectDrawings, useDrawingsStore } from '../../stores/drawingsStore';
import { DRAWING_TOOLS, DrawingIcon, toolInfo } from './chart/drawingTools';

/**
 * Chart toolbar.
 *
 * A row along the chart's bottom-left: it stays clear of the newest candles
 * on the right, and the fixed order keeps each control in a place it can be
 * found without reading.
 *
 * Reading order is how often a control is reached for - timeframe, then chart
 * type, then indicators, then the drawing tools - rather than the order they
 * happen to be declared in.
 *
 * Beginner mode stops after the chart type (brief UX-06: do not put every
 * tool on the beginner screen) and offers only the three chart types a
 * newcomer reads without help. Fullscreen is in both modes: it is how a phone
 * gets a chart big enough to read.
 *
 * Zoom and "back to latest" are not here: they sit bottom-centre, where every
 * reference platform puts them (record 08), and are placed by the chart.
 *
 * On the web each menu was a popover above its button; on a phone it is a
 * bottom sheet, which has room for full names and is reached with the thumb.
 */
const CHART_TYPES: Array<{ value: ChartType; label: string; icon: ComponentType<IconGlyphProps> }> = [
  { value: 'candles', label: 'Candles', icon: CandlestickChart },
  { value: 'bars', label: 'Bars', icon: BarChart3 },
  { value: 'line', label: 'Line', icon: LineChart },
  { value: 'area', label: 'Area', icon: AreaChart },
];

/** Bars read like candles without the bodies, so they wait for advanced mode. */
export const BEGINNER_CHART_TYPES: readonly ChartType[] = ['candles', 'line', 'area'];

interface Props {
  assetId: string | null;
  mode: TradeMode;
  fullscreen: boolean;
  onToggleFullscreen: () => void;
  /** Where the row ends, so the zoom controls can keep clear of it. */
  onRowLayout?: (right: number) => void;
  /**
   * The indicator picker is a dialog, and it is rendered by the chart rather
   * than in here, so it is not tied to this row's lifetime or position.
   */
  indicatorsOpen: boolean;
  onOpenIndicators: () => void;
}

type Menu = 'timeframe' | 'type' | 'draw';

export function ChartToolbar({
  assetId,
  mode,
  fullscreen,
  onToggleFullscreen,
  onRowLayout,
  indicatorsOpen,
  onOpenIndicators,
}: Props) {
  const advanced = mode === 'advanced';
  const [open, setOpen] = useState<Menu | null>(null);
  const [confirmClear, setConfirmClear] = useState(false);

  const timeframe = useSettingsStore((s) => s.timeframe);
  const setTimeframe = useSettingsStore((s) => s.setTimeframe);
  const chartType = useSettingsStore((s) => s.chartType);
  const setChartType = useSettingsStore((s) => s.setChartType);
  const indicatorCount = useSettingsStore((s) => s.indicators.length);
  const showExpiryGuide = useSettingsStore((s) => s.showExpiryGuide);
  const toggleExpiryGuide = useSettingsStore((s) => s.toggleExpiryGuide);

  const drawings = useDrawingsStore((s) => selectDrawings(s, assetId));
  const armed = useDrawingsStore((s) => (assetId && s.armed?.assetId === assetId ? s.armed.kind : null));
  const selectedId = useDrawingsStore((s) => s.selectedId);
  const arm = useDrawingsStore((s) => s.arm);
  const disarm = useDrawingsStore((s) => s.disarm);
  const removeDrawing = useDrawingsStore((s) => s.remove);
  const clearDrawings = useDrawingsStore((s) => s.clear);
  const selectedHere = selectedId !== null && drawings.some((drawing) => drawing.id === selectedId);

  const accentInk = useTokenColor('surface-950');
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

  return (
    /*
     * Held clear of the time axis along the bottom - a 28px strip of tick
     * labels, which is the part of the axis a trader actually reads - by
     * sitting 36px up. (The web also had to dodge the charting library's
     * attribution logo in the corner; this chart has none.)
     */
    <View
      className="absolute bottom-9 left-2 flex-row items-center gap-1"
      onLayout={(event: LayoutChangeEvent) => {
        const { x, width } = event.nativeEvent.layout;
        onRowLayout?.(x + width);
      }}
    >
      <ToolButton label="Timeframe" open={open === 'timeframe'} onPress={() => toggle('timeframe')}>
        <Text className={cn('text-xs font-bold tnum', open === 'timeframe' ? 'text-content-100' : 'text-content-300')}>
          {timeframeLabel(timeframe)}
        </Text>
      </ToolButton>

      <ToolButton label="Chart type" open={open === 'type'} onPress={() => toggle('type')}>
        <Icon as={activeType.icon} size={16} className={open === 'type' ? 'text-content-100' : 'text-content-300'} />
      </ToolButton>

      {advanced ? (
        <>
          <ToolButton
            label={indicatorCount > 0 ? 'Indicators (' + indicatorCount + ' on)' : 'Indicators'}
            active={indicatorsOpen}
            badge={indicatorCount || undefined}
            onPress={() => {
              close();
              onOpenIndicators();
            }}
          >
            <Icon as={Activity} size={16} className={indicatorsOpen ? 'text-surface-950' : 'text-content-300'} />
          </ToolButton>

          {/* While a tool waits for its taps the button shows that tool, and
              pressing it puts the tool down - the same way out as the hint's
              Cancel. */}
          {armed ? (
            <ToolButton
              label={'Stop drawing: ' + toolInfo(armed).name}
              active
              onPress={() => {
                close();
                disarm();
              }}
            >
              <DrawingIcon kind={armed} size={16} color={accentInk} />
            </ToolButton>
          ) : (
            <ToolButton
              label="Drawing tools"
              open={open === 'draw'}
              badge={drawings.length || undefined}
              onPress={() => toggle('draw')}
            >
              <Icon as={PenLine} size={16} className={open === 'draw' ? 'text-content-100' : 'text-content-300'} />
            </ToolButton>
          )}
        </>
      ) : null}

      <ToolButton
        label={fullscreen ? 'Exit fullscreen' : 'Fullscreen chart'}
        active={fullscreen}
        onPress={() => {
          close();
          onToggleFullscreen();
        }}
      >
        <Icon as={fullscreen ? Minimize2 : Maximize2} size={16} className={fullscreen ? 'text-surface-950' : 'text-content-300'} />
      </ToolButton>

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

      <Sheet open={open === 'type'} onClose={close} title="Chart type" bodyClassName="px-2 pb-2">
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
              <Text className={cn('flex-1 text-sm font-medium', activeType.value === entry.value ? 'text-content-100' : 'text-content-300')}>
                {entry.label}
              </Text>
            </MenuRow>
          ))}
        </View>

        {/* Chart settings that belong next to how the chart is drawn, the way
            the reference platforms keep "show area" under chart type. */}
        <View className="mt-2 border-t border-surface-800 pt-2">
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
          Saved on this device for this asset. Select a drawing to delete it.
        </Text>
      </Sheet>
    </View>
  );
}

function ToolButton({
  children,
  label,
  active,
  open,
  badge,
  onPress,
}: {
  children: ReactNode;
  label: string;
  /** A state that is on: filled in the accent. */
  active?: boolean;
  /** Its menu is open: outlined in the accent. */
  open?: boolean;
  badge?: number;
  onPress: () => void;
}) {
  return (
    <Pressable
      onPress={onPress}
      accessibilityRole="button"
      accessibilityLabel={label}
      accessibilityState={{ selected: !!active, expanded: open }}
      hitSlop={4}
      className={cn(
        'h-8 w-8 items-center justify-center rounded-lg border',
        active
          ? 'border-accent bg-accent'
          : open
            ? 'border-accent bg-surface-850'
            : 'border-surface-700/70 bg-surface-900/80 active:border-surface-600',
      )}
    >
      {children}
      {badge ? (
        <View className="absolute -right-1 -top-1 h-3.5 min-w-[14px] items-center justify-center rounded-full bg-accent px-1">
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
