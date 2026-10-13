import { useCallback, useEffect, useMemo, useRef, useState, type MutableRefObject, type RefObject } from 'react';
import type { IChartApi, ISeriesApi } from 'lightweight-charts';
import clsx from 'clsx';
import { ArrowDown, ArrowUp } from 'lucide-react';
import { useMarketStore } from '../../../stores/marketStore';
import { useSettingsStore } from '../../../stores/settingsStore';
import { useTradeStore } from '../../../stores/tradeStore';
import { useAccountStore } from '../../../stores/accountStore';
import { accountOf } from '../ticketModel';
import { useOrderPreviewStore } from '../../../stores/orderPreviewStore';
import {
  formatCountdown,
  formatMoney,
  formatMoneyCompact,
  formatPrice,
  formatSignedMoney,
} from '../../../utils/format';
import { formatClock } from '../../../utils/time';
import { withAlpha, type ChartPalette } from '../../../utils/theme-colors';
import {
  buildOverlayOrders,
  isInTheMoney,
  needsHeartbeat,
  orderTone,
  stateLabel,
  type OverlayOrder,
} from './orderModel';
import { useOrderProjection, type OrderAnchor, type OrderGeometry } from './useOrderProjection';
import type { Asset } from '../../../types/api';

/**
 * The order overlay.
 *
 * Orders are drawn in the DOM rather than on the chart canvas. The library can
 * draw a horizontal price line and a marker glued to a bar, and neither is
 * enough here: an order needs a line that stops at its own expiry, a tag that
 * can be hovered, a vertical line at an instant the canvas has no primitive
 * for, and six visual states. Doing it in the DOM also keeps the promise that
 * hovering an order cannot disturb a candle - the canvas is never redrawn.
 *
 * Two clocks drive it, deliberately kept apart:
 *
 *   - a one-second heartbeat re-renders the countdowns and retires finished
 *     orders. It only runs while something is counting down;
 *   - a frame loop, in `useOrderProjection`, moves the nodes. It writes to the
 *     DOM directly, so the chart's own price and time axes can move under an
 *     order without React being involved at all.
 */
