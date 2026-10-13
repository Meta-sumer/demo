import { useCallback, useState, type MutableRefObject } from 'react';
import clsx from 'clsx';
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
} from 'lucide-react';
import {
  TIMEFRAMES,
  timeframeLabel,
  timeframeName,
  useSettingsStore,
  type ChartType,
  type TradeMode,
} from '../../stores/settingsStore';
import { selectDrawings, useDrawingsStore } from '../../stores/drawingsStore';
import { useDismiss } from '../../hooks/useDismiss';
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
 */
const CHART_TYPES: Array<{ value: ChartType; label: string; icon: typeof LineChart }> = [
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
  /** The row itself, so the zoom controls can keep clear of it. */
  rootRef: MutableRefObject<HTMLDivElement | null>;
  /**
   * The indicator picker is a dialog, and it is rendered by the chart rather
   * than in here: this row is its own stacking layer, under the orders, and a
   * dialog opened from inside it would have order tags painted over it.
   */
  indicatorsOpen: boolean;
  onOpenIndicators: () => void;
}

export function ChartToolbar({
  assetId,
  mode,
  fullscreen,
  onToggleFullscreen,
  rootRef,
  indicatorsOpen,
  onOpenIndicators,
}: Props) {
  const advanced = mode === 'advanced';
  const [open, setOpen] = useState<'timeframe' | 'type' | 'draw' | null>(null);
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

  // Close whichever popover is open on an outside click or Escape.
  const closePopover = useCallback(() => {
    setOpen(null);
    setConfirmClear(false);
  }, []);
  useDismiss(open !== null, rootRef, closePopover);

  const toggle = (name: 'timeframe' | 'type' | 'draw') => {
    setConfirmClear(false);
    setOpen(open === name ? null : name);
  };

  const types = advanced ? CHART_TYPES : CHART_TYPES.filter((entry) => BEGINNER_CHART_TYPES.includes(entry.value));
  const activeType = types.find((entry) => entry.value === chartType) ?? types[0]!;

  return (
    /*
     * Held clear of two things the chart draws for itself.
     *
     * Across the bottom is the time axis, a 28px strip of tick labels. The
     * toolbar was overlapping it by 17px, so the buttons sat on top of the
     * times - which is the part of the axis a trader actually reads.
     *
     * In the bottom-left corner is the TradingView attribution link
     * (`a#tv-attr-logo`, about 35px wide), which a translucent button was
     * half-covering and making look like a rendering fault. The library can be
     * told not to draw it, but that link is how the Apache NOTICE is
     * satisfied, so the toolbar moves instead of the logo.
     *
     * The result puts the row on the same line as the logo, just above the
     * axis. The offsets are measured, not guessed: 36px clears a 28px axis
     * with room to spare, and 56px clears a 35px logo.
     */
    <div ref={rootRef} className="absolute bottom-9 left-14 z-20 flex flex-row items-center gap-1">
      <Popover
        open={open === 'timeframe'}
        onToggle={() => toggle('timeframe')}
        label="Timeframe"
        wide
        trigger={<span className="tnum text-xs font-bold">{timeframeLabel(timeframe)}</span>}
      >
        <PopoverTitle>Timeframe</PopoverTitle>
        {/* Two columns of full names. Ten abbreviations in three columns asked
            the reader to decode each one; there is room here not to. */}
        <div className="grid grid-cols-2 gap-1">
          {TIMEFRAMES.map((seconds) => (
            <button
              key={seconds}
              onClick={() => {
                setTimeframe(seconds);
                setOpen(null);
              }}
              className={clsx(
                'whitespace-nowrap rounded-lg px-2 py-1.5 text-[11px] font-semibold transition',
                timeframe === seconds
                  ? 'bg-accent text-surface-950'
                  : 'bg-surface-850 text-content-300 hover:bg-surface-800 hover:text-content-100',
              )}
            >
              {timeframeName(seconds)}
            </button>
          ))}
        </div>
        <p className="mt-2 text-[10px] leading-snug text-content-400">
          Frames under a minute are rebuilt from the last 15 minutes of ticks, so their history is short.
        </p>
      </Popover>

      <Popover
        open={open === 'type'}
        onToggle={() => toggle('type')}
        label="Chart type"
        wide
        trigger={<activeType.icon className="h-4 w-4" />}
      >
        <PopoverTitle>Chart type</PopoverTitle>
        <div className="space-y-0.5">
          {types.map((entry) => (
            <MenuRow
              key={entry.value}
              selected={activeType.value === entry.value}
              onClick={() => {
                setChartType(entry.value);
                setOpen(null);
              }}
            >
              <entry.icon className="h-4 w-4 shrink-0 text-content-400" />
              {entry.label}
            </MenuRow>
          ))}
        </div>

        {/* Chart settings that belong next to how the chart is drawn, the way
            the reference platforms keep "show area" under chart type. */}
        <div className="mt-2 border-t border-surface-800 pt-2">
          <button
            type="button"
            role="switch"
            aria-checked={showExpiryGuide}
            onClick={toggleExpiryGuide}
            className="flex w-full items-start gap-2.5 rounded-lg px-2 py-1.5 text-left transition hover:bg-surface-850"
          >
            <Flag className="mt-0.5 h-4 w-4 shrink-0 text-content-400" />
            <span className="min-w-0 flex-1">
              <span className="block text-xs font-medium text-content-100">Expiry line</span>
              <span className="block text-[10px] leading-snug text-content-400">
                Where a trade placed now would end
              </span>
            </span>
            <span
              aria-hidden
              className={clsx(
                'relative mt-0.5 h-4 w-7 shrink-0 rounded-full transition',
                showExpiryGuide ? 'bg-accent' : 'bg-surface-700',
              )}
            >
              <span
                className={clsx(
                  'absolute top-0.5 h-3 w-3 rounded-full bg-surface-950 transition-[left]',
                  showExpiryGuide ? 'left-3.5' : 'left-0.5',
                )}
              />
            </span>
          </button>
        </div>
      </Popover>

      {advanced ? (
        <>
          <ToolButton
            label={indicatorCount > 0 ? 'Indicators (' + indicatorCount + ' on)' : 'Indicators'}
            active={indicatorsOpen}
            badge={indicatorCount || undefined}
            onClick={() => {
              setOpen(null);
              onOpenIndicators();
            }}
          >
            <Activity className="h-4 w-4" />
          </ToolButton>

          {/* While a tool waits for its clicks the button shows that tool, and
              pressing it puts the tool down - the same way out as Escape and
              the hint's Cancel. */}
          {armed ? (
            <ToolButton
              label={'Stop drawing: ' + toolInfo(armed).name}
              active
              onClick={() => {
                setOpen(null);
                disarm();
              }}
            >
              <DrawingIcon kind={armed} className="h-4 w-4" />
            </ToolButton>
          ) : (
            <Popover
              open={open === 'draw'}
              onToggle={() => toggle('draw')}
              label="Drawing tools"
              wide
              badge={drawings.length || undefined}
              trigger={<PenLine className="h-4 w-4" />}
            >
              <PopoverTitle>Drawing tools</PopoverTitle>
              <div className="grid grid-cols-3 gap-1">
                {DRAWING_TOOLS.map((tool) => (
                  <button
                    key={tool.kind}
                    type="button"
                    disabled={!assetId}
                    onClick={() => {
                      if (!assetId) return;
                      arm(tool.kind, assetId);
                      setOpen(null);
                    }}
                    title={tool.name}
                    className="flex flex-col items-center gap-1 rounded-lg bg-surface-850 px-1 py-2 text-[10px] font-semibold text-content-300 transition hover:bg-surface-800 hover:text-content-100 disabled:opacity-40"
                  >
                    <DrawingIcon kind={tool.kind} className="h-5 w-5" />
                    {tool.short}
                  </button>
                ))}
              </div>

              <div className="mt-2 space-y-0.5 border-t border-surface-800 pt-2">
                <ActionRow
                  disabled={!selectedHere}
                  onClick={() => {
                    if (assetId && selectedId) removeDrawing(assetId, selectedId);
                  }}
                >
                  <Trash2 className="h-3.5 w-3.5 shrink-0" />
                  Delete selected
                </ActionRow>
                {confirmClear ? (
                  <div className="flex items-center gap-1 rounded-lg bg-down/10 px-2 py-1">
                    <span className="min-w-0 flex-1 text-[11px] font-medium text-content-200">
                      Clear {drawings.length} drawing{drawings.length === 1 ? '' : 's'}?
                    </span>
                    <button
                      type="button"
                      onClick={() => {
                        if (assetId) clearDrawings(assetId);
                        setConfirmClear(false);
                        setOpen(null);
                      }}
                      className="rounded-md bg-down-solid px-2 py-1 text-[11px] font-bold text-white"
                    >
                      Clear
                    </button>
                    <button
                      type="button"
                      onClick={() => setConfirmClear(false)}
                      className="rounded-md px-2 py-1 text-[11px] font-semibold text-content-400 hover:text-content-100"
                    >
                      Keep
                    </button>
                  </div>
                ) : (
                  <ActionRow disabled={drawings.length === 0} onClick={() => setConfirmClear(true)}>
                    <Trash2 className="h-3.5 w-3.5 shrink-0" />
                    Clear all{drawings.length > 0 ? ' (' + drawings.length + ')' : ''}
                  </ActionRow>
                )}
              </div>
              <p className="mt-2 hidden text-[10px] leading-snug text-content-400 sm:block">
                Saved on this device for this asset. Select a drawing to delete it, or press Delete.
              </p>
            </Popover>
          )}

        </>
      ) : null}

      <ToolButton
        label={fullscreen ? 'Exit fullscreen' : 'Fullscreen chart'}
        active={fullscreen}
        onClick={() => {
          setOpen(null);
          onToggleFullscreen();
        }}
      >
        {fullscreen ? <Minimize2 className="h-4 w-4" /> : <Maximize2 className="h-4 w-4" />}
      </ToolButton>
    </div>
  );
}

