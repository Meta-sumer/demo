import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { Pressable, StyleSheet, View } from 'react-native';
import { Canvas, createPicture, Picture, type SkCanvas, type SkFont } from '@shopify/react-native-skia';
import { GestureDetector } from 'react-native-gesture-handler';
import Animated, { useAnimatedStyle, useDerivedValue, useSharedValue } from 'react-native-reanimated';
import { useQuery } from '@tanstack/react-query';
import { Flag, Maximize, Minus, Plus, RefreshCw, Timer } from 'lucide-react-native';
import { api } from '../../../services/api';
import { useMarketStore } from '../../../stores/marketStore';
import { useMarketSubscription } from '../../../hooks/useRealtime';
import { useCountdown } from '../../../hooks/useCountdown';
import { Button, Icon, Skeleton, Text } from '../../../components/ui';
import { formatCountdown, formatPrice } from '../../../utils/format';
import { formatClock, resolveZone } from '../../../utils/time';
import { statusTone } from '../../../utils/tradeStatus';
import { useChartPalette, withAlpha, type ChartPalette } from '../../../utils/theme-colors';
import type { Candle, Trade } from '../../../types/api';
import {
  candlesForWindow,
  candleWindow,
  MAX_CANDLES,
  resampleTicks,
  tickRequestWindow,
  ticksReachEntry,
  ticksRetained,
  tickWindow,
  tradeMoments,
  type RawTick,
  type TickWindow,
} from './tradeWindow';
import { contrastText } from './engine/colors';
import { useChartFonts } from './engine/fonts';
import { buildZoneInfo, priceText } from './engine/format';
import { paintMain, type MainStyle, type PriceLevel } from './engine/mainPainter';
import { clipTo, fillPaint, strokePaint, tag } from './engine/paint';
import { yOf } from './engine/scale';
import { UTC_ZONE, type SeriesData, type View as ChartView, type ZoneInfo } from './engine/types';
import { useChartEngine, type ChartEngine } from './engine/useChartEngine';
import { Pulse } from './Pulse';
import { timeToLogicalTimes } from './timeProjection';
import { clamp } from './useFrameLoop';

/**
 * The trade drawn against the price it was decided on (Deriv's "Contract
 * details").
 *
 * The line - from ticks while the server still has them, from one-minute
 * candles after that - with the trade's moments marked on it the way the
 * details panel lists them:
 *
 *   start      a dashed vertical at the entry, a stopwatch at its foot;
 *   entry spot a hollow circle on the price, and the entry price carried
 *              across as the barrier the result is measured against;
 *   exit spot  a filled dot, with its price in the colour of the result;
 *   expiry     a solid vertical with a flag at its foot.
 *
 * A running trade keeps drawing: the price is live and a countdown hangs on
 * the expiry line. Nothing here is an input to anything - the entry, the exit
 * and the result are the platform's figures, drawn where they happened.
 *
 * It sizes itself - a fixed-height plot across the full width - because the
 * trade screen lays it out inside a scrolling page that gives it no height.
 */
interface Props {
  trade: Trade;
  precision: number;
  zone: string;
}

type WindowData =
  | { kind: 'ticks'; ticks: RawTick[]; window: TickWindow }
  | { kind: 'candles'; candles: Candle[] }
  | { kind: 'none' };

/** How long each resample of a live line waits for more ticks. */
const LIVE_REDRAW_MS = 500;

/** The plot's height: the trade screen scrolls, so the chart brings its own. */
const PLOT_HEIGHT = 240;

/**
 * Fetches the window, choosing the source by age.
 *
 * Written to degrade against a server that does not yet understand `from` and
 * `to`: ticks are asked for with a trailing window long enough to reach the
 * trade and cut to it here, and candles are checked for actually containing
 * the trade - a server that ignored `to` answers with today's candles, and
 * drawing a trade on those would be drawing it on the wrong prices.
 */