interface Props {
  asset: Asset | null;
  palette: ChartPalette;
  zone: string;
  chartRef: MutableRefObject<IChartApi | null>;
  /**
   * Whether `chartRef` holds a chart yet. The chart is created by the page
   * after this layer's first effects have run, so the room-making below has
   * to run again once it exists.
   */
  chartReady: boolean;
  seriesRef: MutableRefObject<ISeriesApi<'Candlestick'> | null>;
  containerRef: MutableRefObject<HTMLDivElement | null>;
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
   * A drawing tool is waiting for clicks on this chart. The markers stop
   * catching the pointer until it is done: a level drawn at an order's own
   * price would otherwise open the order's card instead of being placed.
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
 * is, truthfully, six pixels away - correct, and useless: the expiry line, the
 * countdown and the direction glyph all land on top of the last candle. This
 * floor buys the order somewhere to be drawn, and it is dropped again the
 * moment nothing is pending.
 */
const ORDER_RIGHT_OFFSET = 18;

/** The hover card's width, mirrored from its class so it can be placed. */
const CARD_WIDTH = 220;
/** Clearance the card keeps from the marker and from the plot's edges. */
const CARD_GAP = 12;

/** The triangle that caps the run at the expiry, as CSS border widths. */
const CAP_HEIGHT = 7;
const CAP_HALF_WIDTH = 5;

/** Clearance between the entry dot and its tag. */
const TAG_GAP = 10;
/** Stands in for the tag's width until it has been laid out and measured. */
const TAG_FALLBACK_WIDTH = 96;

/** The DOM nodes the frame loop writes to, per order. */
interface OrderNodes {
  root: HTMLDivElement | null;
  line: HTMLDivElement | null;
  anchor: HTMLDivElement | null;
  tag: HTMLDivElement | null;
  expiry: HTMLDivElement | null;
  band: HTMLDivElement | null;
  /** The bright run from the entry to the right edge. */
  reach: HTMLDivElement | null;
  /** The price tab on the axis. */
  pill: HTMLDivElement | null;
  /** The large direction glyph. */
  arrow: HTMLDivElement | null;
}

export function ChartOrderLayer({
  asset,
  palette,
  zone,
  chartRef,
  chartReady,
  seriesRef,
  containerRef,
  barsRef,
  bucketMs,
  hideTrades,
  showLabels,
  performanceMode,
  guideSeconds,
  passThrough = false,
}: Props) {
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
  const [hoveredId, setHoveredId] = useState<string | null>(null);
  /**
   * The marker held open by a tap.
   *
   * Touch has no hover, and a browser that synthesises one sends it just
   * before the click - so a tap that only toggled `hoveredId` would open the
   * card on the phantom enter and close it again on the click, leaving
   * nothing on screen. A pin is separate state and toggles on its own.
   */
  const [pinnedId, setPinnedId] = useState<string | null>(null);
  const [flip, setFlip] = useState<{ left: number; y: 'up' | 'down' }>({
    left: CARD_GAP,
    y: 'up',
  });

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
    const timer = window.setInterval(() => setHeartbeat(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [beating]);

  const nodesRef = useRef(new Map<string, OrderNodes>());
  const ordersRef = useRef<OverlayOrder[]>(orders);
  ordersRef.current = orders;

  /**
   * Make room on the right for the furthest pending expiry.
   *
   * Changing `rightOffset` scrolls the chart, so this has to be careful about
   * when it fires. Two rules keep it from wrestling the trader for the view:
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
    const chart = chartRef.current;
    if (!chart) return;

    const scale = chart.timeScale();
    const applied = rightOffsetRef.current;
    // A pan in progress leaves fractional drift; a bar and a half of slack
    // tells that apart from a deliberate scroll into history.
    if (Math.abs(scale.scrollPosition() - applied) >= 1.5) return;

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
    const spacing = scale.options().barSpacing;
    const share = spacing > 0 && scale.width() > 0 ? Math.floor((scale.width() * MAX_RIGHT_SHARE) / spacing) : MAX_RIGHT_OFFSET;
    needed = Math.min(needed, MAX_RIGHT_OFFSET, Math.max(BASE_RIGHT_OFFSET, share));

    if (needed === applied) return;
    rightOffsetRef.current = needed;
    scale.applyOptions({ rightOffset: needed });
  }, [orders, autoScroll, bucketMs, chartRef, barsRef, guideSeconds, chartReady]);

  const registerNode = useCallback((id: string, part: keyof OrderNodes, element: HTMLDivElement | null) => {
    const map = nodesRef.current;
    let entry = map.get(id);
    if (!entry) {
      entry = {
        root: null,
        line: null,
        anchor: null,
        tag: null,
        expiry: null,
        band: null,
        reach: null,
        pill: null,
        arrow: null,
      };
      map.set(id, entry);
    }
    entry[part] = element;
    // A stable hook for each node. The class lists are near-identical between
    // parts - the line and the reach differ only in what the frame loop writes
    // to them - so naming them is the only way to tell them apart from outside.
    if (element) element.dataset.part = part;
    if (Object.values(entry).every((node) => node === null)) map.delete(id);
  }, []);

  /**
   * The live price, read without subscribing.
   *
   * A preview's line sits on the market until the order is committed, which
   * means it has to move at tick rate. Subscribing would re-render the whole
   * layer several times a second; reading the store inside the frame loop
   * keeps that movement entirely in the DOM.
   *
   * The feed's last price is the fallback, matching what the ticket quotes
   * from. Without it a preview opened before the first tick lands - or during
   * a dropped socket - would have no price to sit at and would silently not
   * draw, which reads as the preview being broken rather than the feed being
   * quiet. A committed order never reaches either: its entry price is frozen.
   */
  const fallbackPrice = asset?.feed.lastPrice ?? null;
  const resolveAnchors = useCallback((): OrderAnchor[] => {
    const live = assetId ? (useMarketStore.getState().ticks[assetId]?.price ?? null) : null;
    const now = Date.now();
    return ordersRef.current.map((order) => ({
      id: order.id,
      // A sliding preview is recomputed here, every frame, so it tracks the
      // clock smoothly instead of stepping a second's width once a second
      // with the heartbeat that drives the countdowns.
      entryTimestamp: order.tracksClock ? now : order.entryTimestamp,
      expiryTimestamp: order.tracksClock ? now + order.expirySeconds * 1000 : order.expiryTimestamp,
      price: order.entryPrice ?? live ?? fallbackPrice,
    }));
  }, [assetId, fallbackPrice]);

  const applyGeometry = useCallback((id: string, geometry: OrderGeometry) => {
    const nodes = nodesRef.current.get(id);
    if (!nodes) return;
    const order = ordersRef.current.find((entry) => entry.id === id);
    if (!order) return;

    const { root, line, anchor, tag, expiry, band, reach, pill, arrow } = nodes;
    const spanning = order.state === 'PREVIEW';

    if (root) {
      root.style.visibility = geometry.hidden ? 'hidden' : 'visible';
      /*
       * Where the hover card can go, written here so it is already correct by
       * the time a pointer lands on the marker.
       *
       * Measured against the space actually available rather than against a
       * ratio of the plot. A marker two thirds of the way across is not the
       * same problem on a phone as on a desktop: there the card simply does
       * not fit on either side, and the only honest answer is to slide it off
       * the marker and keep it on screen. The offset is relative to the entry,
       * which is what the card hangs from.
       */
      const width = Math.min(CARD_WIDTH, Math.max(0, geometry.plotWidth - CARD_GAP * 2));
      const roomRight = geometry.plotWidth - geometry.entryX - CARD_GAP * 2;
      const roomLeft = geometry.entryX - CARD_GAP * 2;
      const preferred = roomRight >= width ? CARD_GAP : roomLeft >= width ? -CARD_GAP - width : -width / 2;
      const cardLeft = clamp(
        preferred,
        CARD_GAP - geometry.entryX,
        geometry.plotWidth - CARD_GAP - width - geometry.entryX,
      );

      const flipY = geometry.y > geometry.plotHeight * 0.55 ? 'up' : 'down';
      const asText = String(Math.round(cardLeft));
      if (root.dataset.cardLeft !== asText) root.dataset.cardLeft = asText;
      if (root.dataset.flipY !== flipY) root.dataset.flipY = flipY;
    }

    if (line) {
      // A preview spans the plot, like a price level being considered. A real
      // order stops at its own expiry, because that is where it ends.
      const left = spanning ? 0 : geometry.entryX;
      const width = spanning ? geometry.plotWidth : Math.max(2, geometry.expiryX - geometry.entryX);
      line.style.transform = 'translate3d(0, ' + geometry.y + 'px, 0)';
      line.style.left = left + 'px';
      line.style.width = width + 'px';
    }

    if (anchor) {
      anchor.style.transform = 'translate3d(' + geometry.entryX + 'px, ' + geometry.y + 'px, 0)';
      // An entry that has scrolled out of the price range would otherwise be
      // drawn pinned to an edge it is not actually at.
      anchor.style.opacity = geometry.yClamped ? '0.35' : '1';
    }

    if (tag) {
      /*
       * The tag goes on the left of the entry, and crosses over only when it
       * will not fit there.
       *
       * The side is not a free choice. An order runs from its entry to its
       * expiry, which is always to the right, so a tag placed on that side
       * lies along its own line and hides it - the dot and the expiry cap end
       * up looking like two unconnected marks. Sitting left also keeps the
       * tag off the price axis, which was the original reason for flipping
       * it, so one rule now covers both.
       *
       * Measured rather than assumed, because the banner is as wide as the
       * amount inside it. Reading `offsetWidth` every frame would force a
       * layout in the middle of the write pass, so it is cached against the
       * text that produced it and re-read only when that text changes - which
       * for a placed order is never, and for a preview only when the trader
       * edits the stake.
       */
      const label = tag.textContent ?? '';
      if (tag.dataset.measured !== label) {
        const measured = tag.offsetWidth;
        if (measured > 0) {
          tag.dataset.w = String(measured);
          tag.dataset.measured = label;
        }
      }
      const tagWidth = Number(tag.dataset.w) || TAG_FALLBACK_WIDTH;
      const leftwards = geometry.entryX - TAG_GAP >= tagWidth;

      tag.style.left = leftwards ? 'auto' : TAG_GAP + 'px';
      tag.style.right = leftwards ? TAG_GAP + 'px' : 'auto';
      tag.style.alignItems = leftwards ? 'flex-end' : 'flex-start';
      // The banner's nose has to face back across the gap it was just placed
      // over, so it flips with the side. Written as an attribute rather than
      // a style because the shape is a clip-path that belongs in the
      // stylesheet, not in a per-frame string.
      const nose = leftwards ? 'right' : 'left';
      if (tag.dataset.nose !== nose) tag.dataset.nose = nose;
    }

    if (expiry) {
      expiry.style.transform = 'translate3d(' + geometry.expiryX + 'px, 0, 0)';
      expiry.style.opacity = geometry.expiryClamped ? '0.45' : '1';
    }

    if (band) {
      const up = order.direction === 'UP';
      band.style.height = (up ? geometry.y : Math.max(0, geometry.plotHeight - geometry.y)) + 'px';
      band.style.transform = up ? 'translate3d(0, 0, 0)' : 'translate3d(0, ' + geometry.y + 'px, 0)';
    }

    if (reach) {
      // From the entry out to the edge of the plot: the stretch of chart the
      // order is still being decided over.
      reach.style.transform = 'translate3d(' + geometry.entryX + 'px, ' + geometry.y + 'px, 0)';
      reach.style.width = Math.max(0, geometry.plotWidth - geometry.entryX) + 'px';
    }

    if (pill) {
      // Sits on the price axis, past the plot, the way the chart's own last
      // price tab does - so the level being taken is readable off the scale.
      pill.style.transform = 'translate3d(' + geometry.plotWidth + 'px, ' + geometry.y + 'px, 0)';
      pill.style.opacity = geometry.yClamped ? '0' : '1';
    }

    if (arrow) {
      /*
       * In the open ground past the entry.
       *
       * Centred in the order's window when that window is wide enough to hold
       * the glyph, and a fixed step out from the entry when it is not. The
       * narrow case is the common one: a one-minute trade on a one-minute
       * frame spans a single bar, which is about six pixels. Tying the glyph
       * strictly to that span meant it was never drawn on the default
       * timeframe at all - technically defensible, and useless, because the
       * direction of the bet is exactly what the preview exists to show.
       */
      const span = geometry.expiryX - geometry.entryX;
      const along = Math.max(span / 2, 52);
      const x = Math.min(geometry.entryX + along, geometry.plotWidth - 28);
      const lift = geometry.y + (order.direction === 'UP' ? -68 : 68);
      const y = Math.min(Math.max(lift, 34), Math.max(34, geometry.plotHeight - 34));
      arrow.style.transform = 'translate3d(' + x + 'px, ' + y + 'px, 0)';
      // Only when there is no open ground left at all, which means the entry
      // has been scrolled hard against the right edge.
      arrow.style.opacity = geometry.plotWidth - geometry.entryX < 44 ? '0' : '1';
    }
  }, []);

  useOrderProjection({
    chartRef,
    seriesRef,
    containerRef,
    barsRef,
    bucketMs,
    resolve: resolveAnchors,
    apply: applyGeometry,
    enabled: orders.length > 0,
    maxFps: performanceMode ? 30 : 60,
  });

  // An order that settles and fades must not leave a card behind.
  useEffect(() => {
    const has = (id: string | null) => id !== null && orders.some((order) => order.id === id);
    if (hoveredId && !has(hoveredId)) setHoveredId(null);
    if (pinnedId && !has(pinnedId)) setPinnedId(null);
  }, [orders, hoveredId, pinnedId]);

  // Tapping anywhere that is not a marker dismisses a pinned card. Listening
  // on the window rather than the layer, because the layer lets pointer events
  // through to the chart and would never see them.
  useEffect(() => {
    if (!pinnedId) return;
    const dismiss = (event: PointerEvent) => {
      const target = event.target as HTMLElement | null;
      if (target?.closest('[data-order-root]')) return;
      setPinnedId(null);
    };
    window.addEventListener('pointerdown', dismiss);
    return () => window.removeEventListener('pointerdown', dismiss);
  }, [pinnedId]);

  /** Reads the side the card should open on, written by the frame loop. */
  const readFlip = useCallback((element: HTMLElement | null) => {
    const root = element?.closest<HTMLElement>('[data-order-root]');
    setFlip({
      left: Number(root?.dataset.cardLeft ?? CARD_GAP) || CARD_GAP,
      y: root?.dataset.flipY === 'down' ? 'down' : 'up',
    });
  }, []);

  const beginHover = useCallback(
    (id: string, element: HTMLElement | null) => {
      readFlip(element);
      setHoveredId(id);
    },
    [readFlip],
  );

  const togglePin = useCallback(
    (id: string, element: HTMLElement | null) => {
      readFlip(element);
      setPinnedId((current) => (current === id ? null : id));
    },
    [readFlip],
  );

  if (!asset) return null;

  return (
    // Not clipped: the hover card has to be able to reach past the plot,
    // which on a short mobile chart is most of it. The parts that would look
    // wrong outside the plot - the lines and the band - are clipped per order
    // instead, and the marker's own coordinates are already clamped into it.
    /*
     * Above the toolbar, which sits at z-20.
     *
     * They overlap on a short chart - the toolbar runs along the bottom and a
     * marker goes wherever its price is. The toolbar losing a button for a
     * moment is recoverable; a marker that cannot be reached is not, because
     * on touch its tag is the only way to open the order's details. The layer
     * itself takes no pointer events, so only the marker's own tag and line
     * are ever in the way.
     */
    <div
      className={clsx('pointer-events-none absolute inset-0 z-30', passThrough && '[&_*]:!pointer-events-none')}
      aria-hidden={orders.length === 0}
    >
      {orders.map((order) => (
        <OrderVisual
          key={order.id}
          order={order}
          palette={palette}
          zone={zone}
          digits={digits}
          now={heartbeat}
          hovered={hoveredId === order.id || pinnedId === order.id}
          showLabel={showLabels}
          flip={flip}
          onHoverStart={beginHover}
          onHoverEnd={() => setHoveredId((current) => (current === order.id ? null : current))}
          onToggle={togglePin}
          register={registerNode}
        />
      ))}
    </div>
  );
}

interface VisualProps {
  order: OverlayOrder;
  palette: ChartPalette;
  zone: string;
  digits: number;
  now: number;
  hovered: boolean;
  showLabel: boolean;
  flip: { left: number; y: 'up' | 'down' };
  onHoverStart: (id: string, element: HTMLElement | null) => void;
  onHoverEnd: () => void;
  onToggle: (id: string, element: HTMLElement | null) => void;
  register: (id: string, part: keyof OrderNodes, element: HTMLDivElement | null) => void;
}

function OrderVisual({
  order,
  palette,
  zone,
  digits,
  now,
  hovered,
  showLabel,
  flip,
  onHoverStart,
  onHoverEnd,
  onToggle,
  register,
}: VisualProps) {
  const tone = orderTone(order, palette);
  const up = order.direction === 'UP';
  const preview = order.state === 'PREVIEW';
  const live = order.state === 'ACTIVE' || preview;
  const remaining = Math.max(0, order.expiryTimestamp - now);
  const urgent = live && remaining <= 10_000;
  const label = showLabel ? stateLabel(order) : null;
  const fading = order.fadeAt !== null && order.fadeAt - now <= 600;

  const Arrow = up ? ArrowUp : ArrowDown;

  return (
    <div
      data-order-root
      data-order-id={order.id}
      ref={(element) => register(order.id, 'root', element)}
      className={clsx('absolute inset-0 transition-opacity duration-500', fading && 'opacity-0')}
      style={{ opacity: fading ? 0 : tone.opacity }}
    >
      {/* Everything that has to stop at the plot's edge. The marker and its
          card sit outside this, so the card can overhang a short chart. */}
      <div className="pointer-events-none absolute inset-0 overflow-hidden">
        {/* Direction band. Only a preview gets one: it answers "which way am I
          betting" at a glance, and on a chart of live orders the same fill
          repeated would drown the candles. */}
        {preview ? (
          <div
            ref={(element) => register(order.id, 'band', element)}
            className="pointer-events-none absolute left-0 top-0 w-full"
            style={{
              background: up
                ? 'linear-gradient(to top, ' + tone.soft + ', ' + withAlpha(tone.accent, 0.03) + ')'
                : 'linear-gradient(to bottom, ' + tone.soft + ', ' + withAlpha(tone.accent, 0.03) + ')',
            }}
          />
        ) : null}

        {/* The run from the entry out to the edge: bright and solid, where the
            level itself is only a dotted rule across the whole chart. This is
            the part that reads as "an order is being placed here". */}
        {preview ? (
          <div
            ref={(element) => register(order.id, 'reach', element)}
            className="pointer-events-none absolute left-0 top-0 h-0"
          >
            <div
              className="absolute inset-x-0 top-0 rounded-full"
              style={{ borderTopWidth: 2, borderTopStyle: 'solid', borderTopColor: tone.bright }}
            />
          </div>
        ) : null}

        {/* Which way the order is betting, at a glance. */}
        {preview ? (
          <div
            ref={(element) => register(order.id, 'arrow', element)}
            className="pointer-events-none absolute left-0 top-0 h-0 w-0 transition-opacity duration-200"
          >
            <DirectionGlyph key={order.direction} up={up} color={tone.bright} />
          </div>
        ) : null}

        {/* Entry price line. */}
        <div
          ref={(element) => register(order.id, 'line', element)}
          className="pointer-events-none absolute left-0 top-0 h-0"
        >
          <div
            className="absolute inset-x-0 top-0 transition-[box-shadow,border-color] duration-150"
            style={{
              // A real order's run is solid and two pixels, so it reads as a
              // deal on the chart rather than as another gridline. Only the
              // provisional states thin out to a hairline.
              borderTopWidth: tone.dashed && !hovered ? 1 : 2,
              borderTopStyle: tone.dashed ? 'dashed' : 'solid',
              borderTopColor: hovered ? tone.bright : tone.accent,
              boxShadow: hovered ? '0 0 10px 1px ' + tone.glow : 'none',
            }}
          />
          {/* Where the run ends, which for anything but a preview is the
              expiry. The line is already laid out from the entry to that
              coordinate, so the cap rides its right edge and needs no
              projection of its own. */}
          {preview ? null : (
            <div
              data-part="cap"
              className="absolute right-0 h-0 w-0 translate-x-1/2"
              style={{
                top: -CAP_HEIGHT,
                borderLeft: CAP_HALF_WIDTH + 'px solid transparent',
                borderRight: CAP_HALF_WIDTH + 'px solid transparent',
                borderBottom: CAP_HEIGHT + 'px solid ' + tone.accent,
              }}
            />
          )}
          {/* A one-pixel line is not a pointer target; this is. It carries the
              tap handler as well as the hover one, so touch - which has no
              hover at all - has a second way into an order besides its tag. */}
          <div
            className="pointer-events-auto absolute inset-x-0 -top-[7px] h-[14px] cursor-pointer"
            onMouseEnter={(event) => onHoverStart(order.id, event.currentTarget)}
            onMouseLeave={onHoverEnd}
            onClick={(event) => onToggle(order.id, event.currentTarget)}
          />
        </div>

        {/* Expiry line and its countdown. */}
        <div
          ref={(element) => register(order.id, 'expiry', element)}
          className="pointer-events-none absolute inset-y-0 left-0 w-0"
        >
          <div
            className="absolute inset-y-0 left-0 w-px"
            style={{
              background:
                'repeating-linear-gradient(to bottom, ' +
                withAlpha(live ? tone.accent : palette.priceLine, hovered ? 0.95 : 0.6) +
                ' 0 5px, transparent 5px 10px)',
            }}
          />
          {live ? (
            <span
              className={clsx(
                'absolute top-2 -translate-x-1/2 whitespace-nowrap rounded-md px-1.5 py-0.5 text-[10px] font-bold tnum backdrop-blur-sm',
                'sm:text-[11px]',
                urgent && 'animate-order-pulse',
              )}
              style={{
                color: urgent ? palette.brand : palette.strongText,
                backgroundColor: withAlpha(palette.panel, 0.88),
                boxShadow: 'inset 0 0 0 1px ' + withAlpha(urgent ? palette.brand : tone.accent, 0.5),
              }}
            >
              {formatCountdown(remaining)}
            </span>
          ) : null}
        </div>
      </div>

      {/* The level, on the price axis, where the chart puts its own last
          price. Drawn outside the clip because the axis is outside the plot. */}
      <div
        ref={(element) => register(order.id, 'pill', element)}
        className="pointer-events-none absolute left-0 top-0 h-0 w-0"
      >
        <div
          className="absolute left-1 -translate-y-1/2 whitespace-nowrap rounded px-1.5 py-[3px] text-[10px] font-bold leading-none tnum shadow-raised"
          style={{ backgroundColor: tone.accent, color: tone.tagText }}
        >
          {order.entryPrice === null ? (
            <LivePrice assetId={order.assetId} digits={digits} fallback={null} />
          ) : (
            formatPrice(order.entryPrice, digits)
          )}
        </div>
      </div>

      {/* The marker itself: a zero-size anchor pinned to the entry coordinate,
          with everything laid out around it. One transform per frame moves the
          tag, the dot and the tooltip together. */}
      <div ref={(element) => register(order.id, 'anchor', element)} className="absolute left-0 top-0 h-0 w-0">
        {/* Tag and state chip travel together. Which side of the entry they
            sit on is written by the frame loop from this order's own
            coordinate - an entry near the right edge has to place them
            leftwards or they would be drawn over the price axis. */}
        <div
          ref={(element) => register(order.id, 'tag', element)}
          className="pointer-events-none absolute flex -translate-y-1/2 flex-col gap-0.5"
        >
          {/* The banner. Its nose points back at the entry dot, so the tag
              reads as belonging to that point rather than floating beside it;
              the side it points from is written by the frame loop, which is
              the only place that knows which side of the entry it landed on.
              The raised shadow is a filter rather than a box-shadow because
              the nose is a clip-path, and a box-shadow would be clipped away
              with it - drop-shadow follows the clipped outline. */}
          <div
            className={clsx(
              'order-tag pointer-events-auto flex w-fit cursor-pointer select-none items-center gap-1',
              'py-[3px] text-[10px] font-bold leading-none tnum transition-transform duration-150 sm:text-[11px]',
              hovered && 'scale-[1.08]',
              order.state === 'ACTIVE' && 'animate-order-in',
            )}
            style={{
              backgroundColor: tone.tag,
              color: tone.tagText,
              filter: hovered
                ? 'drop-shadow(0 0 3px ' + tone.glow + ') drop-shadow(0 4px 7px rgba(0,0,0,0.8))'
                : 'drop-shadow(0 2px 4px rgba(0,0,0,0.55))',
            }}
            onMouseEnter={(event) => onHoverStart(order.id, event.currentTarget)}
            onMouseLeave={onHoverEnd}
            // A tap on touch, or a click on a desktop, holds the card open.
            onClick={(event) => onToggle(order.id, event.currentTarget)}
          >
            <Arrow className="h-3 w-3 shrink-0" strokeWidth={3} />
            {formatMoneyCompact(order.stake, order.currency)}
          </div>

          {label ? (
            <span
              className="hidden w-fit whitespace-nowrap rounded px-1 py-px text-[9px] font-bold uppercase tracking-wide sm:block"
              style={{
                backgroundColor: withAlpha(tone.accent, 0.2),
                color: tone.bright,
              }}
            >
              {label}
              {order.settlement !== null && order.state !== 'CANCELLED'
                ? ' ' + formatSignedMoney(order.settlement, order.currency)
                : ''}
            </span>
          ) : null}
        </div>

        {/* The exact entry point. A filled disc rather than a diamond: it is
            the one part of the marker that has to read as a single point, and
            a circle says that at any size without the corners of a rotated
            square pulling the eye off centre. */}
        <div
          className="absolute h-2.5 w-2.5 -translate-x-1/2 -translate-y-1/2 rounded-full"
          style={{
            backgroundColor: tone.accent,
            boxShadow: hovered ? '0 0 8px 2px ' + tone.glow : undefined,
          }}
        />

        {hovered ? (
          <OrderTooltip order={order} palette={palette} zone={zone} digits={digits} now={now} flip={flip} />
        ) : null}
      </div>
    </div>
  );
}

/**
 * The live market price, on its own.
 *
 * Its own component so a tick re-renders these few characters and nothing
 * else - the markers around it are moved by the frame loop, and pulling the
 * price into the layer would drag all of them through React instead.
 */
function LivePrice({ assetId, digits, fallback }: { assetId: string; digits: number; fallback: number | null }) {
  const live = useMarketStore((s) => s.ticks[assetId]?.price ?? null);
  const price = live ?? fallback;
  return <>{price === null ? '--' : formatPrice(price, digits)}</>;
}

/**
 * The direction glyph.
 *
 * Hand-drawn rather than an icon: at this size a stroked arrow from the icon
 * set reads as a hairline, and the whole point of it is to be the thing you
 * see without looking for it. Drawn pointing up and mirrored for a down bet,
 * so both directions carry exactly the same weight.
 */
function DirectionGlyph({ up, color }: { up: boolean; color: string }) {
  return (
    /*
     * Two elements, because both need a transform and a transform does not
     * merge - it replaces. Placement and the DOWN flip live on the wrapper;
     * the flash, which animates scale, lives on the glyph inside it. Put them
     * on one element and the first frame of the animation would throw away
     * the centring and the flip, and the arrow would jump half its own size
     * and point the wrong way on a DOWN bet.
     */
    <span className={clsx('absolute block h-11 w-11 -translate-x-1/2 -translate-y-1/2', !up && '-scale-y-100')}>
      <svg
        viewBox="0 0 48 48"
        className="h-full w-full animate-arrow-in drop-shadow"
        style={{ color }}
        fill="none"
        aria-hidden
      >
        <path d="M9 39C18 38 26 31 33 17" stroke="currentColor" strokeWidth="9" strokeLinecap="round" />
        <path
          d="M25 12.5 38.5 11 37.5 25"
          stroke="currentColor"
          strokeWidth="9"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    </span>
  );
}

/**
 * The hover card.
 *
 * It is a child of the entry anchor, so it travels with the marker for free -
 * no second coordinate to keep in step. It flips away from whichever edge it
 * is near, and it is `pointer-events-none` so it can never sit between the
 * pointer and the marker that opened it.
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
  flip: { left: number; y: 'up' | 'down' };
}) {
  // Scoped to the tooltip so a tick re-renders this card alone, never the
  // markers or the chart around it.
  const current = useMarketStore((s) => s.ticks[order.assetId]?.price ?? null);
  const tone = orderTone(order, palette);
  const entry = order.entryPrice;
  const winning = isInTheMoney(order, current);
  const remaining = Math.max(0, order.expiryTimestamp - now);
  const settled = order.settlement !== null;

  return (
    <div
      role="tooltip"
      className={clsx(
        'pointer-events-none absolute z-30 w-[220px] max-w-[calc(100vw-2rem)] rounded-lg p-2.5 text-[11px] shadow-overlay',
        'animate-fade-in backdrop-blur-md',
        flip.y === 'up' ? 'bottom-2' : 'top-2',
      )}
      style={{
        left: flip.left,
        backgroundColor: withAlpha(palette.panel, 0.96),
        boxShadow: 'inset 0 0 0 1px ' + withAlpha(tone.accent, 0.45) + ', 0 20px 40px -18px rgba(0,0,0,0.95)',
      }}
    >
      <div className="mb-1.5 flex items-center justify-between gap-2 border-b border-white/5 pb-1.5">
        <span className="text-[11px] font-black uppercase tracking-wide" style={{ color: tone.accent }}>
          {order.direction}
        </span>
        <span className="text-[10px] font-semibold uppercase tracking-wide" style={{ color: palette.text }}>
          {stateLabel(order) ?? 'Active'}
        </span>
      </div>

      <dl className="grid grid-cols-[auto,1fr] gap-x-3 gap-y-1">
        <Row label="Amount" palette={palette}>
          {formatMoney(order.stake, order.currency)}
        </Row>
        <Row label="Entry Price" palette={palette}>
          {entry === null ? 'At market' : formatPrice(entry, digits)}
        </Row>
        <Row
          label="Current Price"
          palette={palette}
          tone={winning === null ? undefined : winning ? palette.up : palette.down}
        >
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
          {settled
            ? formatSignedMoney(order.settlement!, order.currency)
            : formatMoney(order.potentialReturn, order.currency)}
        </Row>
      </dl>
    </div>
  );
}

function Row({
  label,
  children,
  palette,
  tone,
}: {
  label: string;
  children: React.ReactNode;
  palette: ChartPalette;
  tone?: string;
}) {
  return (
    <>
      <dt className="whitespace-nowrap" style={{ color: palette.text }}>
        {label}
      </dt>
      <dd className="text-right font-semibold tnum" style={{ color: tone ?? palette.strongText }}>
        {children}
      </dd>
    </>
  );
}

/** Keeps a value inside a range, tolerating an inverted one on a tiny plot. */
function clamp(value: number, min: number, max: number): number {
  if (max < min) return min;
  return value < min ? min : value > max ? max : value;
}
