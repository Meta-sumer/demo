import { useCallback, useEffect, useMemo, useRef, useState, type RefObject } from 'react';
import { Pressable, View } from 'react-native';
import { BlurStyle, createPicture, Skia, TileMode, type SkCanvas, type SkFont } from '@shopify/react-native-skia';
import Animated, {
  runOnJS,
  useAnimatedReaction,
  useAnimatedStyle,
  useDerivedValue,
  useSharedValue,
  type DerivedValue,
  type SharedValue,
} from 'react-native-reanimated';
import { ArrowDown, ArrowUp } from 'lucide-react-native';
import { Icon, Text } from '../../../components/ui';
import { useMarketStore } from '../../../stores/marketStore';
import { useSettingsStore } from '../../../stores/settingsStore';
import { useTradeStore } from '../../../stores/tradeStore';
import { useAccountStore } from '../../../stores/accountStore';
import { accountOf } from '../ticketModel';
import { useOrderPreviewStore } from '../../../stores/orderPreviewStore';
import { formatCountdown, formatMoney, formatMoneyCompact, formatPrice, formatSignedMoney } from '../../../utils/format';
import { formatClock } from '../../../utils/time';
import { withAlpha, type ChartPalette } from '../../../utils/theme-colors';
import { cn } from '../../../utils/cn';
import {
  buildOverlayOrders,
  isInTheMoney,
  needsHeartbeat,
  orderTone,
  stateLabel,
  type OverlayOrder,
  type OrderVisualState,
} from './orderModel';
import { useOrderProjection, type AnchorSource, type OrderGeometry } from './useOrderProjection';
import { TAP_PRIORITY, type ChartHost } from '../chart/engine/host';
import { useChartFonts } from '../chart/engine/fonts';
import { priceText } from '../chart/engine/format';
import { clipTo, fillPaint, roundStroke, strokePaint, tag } from '../chart/engine/paint';
import { pulseAt } from '../chart/Pulse';
import { useFrameClock } from '../chart/useFrameLoop';
import type { Asset } from '../../../types/api';

/**
 * The order overlay.
 *
 * Orders are drawn as a layer of their own rather than into the series. An
 * order needs a line that stops at its own expiry, a tag that can be pressed,
 * a vertical line at an instant no series has a point for, and six visual
 * states. Keeping it apart also keeps the promise that pressing an order
 * cannot disturb a candle.
 *
 * The lines, the band, the countdowns and the price tabs are recorded into
 * the chart's canvas; the amount tags are views, because they carry the
 * currency symbol and are the thing a finger presses. Both read the same
 * projection on the UI thread, so they move together with the candles.
 *
 * Two clocks drive it, deliberately kept apart:
 *
 *   - a one-second heartbeat re-renders the countdowns and retires finished
 *     orders. It only runs while something is counting down;
 *   - the projection (`useOrderProjection`) moves everything else, on the UI
 *     thread, so the chart's own price and time axes can move under an order
 *     without React being involved at all.
 */
interface Props {
  host: ChartHost;
  asset: Asset | null;
  palette: ChartPalette;
  zone: string;
  barsRef: RefObject<ReadonlyArray<{ time: number }>>;
  bucketMs: number;
  /** The trader has chosen to hide their positions from the chart. */
  hideTrades: boolean;
  /** Settings' "order labels": the state chips beside each marker. */
  showLabels: boolean;
  performanceMode: boolean;
  /**
   * The duration the expiry guide is showing, or null when it is off. Its
   * line needs room ahead of the candles just as an order's expiry does.
   */
  guideSeconds: number | null;
  /**
   * A drawing tool is waiting for taps on this chart. The markers stop
   * catching them until it is done: a level drawn at an order's own price
   * would otherwise open the order's card instead of being placed.
   */
  passThrough?: boolean;
}

/**
 * Empty bars kept to the right of the last one.
 *
 * `BASE` matches what the chart is created with. An expiry further out than
 * that needs the gap widened or its line would sit clamped against the right
 * edge, which tells the trader nothing about when the order actually ends.
 * `MAX` stops a long expiry on a short timeframe from squeezing the candles
 * into a sliver.
 */
const BASE_RIGHT_OFFSET = 6;
const MAX_RIGHT_OFFSET = 42;
/**
 * The gap may never take more than this share of the plot. Forty-two bars is
 * a sliver on a desktop but the whole width of a phone, and there it would
 * push every candle off screen to make room for an expiry line.
 */
const MAX_RIGHT_SHARE = 0.5;

/**
 * Bars beyond the guide's own span. Its expiry is the duration from now, and
 * now is anywhere inside the newest bar, so the span is up to a bar longer
 * than the duration; one more keeps the line off the edge.
 */