async function loadWindow(trade: Trade): Promise<WindowData> {
  const now = Date.now();
  const window = tickWindow(trade);

  if (ticksRetained(window.from, now)) {
    try {
      const raw = await api.market.ticks(trade.assetId, {
        from: window.from,
        to: Math.min(window.to, now),
        windowMs: tickRequestWindow(window.from, now),
      });
      const ticks = raw
        .map((tick) => ({ timestamp: Number(tick.timestamp), price: Number(tick.price) }))
        .filter((tick) => Number.isFinite(tick.timestamp) && Number.isFinite(tick.price) && tick.timestamp <= window.to);
      if (ticksReachEntry(ticks, trade.entryTimestamp)) return { kind: 'ticks', ticks, window };
    } catch {
      // Candles are the fallback either way.
    }
  }

  const plan = candleWindow(trade, now);
  let candles = candlesForWindow(await api.market.candles(trade.assetId, plan.limit, 60, plan.to), plan, trade.entryTimestamp);
  if (!candles && plan.limit < MAX_CANDLES) {
    candles = candlesForWindow(await api.market.candles(trade.assetId, MAX_CANDLES, 60), plan, trade.entryTimestamp);
  }
  return candles ? { kind: 'candles', candles } : { kind: 'none' };
}

export function TradeWindowChart({ trade, precision, zone }: Props) {
  const palette = useChartPalette();
  const running = trade.status === 'OPEN' || trade.status === 'SETTLING';
  const query = useQuery({
    queryKey: ['trade-window', trade.id],
    queryFn: () => loadWindow(trade),
    // The past does not change. A running trade drawn from ticks is kept
    // current from the feed; one drawn from candles - a long trade whose
    // start has left the tick history - asks again every candle or so, so
    // its chart does not stop at the moment the page was opened.
    staleTime: Infinity,
    refetchInterval: (current) => (running && current.state.data?.kind === 'candles' ? 15_000 : false),
    refetchOnWindowFocus: false,
    retry: 1,
  });

  // Owned here rather than by the canvas, so the zoom buttons can sit in the
  // bar under the chart instead of on top of the price.
  const engine = useChartEngine({
    initialSpacing: 6,
    restingOffset: 0,
    minSpacing: 0.2,
    maxSpacing: 50,
    axisH: 28,
    margins: { top: 0.16, bottom: 0.14 },
    onTap: (x, y) => {
      if (engine.crosshair.value.visible) engine.hideCrosshair();
      else engine.showCrosshairAt(x, y);
    },
  });

  const data = query.data;
  return (
    <View className="w-full">
      <View className="relative w-full" style={{ height: PLOT_HEIGHT }}>
        {query.isLoading ? (
          <View className="absolute inset-0 p-4">
            <Skeleton className="h-full w-full" />
          </View>
        ) : query.isError ? (
          <ChartMessage
            title="The price history could not be loaded"
            body="The trade itself is unaffected - its figures are on this page."
            action={
              <Button variant="ghost" size="sm" icon={RefreshCw} className="mt-3" onPress={() => void query.refetch()}>
                Try again
              </Button>
            }
          />
        ) : !data || data.kind === 'none' ? (
          <ChartMessage
            title="Chart not available for this trade"
            body="The prices for this period are no longer kept. The trade's entry, expiry and result are listed on this page."
          />
        ) : (
          <WindowCanvas
            // The series is built for one kind of data; a change of source
            // gets a fresh canvas rather than candles poured into a line.
            key={trade.id + ':' + data.kind}
            trade={trade}
            data={data}
            precision={precision}
            zone={zone}
            palette={palette}
            engine={engine}
          />
        )}
      </View>

      {/* What the markers are, where the line comes from, and the zoom -
          under the chart, where none of it can cover the price. */}
      <View className="flex-row flex-wrap items-center gap-x-3 gap-y-2 border-t border-surface-800 px-4 py-2">
        <Legend data={data} palette={palette} />
        {data && data.kind !== 'none' ? <WindowZoom engine={engine} /> : null}
      </View>
    </View>
  );
}

