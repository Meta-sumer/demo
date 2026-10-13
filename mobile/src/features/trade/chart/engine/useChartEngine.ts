import { useCallback, useMemo, useRef } from 'react';
import { Gesture } from 'react-native-gesture-handler';
import {
  cancelAnimation,
  runOnJS,
  useDerivedValue,
  useSharedValue,
  withDecay,
  withTiming,
  type DerivedValue,
  type SharedValue,
} from 'react-native-reanimated';
import { clampOffset, fitPrices, horizontal, indexAt, logicalAt, offsetKeeping, type Margins } from './scale';
import { EMPTY_SERIES, HIDDEN_CROSSHAIR, type Crosshair, type SeriesData, type Size, type View } from './types';

/**
 * The engine: the viewport state of one chart, the gestures that move it,
 * and the derived `View` every layer draws through.
 *
 * It stands in for lightweight-charts' chart object. The state is the same -
 * a bar spacing, a right offset counted in bars, a resting right offset the
 * view returns to when it follows the live edge - but it lives in Reanimated
 * shared values, so a pan, a pinch or a fling runs on the UI thread from
 * start to finish and the chart repaints at the display's rate without React
 * being involved.
 *
 *   pan            scrolls the time axis; a fling keeps it going
 *   pinch          zooms about the fingers (about the live edge when following)
 *   drag the axis  zooms time, as lightweight-charts' axis drag does
 *   long-press     shows the crosshair and drags it
 *   tap            handed to the chart (`onTap`), which routes it to the
 *                  orders, the drawings or the crosshair
 */
export interface EngineOptions {
  /** Resting width of one bar, px. */
  initialSpacing?: number;
  /** Empty bars kept ahead of the newest one while following the live edge. */
  restingOffset?: number;
  minSpacing?: number;
  maxSpacing?: number;
  /** Height of the time axis strip; 0 for none. */
  axisH?: number;
  margins: Margins;
  /** A tap on the canvas, in canvas coordinates, after the gestures have let it through. */
  onTap?: (x: number, y: number) => void;
  /** A drag or a pinch has started - anything held open by a tap lets go. */
  onPanBegin?: () => void;
  /** A fling keeps scrolling after the finger lifts. */
  kinetic?: boolean;
}

export interface ChartEngine {
  size: SharedValue<Size>;
  /** Width of the price axis; shared with the oscillator bands so every slot lines up. */
  axisW: SharedValue<number>;
  axisH: number;
  spacing: SharedValue<number>;
  /** Bars between the newest bar and the right edge of the plot, now. */
  rightOffset: SharedValue<number>;
  /** The right offset the view keeps while following the live edge (lightweight-charts' `rightOffset` option). */
  restingOffset: SharedValue<number>;
  series: SharedValue<SeriesData>;
  /** Extra values the price scale fits to: the overlay indicators. */
  extents: SharedValue<number[][]>;
  /** Fit the price scale to closes only - a line or an area has no highs and lows. */
  closeOnly: SharedValue<boolean>;
  /** Smallest price step, for a flat range. */
  minMove: SharedValue<number>;
  crosshair: SharedValue<Crosshair>;
  view: DerivedValue<View>;
  gesture: ReturnType<typeof Gesture.Race>;
  minSpacing: number;
  maxSpacing: number;

  /** Reads the view on the JS thread (a synchronous hop to the UI thread). Not during render. */
  readView: () => View;
  isFollowing: () => boolean;
  scrollToRealTime: () => void;
  /** Changes the resting offset, moving the view with it when it is following. */
  setRestingOffset: (offset: number) => void;
  /** Zooms by a factor: about the live edge when following, about the middle otherwise. */
  zoom: (factor: number, min?: number, max?: number) => void;
  /** Every bar on screen. */
  fitContent: () => void;
  showCrosshairAt: (x: number, y: number) => void;
  hideCrosshair: () => void;
}

/** A press held this long becomes the crosshair rather than a tap. */
const LONG_PRESS_MS = 260;
/** Movement that turns a press into a pan rather than a tap (touch slop). */
const TAP_SLOP = 10;
/** A press held longer than this is not a tap. */
const TAP_MS = 700;