const GUIDE_PADDING_BARS = 2;

/**
 * The open area an order needs to its right.
 *
 * Bars ahead alone is not enough. On the default frame an expiry one bar out
 * is, truthfully, ten pixels away - correct, and useless: the expiry line, the
 * countdown and the direction glyph all land on top of the last candle. This
 * floor buys the order somewhere to be drawn, and it is dropped again the
 * moment nothing is pending.
 */
const ORDER_RIGHT_OFFSET = 18;

/** The card's width, so it can be placed. */
const CARD_WIDTH = 220;
/** Clearance the card keeps from the marker and from the plot's edges. */
const CARD_GAP = 12;

/** The triangle that caps the run at the expiry. */
const CAP_HEIGHT = 7;
const CAP_HALF_WIDTH = 5;

/** Clearance between the entry dot and its tag. */
const TAG_GAP = 10;
/** Stands in for the tag's width until it has been laid out and measured. */
const TAG_FALLBACK_WIDTH = 96;
/** How far from the line a tap still counts as on it - a one-pixel line is not a target. */
const LINE_REACH = 7;

/** One order as the UI thread draws it: where it is anchored, and how it looks. */
interface OrderItem extends AnchorSource {
  state: OrderVisualState;
  preview: boolean;
  live: boolean;
  up: boolean;
  hovered: boolean;
  accent: string;
  bright: string;
  soft: string;
  faint: string;
  glow: string;
  tagText: string;
  expiryColor: string;
  opacity: number;
  dashed: boolean;
  fadeAt: number | null;
  countdown: string;
  urgent: boolean;
  countdownColor: string;
  countdownBorder: string;
  chipBackground: string;
  /** The entry price as text; null while it follows the live price. */
  pill: string | null;
}

interface Flip {
  left: number;
  y: 'up' | 'down';
}