function ChartMessage({ title, body, action }: { title: string; body: string; action?: ReactNode }) {
  return (
    <View className="absolute inset-0 items-center justify-center px-6" accessibilityRole="text">
      <Text className="text-center text-sm font-semibold text-content-200">{title}</Text>
      <Text className="mt-1 max-w-sm text-center text-xs leading-relaxed text-content-400">{body}</Text>
      {action}
    </View>
  );
}

function Legend({ data, palette }: { data: WindowData | undefined; palette: ChartPalette }) {
  // Where the line comes from, so a trader comparing two trades knows why one
  // is a smooth line and the other is candles.
  const source =
    data?.kind === 'ticks'
      ? 'Tick prices, every ' + String(data.window.stepMs / 1000) + ' s'
      : data?.kind === 'candles'
        ? 'One-minute candles'
        : null;
  return (
    <View className="min-w-0 flex-1 flex-row flex-wrap items-center gap-x-4 gap-y-1">
      <View className="flex-row items-center gap-1.5">
        <Icon as={Timer} size={14} className="text-content-400" />
        <Text className="text-[11px] text-content-400">Start</Text>
      </View>
      <View className="flex-row items-center gap-1.5">
        <View className="h-3 w-3 rounded-full border-2" style={{ borderColor: palette.strongText }} />
        <Text className="text-[11px] text-content-400">Entry spot</Text>
      </View>
      <View className="flex-row items-center gap-1.5">
        <View className="h-3 w-3 rounded-full" style={{ backgroundColor: palette.strongText }} />
        <Text className="text-[11px] text-content-400">Exit spot</Text>
      </View>
      <View className="flex-row items-center gap-1.5">
        <Icon as={Flag} size={14} className="text-content-400" />
        <Text className="text-[11px] text-content-400">Expiry</Text>
      </View>
      {source ? <Text className="w-full text-[10px] text-content-400">{source}</Text> : null}
    </View>
  );
}

interface CanvasProps {
  trade: Trade;
  data: Exclude<WindowData, { kind: 'none' }>;
  precision: number;
  zone: string;
  palette: ChartPalette;
  engine: ChartEngine;
}

/** Where the trade's moments land on the plot this frame; null where one is off it. */
interface Marks {
  pw: number;
  ph: number;
  startX: number | null;
  expiryX: number | null;
  entryY: number | null;
  exitX: number | null;
  exitY: number | null;
}