export function useChartEngine(options: EngineOptions): ChartEngine {
  const initialSpacing = options.initialSpacing ?? 10;
  const initialResting = options.restingOffset ?? 6;
  const minSpacing = options.minSpacing ?? 0.5;
  const maxSpacing = options.maxSpacing ?? 50;
  const axisH = options.axisH ?? 28;
  const kinetic = options.kinetic ?? true;
  const marginTop = options.margins.top;
  const marginBottom = options.margins.bottom;

  const size = useSharedValue<Size>({ w: 0, h: 0 });
  const axisW = useSharedValue(56);
  const spacing = useSharedValue(initialSpacing);
  const rightOffset = useSharedValue(initialResting);
  const restingOffset = useSharedValue(initialResting);
  const series = useSharedValue<SeriesData>(EMPTY_SERIES);
  const extents = useSharedValue<number[][]>([]);
  const closeOnly = useSharedValue(false);
  const minMove = useSharedValue(0.00001);
  const crosshair = useSharedValue<Crosshair>(HIDDEN_CROSSHAIR);

  const view = useDerivedValue<View>(() => {
    const data = series.value;
    const base = horizontal({
      size: size.value,
      axisW: axisW.value,
      axisH,
      spacing: spacing.value,
      rightOffset: rightOffset.value,
      count: data.t.length,
    });
    return fitPrices(
      base,
      data,
      extents.value,
      closeOnly.value,
      { top: marginTop, bottom: marginBottom },
      minMove.value,
      null,
    );
  }, [axisH, marginTop, marginBottom]);

  // The callbacks change every render; the gestures are built once.
  const tapRef = useRef(options.onTap);
  const panBeginRef = useRef(options.onPanBegin);
  tapRef.current = options.onTap;
  panBeginRef.current = options.onPanBegin;
  const handleTap = useCallback((x: number, y: number) => tapRef.current?.(x, y), []);
  const handlePanBegin = useCallback(() => panBeginRef.current?.(), []);

  const panStart = useSharedValue({ offset: 0, spacing: 0, axis: false });
  const pinchStart = useSharedValue({ offset: 0, spacing: 0, logical: 0, x: 0, follow: false });
  const pinching = useSharedValue(false);

  const gesture = useMemo(() => {
    const placeCrosshair = (x: number, y: number) => {
      'worklet';
      const current = view.value;
      if (!current.ok) return;
      const cx = x < 0 ? 0 : x > current.pw ? current.pw : x;
      const cy = y < 0 ? 0 : y > current.ph ? current.ph : y;
      crosshair.value = { visible: true, index: indexAt(current, cx), y: cy };
    };

    const pan = Gesture.Pan()
      .minDistance(6)
      .onStart((event) => {
        const current = view.value;
        cancelAnimation(rightOffset);
        cancelAnimation(spacing);
        panStart.value = {
          offset: rightOffset.value,
          spacing: spacing.value,
          axis: axisH > 0 && event.y > current.ph && event.x < current.pw,
        };
        if (crosshair.value.visible) crosshair.value = HIDDEN_CROSSHAIR;
        runOnJS(handlePanBegin)();
      })
      .onUpdate((event) => {
        if (pinching.value) return;
        const start = panStart.value;
        const current = view.value;
        if (start.axis) {
          // Dragging the time axis scales time; the right edge stays put.
          const next = start.spacing * Math.exp(event.translationX / 220);
          spacing.value = next < minSpacing ? minSpacing : next > maxSpacing ? maxSpacing : next;
          return;
        }
        rightOffset.value = clampOffset(start.offset - event.translationX / start.spacing, current.count, current.pw, start.spacing);
      })
      .onEnd((event) => {
        const start = panStart.value;
        if (start.axis || pinching.value || !kinetic) return;
        const current = view.value;
        const s = current.s;
        if (!(s > 0)) return;
        const max = clampOffset(Number.MAX_SAFE_INTEGER, current.count, current.pw, s);
        const min = clampOffset(-Number.MAX_SAFE_INTEGER, current.count, current.pw, s);
        rightOffset.value = withDecay({ velocity: -event.velocityX / s, deceleration: 0.997, clamp: [min, max] });
      });

    const pinch = Gesture.Pinch()
      .onStart((event) => {
        const current = view.value;
        pinching.value = true;
        cancelAnimation(rightOffset);
        cancelAnimation(spacing);
        if (crosshair.value.visible) crosshair.value = HIDDEN_CROSSHAIR;
        pinchStart.value = {
          offset: rightOffset.value,
          spacing: spacing.value,
          logical: logicalAt(current, event.focalX),
          x: event.focalX,
          follow: Math.abs(rightOffset.value - restingOffset.value) < 1.5,
        };
        runOnJS(handlePanBegin)();
      })
      .onUpdate((event) => {
        const start = pinchStart.value;
        const current = view.value;
        const raw = start.spacing * event.scale;
        const next = raw < minSpacing ? minSpacing : raw > maxSpacing ? maxSpacing : raw;
        spacing.value = next;
        // Following the live edge, the newest candle stays where it is; in
        // history, the bar under the fingers does.
        if (!start.follow) {
          rightOffset.value = clampOffset(
            offsetKeeping(current.pw, current.count, start.logical, start.x, next),
            current.count,
            current.pw,
            next,
          );
        }
      })
      .onFinalize(() => {
        pinching.value = false;
      });

    const cross = Gesture.Pan()
      .activateAfterLongPress(LONG_PRESS_MS)
      .onStart((event) => placeCrosshair(event.x, event.y))
      .onUpdate((event) => placeCrosshair(event.x, event.y));

    const tap = Gesture.Tap()
      .maxDuration(TAP_MS)
      .maxDistance(TAP_SLOP)
      .onEnd((event, success) => {
        if (success) runOnJS(handleTap)(event.x, event.y);
      });

    return Gesture.Race(cross, Gesture.Simultaneous(pan, pinch), tap);
    // Built once: everything it touches is a shared value or a stable callback.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const isFollowing = useCallback(
    () => Math.abs(rightOffset.value - restingOffset.value) < 1.5,
    [rightOffset, restingOffset],
  );

  const scrollToRealTime = useCallback(() => {
    rightOffset.value = withTiming(restingOffset.value, { duration: 260 });
  }, [rightOffset, restingOffset]);

  const setRestingOffset = useCallback(
    (offset: number) => {
      const following = Math.abs(rightOffset.value - restingOffset.value) < 1.5;
      restingOffset.value = offset;
      if (following) rightOffset.value = withTiming(offset, { duration: 220 });
    },
    [rightOffset, restingOffset],
  );

  const zoom = useCallback(
    (factor: number, min = 2, max = 48) => {
      const current = view.value;
      const s = spacing.value;
      const next = Math.min(max, Math.max(min, s * factor));
      if (Math.abs(next - s) < 0.01) return;
      if (Math.abs(rightOffset.value - restingOffset.value) < 1.5) {
        spacing.value = next;
        return;
      }
      const centre = current.pw / 2;
      const logical = logicalAt(current, centre);
      spacing.value = next;
      rightOffset.value = clampOffset(offsetKeeping(current.pw, current.count, logical, centre, next), current.count, current.pw, next);
    },
    [view, spacing, rightOffset, restingOffset],
  );

  const fitContent = useCallback(() => {
    const current = view.value;
    if (current.count === 0 || current.pw <= 0) return;
    const resting = restingOffset.value;
    const next = Math.max(minSpacing, Math.min(maxSpacing, current.pw / (current.count + resting)));
    spacing.value = next;
    rightOffset.value = resting;
  }, [view, spacing, rightOffset, restingOffset, minSpacing, maxSpacing]);

  const showCrosshairAt = useCallback(
    (x: number, y: number) => {
      const current = view.value;
      if (!current.ok) return;
      crosshair.value = {
        visible: true,
        index: indexAt(current, Math.max(0, Math.min(current.pw, x))),
        y: Math.max(0, Math.min(current.ph, y)),
      };
    },
    [view, crosshair],
  );

  const hideCrosshair = useCallback(() => {
    crosshair.value = HIDDEN_CROSSHAIR;
  }, [crosshair]);

  const readView = useCallback(() => view.value, [view]);

  return useMemo(
    () => ({
      size,
      axisW,
      axisH,
      spacing,
      rightOffset,
      restingOffset,
      series,
      extents,
      closeOnly,
      minMove,
      crosshair,
      view,
      gesture,
      minSpacing,
      maxSpacing,
      readView,
      isFollowing,
      scrollToRealTime,
      setRestingOffset,
      zoom,
      fitContent,
      showCrosshairAt,
      hideCrosshair,
    }),
    // Every member is stable for the life of the chart.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );
}