export function ChartOrderLayer({
  host,
  asset,
  palette,
  zone,
  barsRef,
  bucketMs,
  hideTrades,
  showLabels,
  performanceMode,
  guideSeconds,
  passThrough = false,
}: Props) {
  const { engine } = host;
  const assetId = asset?.id ?? null;
  const digits = asset?.pricePrecision ?? 5;

  // Only the account being traded: a demo position drawn over a live chart
  // would read as live money at risk.
  const account = useAccountStore((s) => s.active);
  const allOpen = useTradeStore((s) => s.openTrades);
  const allSettled = useTradeStore((s) => s.recentlySettled);
  const openTrades = useMemo(() => allOpen.filter((trade) => accountOf(trade) === account), [allOpen, account]);
  const settled = useMemo(() => allSettled.filter((trade) => accountOf(trade) === account), [allSettled, account]);
  const preview = useOrderPreviewStore((s) => s.preview);

  /**
   * When this client first saw each settled trade.
   *
   * The fade is timed from the moment it arrived here, not from the server's
   * `settledAt`: a client clock that runs a few seconds behind would otherwise
   * retire a result before it had been on screen at all.
   *
   * Recorded while deriving rather than in an effect. An effect runs after the
   * render that needed it, so the first render holding a freshly settled trade
   * would find no timestamp and skip it - and by then the order is no longer
   * open, so the heartbeat that would have picked it up a second later has
   * already stopped. The result would simply never appear. Writing the id here
   * is idempotent, so repeating the render is harmless.
   */
  const seenAtRef = useRef(new Map<string, number>());

  const [heartbeat, setHeartbeat] = useState(() => Date.now());
  /**
   * The marker held open by a tap. Touch has no hover, so a tap is the only
   * way into an order's card: on its tag, or on its line.
   */
  const [pinnedId, setPinnedId] = useState<string | null>(null);
  const [flip, setFlip] = useState<Flip>({ left: CARD_GAP, y: 'up' });

  const orders = useMemo(() => {
    const seen = seenAtRef.current;
    const now = Date.now();
    const live = new Set(settled.map((trade) => trade.id));
    for (const trade of settled) if (!seen.has(trade.id)) seen.set(trade.id, now);
    for (const id of seen.keys()) if (!live.has(id)) seen.delete(id);

    return buildOverlayOrders({
      assetId,
      preview,
      openTrades: hideTrades ? [] : openTrades,
      settled: hideTrades ? [] : settled,
      seenAt: seen,
      now: heartbeat,
    });
  }, [assetId, preview, openTrades, settled, hideTrades, heartbeat]);

  // The heartbeat exists only to advance countdowns and retire finished
  // orders. With an empty chart it does not run, so an idle workspace is
  // genuinely idle.
  const beating = needsHeartbeat(orders);
  useEffect(() => {
    if (!beating) return;
    const timer = setInterval(() => setHeartbeat(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [beating]);

  const ordersRef = useRef<OverlayOrder[]>(orders);
  ordersRef.current = orders;

  /**
   * Make room on the right for the furthest pending expiry.
   *
   * Changing the resting offset scrolls the chart, so this has to be careful
   * about when it fires. Two rules keep it from wrestling the trader for the
   * view:
   *
   *   - it never touches a chart that has been panned away from the live edge.
   *     Someone reading back through history is not asking for the viewport to
   *     be adjusted around an order they can no longer see;
   *   - while orders are open the gap only ever widens. The bars remaining
   *     until an expiry fall by one every bar, so a figure recomputed from
   *     them would shrink continuously and re-scroll the chart each time. It
   *     drops back to the resting gap once, when the last order clears.
   *
   * The expiry guide needs the same room, but its expiry is always the same
   * distance ahead of now, so what it asks for depends only on the duration
   * and the frame. It is part of the resting gap: steady while nothing
   * changes, and it follows a change of duration straight away.
   */
  const autoScroll = useSettingsStore((s) => s.autoScroll);
  const rightOffsetRef = useRef(BASE_RIGHT_OFFSET);
  useEffect(() => {
    const applied = rightOffsetRef.current;
    // A pan in progress leaves fractional drift; a bar and a half of slack
    // tells that apart from a deliberate scroll into history.
    if (Math.abs(engine.rightOffset.value - applied) >= 1.5) return;

    const bars = barsRef.current;
    const last = bars && bars.length > 0 ? bars[bars.length - 1] : undefined;

    const pending = orders.filter((order) => order.state === 'ACTIVE' || order.state === 'PREVIEW');

    let needed = BASE_RIGHT_OFFSET;
    if (autoScroll && guideSeconds !== null && bucketMs > 0) {
      needed = Math.max(needed, Math.ceil((guideSeconds * 1000) / bucketMs) + GUIDE_PADDING_BARS);
    }
    if (autoScroll && pending.length > 0) {
      // Hold whatever has already been made, then widen if an expiry needs it.
      needed = Math.max(needed, applied, ORDER_RIGHT_OFFSET);
      if (last && bucketMs > 0) {
        for (const order of pending) {
          // Two bars of padding so the line is never flush with the edge.
          const ahead = Math.ceil((order.expiryTimestamp - last.time) / bucketMs) + 2;
          needed = Math.max(needed, ahead);
        }
      }
    }
    const view = engine.readView();
    const share = view.s > 0 && view.pw > 0 ? Math.floor((view.pw * MAX_RIGHT_SHARE) / view.s) : MAX_RIGHT_OFFSET;
    needed = Math.min(needed, MAX_RIGHT_OFFSET, Math.max(BASE_RIGHT_OFFSET, share));

    if (needed === applied) return;
    rightOffsetRef.current = needed;
    engine.setRestingOffset(needed);
  }, [orders, autoScroll, bucketMs, engine, barsRef, guideSeconds]);

  /**
   * The live price, mirrored onto the UI thread without a render.
   *
   * A preview's line sits on the market until the order is committed, which
   * means it has to move at tick rate. Subscribing would re-render the whole
   * layer several times a second; writing the price into a shared value keeps
   * that movement entirely on the UI thread.
   */
  const livePrice = useSharedValue<number | null>(null);
  useEffect(() => {
    if (!assetId) {
      livePrice.value = null;
      return;
    }
    livePrice.value = useMarketStore.getState().ticks[assetId]?.price ?? null;
    return useMarketStore.subscribe((state, previous) => {
      const tick = state.ticks[assetId];
      if (tick !== previous.ticks[assetId]) livePrice.value = tick?.price ?? null;
    });
  }, [assetId, livePrice]);
  const fallbackPrice = asset?.feed.lastPrice ?? null;

  const urgentOrFading = orders.some(
    (order) =>
      order.fadeAt !== null ||
      ((order.state === 'ACTIVE' || order.state === 'PREVIEW') && order.expiryTimestamp - heartbeat <= 10_000),
  );
  const now = useFrameClock(engine, {
    enabled: orders.length > 0,
    maxFps: performanceMode ? 30 : 60,
    continuous: urgentOrFading,
  });

  const items = useSharedValue<OrderItem[]>([]);
  useEffect(() => {
    items.value = orders.map((order) => {
      const tone = orderTone(order, palette);
      const hovered = pinnedId === order.id;
      const isPreview = order.state === 'PREVIEW';
      const live = order.state === 'ACTIVE' || isPreview;
      const remaining = Math.max(0, order.expiryTimestamp - heartbeat);
      const urgent = live && remaining <= 10_000;
      return {
        id: order.id,
        tracksClock: order.tracksClock,
        entryTimestamp: order.entryTimestamp,
        expiryTimestamp: order.expiryTimestamp,
        expirySeconds: order.expirySeconds,
        entryPrice: order.entryPrice,
        state: order.state,
        preview: isPreview,
        live,
        up: order.direction === 'UP',
        hovered,
        accent: tone.accent,
        bright: tone.bright,
        soft: tone.soft,
        faint: withAlpha(tone.accent, 0.03),
        glow: tone.glow,
        tagText: tone.tagText,
        expiryColor: withAlpha(live ? tone.accent : palette.priceLine, hovered ? 0.95 : 0.6),
        opacity: tone.opacity,
        dashed: tone.dashed,
        fadeAt: order.fadeAt,
        countdown: formatCountdown(remaining),
        urgent,
        countdownColor: urgent ? palette.brand : palette.strongText,
        countdownBorder: withAlpha(urgent ? palette.brand : tone.accent, 0.5),
        chipBackground: withAlpha(palette.panel, 0.88),
        pill: order.entryPrice === null ? null : formatPrice(order.entryPrice, digits),
      };
    });
  }, [orders, palette, pinnedId, heartbeat, digits, items]);

  const geometry = useOrderProjection({ engine, sources: items, now, livePrice, fallbackPrice });

  const fonts = useChartFonts();
  const boldFont = fonts.bold;
  const picture = useDerivedValue(
    () =>
      createPicture((canvas) => {
        paintOrders(canvas, items.value, geometry.value, now.value, digits, boldFont);
      }),
    [digits, boldFont],
  );

  useEffect(() => {
    host.setLayer('orders', picture);
    return () => host.setLayer('orders', null);
  }, [host, picture]);

  // An order that settles and fades must not leave a card behind.
  useEffect(() => {
    if (pinnedId && !orders.some((order) => order.id === pinnedId)) setPinnedId(null);
  }, [orders, pinnedId]);

  /** Where the card should open, from the marker's place when it was tapped. */
  const togglePin = useCallback((id: string, geometryNow: OrderGeometry | undefined) => {
    if (geometryNow) {
      /*
       * Measured against the space actually available rather than against a
       * ratio of the plot. On a phone the card often does not fit on either
       * side of the marker, and the only honest answer is to slide it off the
       * marker and keep it on screen. The offset is relative to the entry,
       * which is what the card hangs from.
       */
      const width = Math.min(CARD_WIDTH, Math.max(0, geometryNow.plotWidth - CARD_GAP * 2));
      const roomRight = geometryNow.plotWidth - geometryNow.entryX - CARD_GAP * 2;
      const roomLeft = geometryNow.entryX - CARD_GAP * 2;
      const preferred = roomRight >= width ? CARD_GAP : roomLeft >= width ? -CARD_GAP - width : -width / 2;
      const left = clamp(preferred, CARD_GAP - geometryNow.entryX, geometryNow.plotWidth - CARD_GAP - width - geometryNow.entryX);
      setFlip({ left: Math.round(left), y: geometryNow.y > geometryNow.plotHeight * 0.55 ? 'up' : 'down' });
    }
    setPinnedId((current) => (current === id ? null : id));
  }, []);

  const passThroughRef = useRef(passThrough);
  passThroughRef.current = passThrough;
  const pinnedRef = useRef(pinnedId);
  pinnedRef.current = pinnedId;

  /*
   * Taps on the chart. An order's line is a target of its own, as well as its
   * tag - a second way into an order besides the tag. Tapping anywhere that
   * is not a marker lets go of a held card, and the tap carries on to the
   * drawings and the crosshair underneath.
   */
  useEffect(() => {
    host.setTap('orders', TAP_PRIORITY.orders, (x, y) => {
      if (passThroughRef.current) return false;
      const shapes = geometry.value;
      const list = ordersRef.current;
      for (let i = shapes.length - 1; i >= 0; i -= 1) {
        const shape = shapes[i]!;
        if (shape.hidden) continue;
        const order = list.find((entry) => entry.id === shape.id);
        if (!order) continue;
        const spanning = order.state === 'PREVIEW';
        const left = spanning ? 0 : shape.entryX;
        const right = spanning ? shape.plotWidth : Math.max(shape.entryX + 2, shape.expiryX);
        if (Math.abs(y - shape.y) <= LINE_REACH && x >= left && x <= right) {
          togglePin(shape.id, shape);
          return true;
        }
      }
      if (pinnedRef.current) setPinnedId(null);
      return false;
    });
    host.setPanBegin('orders', () => setPinnedId(null));
    return () => {
      host.setTap('orders', 0, null);
      host.setPanBegin('orders', null);
    };
  }, [host, geometry, togglePin]);

  if (!asset) return null;

  return (
    /*
     * Not clipped: the card has to be able to reach past the plot, which on a
     * short phone chart is most of it. The layer itself takes no touches, so
     * only the markers' own tags are ever in the way.
     */
    <View pointerEvents={passThrough ? 'none' : 'box-none'} className="absolute inset-0" accessibilityElementsHidden={orders.length === 0}>
      {orders.map((order) => (
        <OrderMarker
          key={order.id}
          order={order}
          geometry={geometry}
          now={now}
          palette={palette}
          zone={zone}
          digits={digits}
          heartbeat={heartbeat}
          hovered={pinnedId === order.id}
          showLabel={showLabels}
          flip={flip}
          onToggle={togglePin}
        />
      ))}
    </View>
  );
}

/* ------------------------------------------------------------ the canvas */

/** How visible a finished order still is: it fades over the last half second before it is retired. */
function fadeOf(fadeAt: number | null, now: number): number {
  'worklet';
  if (fadeAt === null) return 1;
  const left = (fadeAt - 100 - now) / 500;
  return left <= 0 ? 0 : left >= 1 ? 1 : left;
}

function layer(canvas: SkCanvas, alpha: number): void {
  'worklet';
  const paint = Skia.Paint();
  paint.setAlphaf(alpha);
  canvas.saveLayer(paint);
}

/**
 * The direction glyph.
 *
 * Hand-drawn rather than an icon: at this size a stroked arrow from the icon
 * set reads as a hairline, and the whole point of it is to be the thing you
 * see without looking for it. Drawn pointing up and mirrored for a down bet,
 * so both directions carry exactly the same weight.
 */
function directionGlyph(canvas: SkCanvas, x: number, y: number, up: boolean, color: string): void {
  'worklet';
  const scale = 44 / 48;
  canvas.save();
  canvas.translate(x, y);
  canvas.scale(scale, up ? scale : -scale);
  canvas.translate(-24, -24);
  const path = Skia.Path.Make();
  path.moveTo(9, 39);
  path.cubicTo(18, 38, 26, 31, 33, 17);
  path.moveTo(25, 12.5);
  path.lineTo(38.5, 11);
  path.lineTo(37.5, 25);
  const paint = roundStroke(strokePaint(color, 9));
  paint.setAlphaf(paint.getAlphaf() * 0.95);
  canvas.drawPath(path, paint);
  canvas.restore();
}

function paintOrders(
  canvas: SkCanvas,
  items: ReadonlyArray<OrderItem>,
  shapes: ReadonlyArray<OrderGeometry>,
  now: number,
  digits: number,
  font: SkFont,
): void {
  'worklet';
  for (let i = 0; i < items.length; i += 1) {
    const item = items[i]!;
    const g = shapes[i];
    if (!g || g.id !== item.id || g.hidden) continue;
    const alpha = item.opacity * fadeOf(item.fadeAt, now);
    if (alpha <= 0.001) continue;
    const pw = g.plotWidth;
    const ph = g.plotHeight;

    layer(canvas, alpha);
    // Everything that has to stop at the plot's edge.
    canvas.save();
    clipTo(canvas, 0, 0, pw, ph);

    if (item.preview) {
      // Direction band. Only a preview gets one: it answers "which way am I
      // betting" at a glance, and on a chart of live orders the same fill
      // repeated would drown the candles.
      const band = fillPaint(item.soft);
      band.setShader(
        Skia.Shader.MakeLinearGradient(
          Skia.Point(0, g.y),
          Skia.Point(0, item.up ? 0 : ph),
          [Skia.Color(item.soft), Skia.Color(item.faint)],
          null,
          TileMode.Clamp,
        ),
      );
      if (item.up) canvas.drawRect(Skia.XYWHRect(0, 0, pw, g.y), band);
      else canvas.drawRect(Skia.XYWHRect(0, g.y, pw, Math.max(0, ph - g.y)), band);

      // The run from the entry out to the edge: bright and solid, where the
      // level itself is only a dashed rule across the whole chart.
      canvas.drawLine(g.entryX, g.y, pw, g.y, roundStroke(strokePaint(item.bright, 2)));

      // Which way the order is betting, in the open ground past the entry -
      // centred in the window when it is wide enough, a fixed step out when
      // it is not, and gone only when the entry is hard against the edge.
      if (pw - g.entryX >= 44) {
        const span = g.expiryX - g.entryX;
        const along = Math.max(span / 2, 52);
        const ax = Math.min(g.entryX + along, pw - 28);
        const lift = g.y + (item.up ? -68 : 68);
        const ay = Math.min(Math.max(lift, 34), Math.max(34, ph - 34));
        directionGlyph(canvas, ax, ay, item.up, item.bright);
      }
    }

    // The entry line. A preview spans the plot, like a price level being
    // considered; a real order stops at its own expiry, because that is where
    // it ends. A real order's run is solid and two pixels, so it reads as a
    // deal rather than as another gridline.
    const left = item.preview ? 0 : g.entryX;
    const right = item.preview ? pw : left + Math.max(2, g.expiryX - g.entryX);
    const width = item.dashed && !item.hovered ? 1 : 2;
    const lineY = g.y + width / 2;
    if (item.hovered) {
      const glow = strokePaint(item.glow, 6);
      glow.setMaskFilter(Skia.MaskFilter.MakeBlur(BlurStyle.Normal, 4, true));
      canvas.drawLine(left, lineY, right, lineY, glow);
    }
    canvas.drawLine(
      left,
      lineY,
      right,
      lineY,
      strokePaint(item.hovered ? item.bright : item.accent, width, item.dashed ? (width === 1 ? [4, 3] : [6, 4]) : null),
    );
    if (!item.preview) {
      // Where the run ends: the expiry, capped.
      const cap = Skia.Path.Make();
      cap.moveTo(right - CAP_HALF_WIDTH, g.y);
      cap.lineTo(right + CAP_HALF_WIDTH, g.y);
      cap.lineTo(right, g.y - CAP_HEIGHT);
      cap.close();
      canvas.drawPath(cap, fillPaint(item.accent));
    }

    // The expiry line and its countdown.
    const expiryAlpha = g.expiryClamped ? 0.45 : 1;
    const ex = Math.round(g.expiryX) + 0.5;
    const rule = strokePaint(item.expiryColor, 1, [5, 5]);
    rule.setAlphaf(rule.getAlphaf() * expiryAlpha);
    canvas.drawLine(ex, 0, ex, ph, rule);
    if (item.live) {
      const chipAlpha = expiryAlpha * (item.urgent ? pulseAt(now) : 1);
      layer(canvas, chipAlpha);
      tag(canvas, font, item.countdown, g.expiryX, 8 + 8.5, 'center', item.chipBackground, item.countdownColor, {
        padX: 6,
        height: 17,
        radius: 6,
        border: item.countdownBorder,
      });
      canvas.restore();
    }
    canvas.restore();

    // The level, on the price axis, where the chart puts its own last price.
    if (!g.yClamped) {
      const text = item.pill ?? (g.price === null ? '--' : priceText(g.price, digits));
      tag(canvas, font, text, pw + 4, g.y, 'left', item.accent, item.tagText, { padX: 6, height: 16, radius: 3 });
    }

    // The exact entry point. A filled disc: it is the one part of the marker
    // that has to read as a single point. An entry that has scrolled out of
    // the price range is drawn faint, so it is not mistaken for being at the
    // edge it is pinned to.
    const dotAlpha = g.yClamped ? 0.35 : 1;
    if (item.hovered) {
      const halo = fillPaint(item.glow);
      halo.setMaskFilter(Skia.MaskFilter.MakeBlur(BlurStyle.Normal, 4, true));
      halo.setAlphaf(halo.getAlphaf() * dotAlpha);
      canvas.drawCircle(g.entryX, g.y, 7, halo);
    }
    const dot = fillPaint(item.accent);
    dot.setAlphaf(dot.getAlphaf() * dotAlpha);
    canvas.drawCircle(g.entryX, g.y, 5, dot);

    canvas.restore();
  }
}

/* ------------------------------------------------------------ the markers */

interface MarkerProps {
  order: OverlayOrder;
  geometry: DerivedValue<OrderGeometry[]>;
  now: SharedValue<number>;
  palette: ChartPalette;
  zone: string;
  digits: number;
  heartbeat: number;
  hovered: boolean;
  showLabel: boolean;
  flip: Flip;
  onToggle: (id: string, geometry: OrderGeometry | undefined) => void;
}

function OrderMarker({ order, geometry, now, palette, zone, digits, heartbeat, hovered, showLabel, flip, onToggle }: MarkerProps) {
  const tone = orderTone(order, palette);
  const up = order.direction === 'UP';
  const label = showLabel ? stateLabel(order) : null;
  const id = order.id;
  const fadeAt = order.fadeAt;
  const opacity = tone.opacity;

  const groupW = useSharedValue(TAG_FALLBACK_WIDTH);
  const groupH = useSharedValue(18);

  /*
   * The marker itself: a zero-size anchor pinned to the entry coordinate,
   * with the tag and the card laid out around it. One transform per frame
   * moves them together.
   */
  const anchorStyle = useAnimatedStyle(() => {
    const shapes = geometry.value;
    let g: OrderGeometry | undefined;
    for (let i = 0; i < shapes.length; i += 1) if (shapes[i]!.id === id) g = shapes[i];
    if (!g || g.hidden) return { opacity: 0, transform: [{ translateX: -9999 }, { translateY: 0 }] };
    const alpha = opacity * fadeOf(fadeAt, now.value) * (g.yClamped ? 0.35 : 1);
    return { opacity: alpha, transform: [{ translateX: g.entryX }, { translateY: g.y }] };
  });

  /*
   * The tag goes on the left of the entry, and crosses over only when it will
   * not fit there. An order runs from its entry to its expiry, which is always
   * to the right, so a tag on that side lies along its own line and hides it.
   * Sitting left also keeps the tag off the price axis.
   */
  const tagStyle = useAnimatedStyle(() => {
    const shapes = geometry.value;
    let entryX = 0;
    for (let i = 0; i < shapes.length; i += 1) if (shapes[i]!.id === id) entryX = shapes[i]!.entryX;
    const width = groupW.value;
    const leftwards = entryX - TAG_GAP >= width;
    return {
      alignItems: leftwards ? 'flex-end' : 'flex-start',
      transform: [{ translateX: leftwards ? -TAG_GAP - width : TAG_GAP }, { translateY: -groupH.value / 2 }],
    };
  });

  // The banner's nose has to face back across the gap it was placed over, so
  // it flips with the side.
  const [nose, setNose] = useState<'left' | 'right'>('right');
  useAnimatedReaction(
    () => {
      const shapes = geometry.value;
      let entryX = 0;
      for (let i = 0; i < shapes.length; i += 1) if (shapes[i]!.id === id) entryX = shapes[i]!.entryX;
      return entryX - TAG_GAP >= groupW.value ? 'right' : 'left';
    },
    (current, previous) => {
      if (current !== previous) runOnJS(setNose)(current);
    },
  );

  const press = () => {
    const shapes = geometry.value;
    onToggle(
      id,
      shapes.find((shape) => shape.id === id),
    );
  };

  return (
    <Animated.View pointerEvents="box-none" className="absolute left-0 top-0 h-0 w-0" style={anchorStyle}>
      {/* Tag and state chip travel together. */}
      <Animated.View
        pointerEvents="box-none"
        className="absolute left-0 top-0 gap-0.5"
        style={tagStyle}
        onLayout={(event) => {
          groupW.value = event.nativeEvent.layout.width;
          groupH.value = event.nativeEvent.layout.height;
        }}
      >
        <Pressable
          onPress={press}
          hitSlop={6}
          accessibilityRole="button"
          accessibilityLabel={order.direction + ' ' + formatMoneyCompact(order.stake, order.currency)}
          className="flex-row items-center"
          style={{ transform: [{ scale: hovered ? 1.08 : 1 }] }}
        >
          {nose === 'left' ? <Nose side="left" color={tone.tag} /> : null}
          <View
            className={cn('flex-row items-center gap-1 py-[3px]', nose === 'left' ? 'rounded-r pl-1 pr-2' : 'rounded-l pl-2 pr-1')}
            style={{ backgroundColor: tone.tag }}
          >
            <Icon as={up ? ArrowUp : ArrowDown} size={12} strokeWidth={3} color={tone.tagText} />
            <Text className="text-[10px] font-bold leading-[12px] tnum" style={{ color: tone.tagText }}>
              {formatMoneyCompact(order.stake, order.currency)}
            </Text>
          </View>
          {nose === 'right' ? <Nose side="right" color={tone.tag} /> : null}
        </Pressable>

        {label ? (
          <View className="rounded px-1 py-px" style={{ backgroundColor: withAlpha(tone.accent, 0.2) }}>
            <Text className="text-[9px] font-bold uppercase tracking-wide" style={{ color: tone.bright }}>
              {label}
              {order.settlement !== null && order.state !== 'CANCELLED'
                ? ' ' + formatSignedMoney(order.settlement, order.currency)
                : ''}
            </Text>
          </View>
        ) : null}
      </Animated.View>

      {hovered ? (
        <OrderTooltip order={order} palette={palette} zone={zone} digits={digits} now={heartbeat} flip={flip} />
      ) : null}
    </Animated.View>
  );
}

/** The banner's point, aimed back at the entry dot so the tag reads as belonging to it. */
function Nose({ side, color }: { side: 'left' | 'right'; color: string }) {
  return (
    <View
      style={{
        width: 0,
        height: 0,
        borderTopWidth: 9,
        borderBottomWidth: 9,
        borderTopColor: 'transparent',
        borderBottomColor: 'transparent',
        ...(side === 'right'
          ? { borderLeftWidth: 7, borderLeftColor: color }
          : { borderRightWidth: 7, borderRightColor: color }),
      }}
    />
  );
}

/**
 * The order's card.
 *
 * A child of the entry anchor, so it travels with the marker for free - no
 * second coordinate to keep in step. It opens away from whichever edge the
 * marker is near, and takes no touches, so it can never sit between a finger
 * and the marker that opened it.
 */
function OrderTooltip({
  order,
  palette,
  zone,
  digits,
  now,
  flip,
}: {
  order: OverlayOrder;
  palette: ChartPalette;
  zone: string;
  digits: number;
  now: number;
  flip: Flip;
}) {
  // Scoped to the card so a tick re-renders this card alone, never the
  // markers or the chart around it.
  const current = useMarketStore((s) => s.ticks[order.assetId]?.price ?? null);
  const tone = orderTone(order, palette);
  const entry = order.entryPrice;
  const winning = isInTheMoney(order, current);
  const remaining = Math.max(0, order.expiryTimestamp - now);
  const settled = order.settlement !== null;

  return (
    <View
      pointerEvents="none"
      accessibilityRole="summary"
      className="absolute rounded-lg p-2.5"
      style={[
        {
          width: CARD_WIDTH,
          left: flip.left,
          backgroundColor: withAlpha(palette.panel, 0.96),
          borderWidth: 1,
          borderColor: withAlpha(tone.accent, 0.45),
          shadowColor: '#000',
          shadowOpacity: 0.5,
          shadowRadius: 16,
          shadowOffset: { width: 0, height: 10 },
          elevation: 8,
        },
        flip.y === 'up' ? { bottom: 8 } : { top: 8 },
      ]}
    >
      <View className="mb-1.5 flex-row items-center justify-between gap-2 border-b pb-1.5" style={{ borderBottomColor: withAlpha(palette.strongText, 0.06) }}>
        <Text className="text-[11px] font-black uppercase tracking-wide" style={{ color: tone.accent }}>
          {order.direction}
        </Text>
        <Text className="text-[10px] font-semibold uppercase tracking-wide" style={{ color: palette.text }}>
          {stateLabel(order) ?? 'Active'}
        </Text>
      </View>

      <View className="gap-1">
        <Row label="Amount" palette={palette}>
          {formatMoney(order.stake, order.currency)}
        </Row>
        <Row label="Entry Price" palette={palette}>
          {entry === null ? 'At market' : formatPrice(entry, digits)}
        </Row>
        <Row label="Current Price" palette={palette} tone={winning === null ? undefined : winning ? palette.up : palette.down}>
          {current === null ? '--' : formatPrice(current, digits)}
        </Row>
        <Row label="Direction" palette={palette} tone={tone.accent}>
          {order.direction}
        </Row>
        <Row label="Expiry" palette={palette}>
          {formatClock(order.expiryTimestamp, zone)}
        </Row>
        <Row label="Remaining" palette={palette} tone={remaining <= 10_000 ? palette.brand : undefined}>
          {formatCountdown(remaining)}
        </Row>
        <Row
          label={settled ? 'Result' : 'Potential Payout'}
          palette={palette}
          tone={settled ? (order.settlement! >= 0 ? palette.up : palette.down) : palette.up}
        >
          {settled ? formatSignedMoney(order.settlement!, order.currency) : formatMoney(order.potentialReturn, order.currency)}
        </Row>
      </View>
    </View>
  );
}

function Row({ label, children, palette, tone }: { label: string; children: string; palette: ChartPalette; tone?: string }) {
  return (
    <View className="flex-row items-center justify-between gap-3">
      <Text className="text-[11px]" style={{ color: palette.text }} numberOfLines={1}>
        {label}
      </Text>
      <Text className="text-right text-[11px] font-semibold tnum" style={{ color: tone ?? palette.strongText }} numberOfLines={1}>
        {children}
      </Text>
    </View>
  );
}

/** Keeps a value inside a range, tolerating an inverted one on a tiny plot. */
function clamp(value: number, min: number, max: number): number {
  if (max < min) return min;
  return value < min ? min : value > max ? max : value;
}