function ToolButton({
  children,
  label,
  active,
  badge,
  onClick,
}: {
  children: React.ReactNode;
  label: string;
  active?: boolean;
  badge?: number;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={label}
      aria-label={label}
      aria-pressed={active}
      className={clsx(
        'relative grid h-8 w-8 place-items-center rounded-lg border backdrop-blur transition',
        active
          ? 'border-accent bg-accent text-surface-950'
          : 'border-surface-700/70 bg-surface-900/80 text-content-300 hover:border-surface-600 hover:text-content-100',
      )}
    >
      {children}
      {badge ? <Badge value={badge} /> : null}
    </button>
  );
}

function Badge({ value }: { value: number }) {
  return (
    <span className="absolute -right-1 -top-1 grid h-3.5 min-w-3.5 place-items-center rounded-full bg-accent px-1 text-[9px] font-bold text-surface-950">
      {value}
    </span>
  );
}

function Popover({
  open,
  onToggle,
  label,
  trigger,
  badge,
  wide,
  children,
}: {
  open: boolean;
  onToggle: () => void;
  label: string;
  trigger: React.ReactNode;
  badge?: number;
  /** Two columns of full names need more room than a list of icons. */
  wide?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className="relative">
      {open ? (
        <div
          className={clsx(
            'absolute bottom-full left-0 mb-1.5 rounded-xl border border-surface-700 bg-surface-900 p-2 shadow-overlay',
            wide ? 'w-[250px]' : 'w-[190px]',
          )}
        >
          {children}
        </div>
      ) : null}

      <button
        type="button"
        onClick={onToggle}
        title={label}
        aria-label={label}
        aria-expanded={open}
        className={clsx(
          'relative grid h-8 w-8 place-items-center rounded-lg border backdrop-blur transition',
          open
            ? 'border-accent bg-surface-850 text-content-100'
            : 'border-surface-700/70 bg-surface-900/80 text-content-300 hover:border-surface-600 hover:text-content-100',
        )}
      >
        {trigger}
        {badge ? <Badge value={badge} /> : null}
      </button>
    </div>
  );
}

function PopoverTitle({ children }: { children: React.ReactNode }) {
  return (
    <h4 className="mb-1.5 px-1 text-[10px] font-bold uppercase tracking-widest text-content-400">{children}</h4>
  );
}

function MenuRow({
  selected,
  onClick,
  children,
}: {
  selected: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={clsx(
        'flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-xs font-medium transition',
        selected
          ? 'bg-accent/15 text-content-100'
          : 'text-content-300 hover:bg-surface-850 hover:text-content-100',
      )}
    >
      {children}
      <Check
        className={clsx('ml-auto h-3.5 w-3.5 shrink-0 text-accent', selected ? 'opacity-100' : 'opacity-0')}
      />
    </button>
  );
}

function ActionRow({
  disabled,
  onClick,
  children,
}: {
  disabled?: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className="flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-xs font-medium text-content-300 transition enabled:hover:bg-surface-850 enabled:hover:text-content-100 disabled:opacity-40"
    >
      {children}
    </button>
  );
}