function WindowCanvas({ trade, data, precision, zone, palette, engine }: CanvasProps) {
  const tradeRef = useRef(trade);
  tradeRef.current = trade;
  const liveTicksRef = useRef<RawTick[]>([]);

  const moments = tradeMoments(trade);
  const running = trade.status === 'OPEN';
  const windowEnd = data.kind === 'ticks' ? data.window.to : Number.NaN;
  // Live while the window is still being drawn: a running trade, or one that
  // ended moments ago and still has its margin to fill.
  const [live, setLive] = useState(() => data.kind === 'ticks' && Date.now() < windowEnd);
  useMarketSubscription(live ? trade.assetId : null);

  // A past window has no "last price": the newest candle here closed long
  // ago, and a live-looking price tab on it would be a lie.
  useEffect(() => {
    const resting = data.kind === 'ticks' ? 0 : 2;
    engine.restingOffset.value = resting;
    engine.rightOffset.value = resting;
    engine.closeOnly.value = data.kind === 'ticks';
    engine.minMove.value = 1 / 10 ** precision;
    engine.hideCrosshair();
    // Set up once per data source; the page keys this component on it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const style = useSharedValue<MainStyle | null>(null);
  const levels = useSharedValue<PriceLevel[]>([]);
  const zoneInfo = useSharedValue<ZoneInfo>(UTC_ZONE);

  // Colours follow the theme; the entry price is carried across as the barrier.
  useEffect(() => {
    const ticks = data.kind === 'ticks';
    const barrier = withAlpha(palette.strongText, 0.7);
    style.value = {
      kind: ticks ? 'area' : 'candles',
      background: palette.background,
      grid: withAlpha(palette.grid, 0.6),
      text: palette.text,
      border: palette.border,
      crosshair: palette.crosshair,
      crosshairText: contrastText(palette.crosshair),
      bull: palette.bull,
      bear: palette.bear,
      bullText: contrastText(palette.bull),
      bearText: contrastText(palette.bear),
      accent: palette.strongText,
      accentText: contrastText(palette.strongText),
      accentTop: withAlpha(palette.strongText, 0.16),
      accentBottom: withAlpha(palette.strongText, 0.01),
      priceLine: palette.priceLine,
      watermark: null,
      digits: precision,
      // The live price's own line and tag only while there is a live price.
      lastPrice: ticks && trade.status === 'OPEN',
      magnet: false,
    };
    levels.value = [{ price: trade.entryPrice, color: barrier, textColor: contrastText(palette.strongText), title: 'Entry' }];
  }, [palette, data.kind, trade.entryPrice, trade.status, precision, style, levels]);

  const fonts = useChartFonts();
  const axisFont = fonts.axis;
  const boldFont = fonts.bold;

  /** Lays the data onto the chart; for ticks, resampled with whatever has arrived live since. */
  const draw = useCallback(
    (fit: boolean) => {
      const now = Date.now();
      const current = tradeRef.current;
      const currentMoments = tradeMoments(current);
      let next: SeriesData;

      if (data.kind === 'ticks') {
        const pins = [{ time: currentMoments.entry, price: currentMoments.entryPrice }];
        if (currentMoments.exit !== null && currentMoments.exitPrice !== null) {
          pins.push({ time: currentMoments.exit, price: currentMoments.exitPrice });
        }
        const points = resampleTicks([...data.ticks, ...liveTicksRef.current], data.window, current.entryTimestamp, now, pins);
        const values = points.map((point) => (point.value === undefined ? Number.NaN : point.value));
        next = { t: points.map((point) => point.time), o: values, h: values.slice(), l: values.slice(), c: values.slice(), bucket: data.window.stepMs };
      } else {
        next = {
          t: data.candles.map((candle) => candle.time),
          o: data.candles.map((candle) => candle.open),
          h: data.candles.map((candle) => candle.high),
          l: data.candles.map((candle) => candle.low),
          c: data.candles.map((candle) => candle.close),
          bucket: 60_000,
        };
      }
      engine.series.value = next;
      if (next.t.length > 0) {
        const resolved = resolveZone(zone);
        zoneInfo.value = buildZoneInfo(resolved, next.t[0]! - 86_400_000, next.t[next.t.length - 1]! + 86_400_000);
        let widest = 0;
        for (const value of next.h) if (value === value) widest = Math.max(widest, Math.abs(value));
        widest = Math.max(widest, Math.abs(current.entryPrice));
        engine.axisW.value = Math.ceil(Math.max(44, axisFont.getTextWidth(priceText(widest, precision)) + 16));
      }
      if (fit) engine.fitContent();
    },
    [data, engine, zone, precision, axisFont, zoneInfo],
  );

  /**
   * Whether this chart has been fitted to the whole trade yet. Once it has, a
   * refresh keeps whatever view the trader zoomed to.
   */
  const fittedRef = useRef(false);
  useEffect(() => {
    draw(!fittedRef.current);
    fittedRef.current = true;
  }, [draw]);

  // A running window keeps collecting ticks from the feed and redraws twice a
  // second, then stops once its margin after the expiry has filled.
  useEffect(() => {
    if (!live) return;
    const assetId = trade.assetId;
    const unsubscribe = useMarketStore.subscribe((state, previous) => {
      const tick = state.ticks[assetId];
      if (!tick || tick === previous.ticks[assetId]) return;
      const buffer = liveTicksRef.current;
      if (buffer.length === 0 || tick.timestamp > buffer[buffer.length - 1]!.timestamp) {
        buffer.push({ timestamp: tick.timestamp, price: tick.price });
      }
    });
    const timer = setInterval(() => {
      draw(false);
      if (Date.now() >= windowEnd) setLive(false);
    }, LIVE_REDRAW_MS);
    return () => {
      unsubscribe();
      clearInterval(timer);
    };
  }, [live, trade.assetId, draw, windowEnd]);

  // A settlement arriving while the page is open lands on the line at once.
  useEffect(() => {
    if (data.kind === 'ticks') draw(false);
  }, [trade.status, trade.expiryPrice, draw, data.kind]);

  /* The markers, projected each frame so they follow pan, zoom and the live line. */
  const { view, series, crosshair } = engine;
  const entryTime = moments.entry;
  const expiryTime = moments.expiry;
  const entryPrice = moments.entryPrice;
  const exitTime = moments.exit;
  const exitPrice = moments.exitPrice;
  const marks = useDerivedValue<Marks>(() => {
    const current = view.value;
    const slots = series.value;
    const x = (ms: number | null): number | null => {
      'worklet';
      if (ms === null || !current.ok) return null;
      const logical = timeToLogicalTimes(ms, slots.t, slots.bucket);
      return logical === null ? null : current.x0 + logical * current.s;
    };
    const y = (price: number | null): number | null => {
      'worklet';
      return price === null || !current.ok ? null : yOf(current, price);
    };
    return {
      pw: current.pw,
      ph: current.ph,
      startX: x(entryTime),
      expiryX: x(expiryTime),
      entryY: y(entryPrice),
      exitX: x(exitTime),
      exitY: y(exitPrice),
    };
  }, [entryTime, expiryTime, entryPrice, exitTime, exitPrice]);

  const tone = statusTone(trade);
  const resultColour = tone === 'up' ? palette.up : tone === 'down' ? palette.down : palette.strongText;
  const ink = palette.strongText;
  const background = palette.background;
  const textColour = palette.text;
  const panel = palette.panel;
  const exitClock = exitTime === null ? '' : formatClock(exitTime, zone);
  const exitText = exitPrice === null ? '' : formatPrice(exitPrice, precision);

  const picture = useDerivedValue(
    () =>
      createPicture((canvas) => {
        const current = style.value;
        if (!current) return;
        const frame = view.value;
        paintMain(canvas, frame, series.value, current, [], crosshair.value, zoneInfo.value, { axis: axisFont, watermark: null }, { levels: levels.value });
        paintMoments(canvas, frame, marks.value, {
          ink,
          background,
          text: textColour,
          panel,
          result: resultColour,
          exitClock,
          exitText,
          font: axisFont,
          bold: boldFont,
        });
      }),
    [axisFont, boldFont, ink, background, textColour, panel, resultColour, exitClock, exitText],
  );

  // Start: a stopwatch at the foot of its line. Expiry: a flag, and while
  // running, the time left at the top.
  const startStyle = useFootStyle(marks, 'startX');
  const expiryStyle = useFootStyle(marks, 'expiryX');
  const countdownStyle = useAnimatedStyle(() => {
    const m = marks.value;
    const visible = m.expiryX !== null && m.expiryX >= -1 && m.expiryX <= m.pw + 1;
    return {
      opacity: visible ? 1 : 0,
      transform: [{ translateX: visible ? clamp(m.expiryX!, 0, m.pw) : -9999 }],
    };
  });

  return (
    <View className="absolute inset-0">
      <View
        className="absolute inset-0"
        onLayout={(event) => {
          engine.size.value = { w: event.nativeEvent.layout.width, h: event.nativeEvent.layout.height };
        }}
      >
        <GestureDetector gesture={engine.gesture}>
          <View style={StyleSheet.absoluteFill} collapsable={false}>
            <Canvas style={StyleSheet.absoluteFill}>
              <Picture picture={picture} />
            </Canvas>
          </View>
        </GestureDetector>
      </View>

      {/* Everything that must stop at the plot's edge. */}
      <View pointerEvents="none" className="absolute inset-0 overflow-hidden" accessibilityElementsHidden importantForAccessibility="no-hide-descendants">
        <Animated.View className="absolute left-0 top-0" style={startStyle}>
          <FootBadge palette={palette}>
            <Icon as={Timer} size={14} color={palette.strongText} />
          </FootBadge>
        </Animated.View>
        <Animated.View className="absolute left-0 top-0" style={expiryStyle}>
          <FootBadge palette={palette}>
            <Icon as={Flag} size={14} color={palette.strongText} />
          </FootBadge>
        </Animated.View>
        {running ? (
          <Animated.View className="absolute left-0 top-2" style={countdownStyle}>
            <Countdown expiry={moments.expiry} palette={palette} />
          </Animated.View>
        ) : null}
      </View>
    </View>
  );
}

/** A marker's badge, centred on its line just above the time axis. */
function useFootStyle(marks: { value: Marks }, key: 'startX' | 'expiryX') {
  return useAnimatedStyle(() => {
    const m = marks.value;
    const x = m[key];
    // One off the plot is hidden rather than pinned to an edge it is not at.
    const visible = x !== null && x >= -1 && x <= m.pw + 1;
    return {
      opacity: visible ? 1 : 0,
      transform: [{ translateX: visible ? clamp(x!, 0, m.pw) - 12 : -9999 }, { translateY: Math.max(0, m.ph - 4 - 24) }],
    };
  });
}

function FootBadge({ palette, children }: { palette: ChartPalette; children: ReactNode }) {
  return (
    <View className="h-6 w-6 items-center justify-center rounded-full" style={{ backgroundColor: withAlpha(palette.panel, 0.9) }}>
      {children}
    </View>
  );
}

interface MomentStyle {
  ink: string;
  background: string;
  text: string;
  panel: string;
  result: string;
  exitClock: string;
  exitText: string;
  font: SkFont;
  bold: SkFont;
}

/** The start and expiry lines, and the entry and exit spots with the exit's labels. */
function paintMoments(canvas: SkCanvas, view: ChartView, marks: Marks, style: MomentStyle): void {
  'worklet';
  if (!view.ok) return;
  const onPlot = (x: number | null) => {
    'worklet';
    return x !== null && x >= -1 && x <= marks.pw + 1;
  };

  canvas.save();
  clipTo(canvas, 0, 0, marks.pw, marks.ph);
  if (onPlot(marks.startX)) {
    const x = Math.round(clamp(marks.startX!, 0, marks.pw)) + 0.5;
    const dashed = strokePaint(style.ink, 1, [5, 4]);
    dashed.setAlphaf(dashed.getAlphaf() * 0.75);
    canvas.drawLine(x, 0, x, marks.ph, dashed);
  }
  if (onPlot(marks.expiryX)) {
    const x = Math.round(clamp(marks.expiryX!, 0, marks.pw)) + 0.5;
    const solid = strokePaint(style.ink, 1);
    solid.setAlphaf(solid.getAlphaf() * 0.8);
    canvas.drawLine(x, 0, x, marks.ph, solid);
  }
  canvas.restore();

  // The spots sit over the line and outside the clip, so a dot at the very
  // edge is drawn whole.
  const spot = (x: number | null, y: number | null) => {
    'worklet';
    return x !== null && y !== null && x >= -1 && x <= marks.pw + 1 && y >= -1 && y <= marks.ph + 1;
  };
  if (spot(marks.startX, marks.entryY)) {
    canvas.drawCircle(marks.startX!, marks.entryY!, 6, fillPaint(style.background));
    canvas.drawCircle(marks.startX!, marks.entryY!, 6, strokePaint(style.ink, 2));
  }
  if (spot(marks.exitX, marks.exitY)) {
    const ring = fillPaint(style.background);
    ring.setAlphaf(ring.getAlphaf() * 0.9);
    canvas.drawCircle(marks.exitX!, marks.exitY!, 10, ring);
    canvas.drawCircle(marks.exitX!, marks.exitY!, 7, fillPaint(style.result));
    if (style.exitText) {
      // The exit's price in the result's colour, and its time above it.
      const chipCentre = marks.exitY! - 12 - 9;
      tag(canvas, style.bold, style.exitText, marks.exitX!, chipCentre, 'center', style.result, style.panel, { padX: 8, height: 18, radius: 6 });
      if (style.exitClock) {
        const clockBg = withAlphaColour(style.background, 0.8);
        tag(canvas, style.font, style.exitClock, marks.exitX!, chipCentre - 9 - 2 - 7, 'center', clockBg, style.text, { padX: 4, height: 14, radius: 3 });
      }
    }
  }
}

/** A colour with an alpha, on the UI thread: hex in, rgba out. */
function withAlphaColour(color: string, alpha: number): string {
  'worklet';
  if (color.startsWith('#') && color.length === 7) {
    const r = parseInt(color.slice(1, 3), 16);
    const g = parseInt(color.slice(3, 5), 16);
    const b = parseInt(color.slice(5, 7), 16);
    return 'rgba(' + r + ', ' + g + ', ' + b + ', ' + alpha + ')';
  }
  return color;
}

/** The time left on a running trade, at the top of its expiry line. */
function Countdown({ expiry, palette }: { expiry: number; palette: ChartPalette }) {
  const remaining = useCountdown(expiry);
  const urgent = remaining <= 10_000;
  const [width, setWidth] = useState(0);
  return (
    <View style={{ transform: [{ translateX: -width / 2 }] }} onLayout={(event) => setWidth(event.nativeEvent.layout.width)}>
      <Pulse active={urgent}>
        <View
          className="rounded-md px-1.5 py-0.5"
          style={{
            backgroundColor: withAlpha(palette.panel, 0.92),
            borderWidth: 1,
            borderColor: withAlpha(palette.brand, 0.6),
          }}
        >
          <Text className="text-[11px] font-bold tnum" style={{ color: urgent ? palette.brand : palette.strongText }}>
            {formatCountdown(remaining)} left
          </Text>
        </View>
      </Pulse>
    </View>
  );
}

/** Zoom out, fit the whole trade, zoom in - the same three a trader has on the trade screen. */
function WindowZoom({ engine }: { engine: ChartEngine }) {
  const zoom = (factor: number) => {
    // About the middle of the view, never narrower than a handful of slots.
    const current = engine.readView();
    if (!current.ok) return;
    const visible = current.pw / current.s;
    const half = Math.max(2, visible / 2 / factor);
    const next = clamp(current.pw / (half * 2), engine.minSpacing, engine.maxSpacing);
    const centre = current.pw / 2;
    const logical = (centre - current.x0) / current.s;
    const x0 = centre - logical * next;
    engine.spacing.value = next;
    engine.rightOffset.value = (current.pw - x0) / next - (current.count - 1) - 0.5;
  };
  return (
    <View accessibilityRole="toolbar" accessibilityLabel="Chart zoom" className="ml-auto flex-row gap-1">
      <ZoomButton label="Zoom out" onPress={() => zoom(1 / 1.4)}>
        <Icon as={Minus} size={16} className="text-content-300" />
      </ZoomButton>
      <ZoomButton label="Show the whole trade" onPress={engine.fitContent}>
        <Icon as={Maximize} size={16} className="text-content-300" />
      </ZoomButton>
      <ZoomButton label="Zoom in" onPress={() => zoom(1.4)}>
        <Icon as={Plus} size={16} className="text-content-300" />
      </ZoomButton>
    </View>
  );
}

function ZoomButton({ label, onPress, children }: { label: string; onPress: () => void; children: ReactNode }) {
  return (
    <Pressable
      onPress={onPress}
      accessibilityRole="button"
      accessibilityLabel={label}
      className="h-9 w-9 items-center justify-center rounded-lg border border-surface-700 bg-surface-850 active:border-surface-600"
    >
      {children}
    </Pressable>
  );
}
