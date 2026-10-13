import { useCallback, useEffect, useLayoutEffect, useState, type RefObject } from 'react';
import type { IChartApi } from 'lightweight-charts';
import clsx from 'clsx';
import { ArrowRightToLine, Minus, Plus } from 'lucide-react';
import { isFollowingRealTime } from './useFrameLoop';

/**
 * Zoom out, zoom in, and back to the latest price (record 08).
 *
 * Every reference platform has these, and they matter most on touch: a pinch
 * cannot be made precise, and a trader who has dragged back through history
 * needs one tap to return to now rather than a long swipe the other way.
 *
 * Bottom-centre of the plot, in the toolbar's row. Where the toolbar leaves no
 * room there - a phone in advanced mode - the pair stands upright at the right
 * edge of the plot instead, in the gap the chart keeps ahead of the newest
 * candle, rather than landing on top of the toolbar or the candles.
 *
 * "Back to latest" appears only once the view has left the live edge. It hangs
 * off the end of the pair rather than sitting inside it, so the zoom buttons
 * do not shift under the thumb when it comes and goes.
 */
interface Props {
  chart: IChartApi | null;
  /** The toolbar in the same row, which the controls must stay clear of. */
  toolbarRef: RefObject<HTMLElement | null>;
  /** Distance from the bottom of the chart box, matching the toolbar's row. */
  bottom: number;
}

/** 36px targets: the smallest a thumb hits reliably (brief: touch-friendly). */
const BUTTON = 36;
const GAP = 4;
/** The pair. */
const PAIR = BUTTON * 2 + GAP;
/** "Back to latest", with the gap before it. */
const LATEST = BUTTON + GAP;
/** Clearance from the toolbar and from the price axis. */
const CLEAR = 8;

/** One press zooms by this factor. */
const STEP = 1.3;
/** Bar spacing bounds, in pixels: from a wide overview to a handful of candles. */
const MIN_SPACING = 2;
const MAX_SPACING = 48;

type Layout = { kind: 'row'; left: number } | { kind: 'column'; left: number };

export function ChartZoomControls({ chart, toolbarRef, bottom }: Props) {
  const [following, setFollowing] = useState(true);
  const [layout, setLayout] = useState<Layout | null>(null);

  // Whether "back to latest" is needed, from every movement of the view.
  useEffect(() => {
    if (!chart) return;
    const scale = chart.timeScale();
    const update = () => setFollowing(isFollowingRealTime(chart));
    update();
    scale.subscribeVisibleLogicalRangeChange(update);
    return () => scale.unsubscribeVisibleLogicalRangeChange(update);
  }, [chart]);

  /*
   * Placement. Measured, not guessed: the toolbar's width depends on the mode
   * and the price axis's on the instrument, and a fixed breakpoint would be
   * wrong for one of them. Room is judged with "back to latest" showing, so
   * the controls do not jump between the two layouts as it comes and goes.
   */
  useLayoutEffect(() => {
    if (!chart) return;
    const scale = chart.timeScale();

    const measure = () => {
      const plot = scale.width();
      if (plot <= 0) return;
      const toolbar = toolbarRef.current;
      const toolbarRight = toolbar ? toolbar.offsetLeft + toolbar.offsetWidth : 0;
      const left = Math.max(plot / 2 - PAIR / 2, toolbarRight + CLEAR);
      const next: Layout =
        left + PAIR + LATEST <= plot - CLEAR
          ? { kind: 'row', left: Math.round(left) }
          : { kind: 'column', left: Math.round(plot - CLEAR - BUTTON) };
      setLayout((previous) =>
        previous && previous.kind === next.kind && previous.left === next.left ? previous : next,
      );
    };

    measure();
    scale.subscribeSizeChange(measure);
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure);
    if (observer && toolbarRef.current) observer.observe(toolbarRef.current);
    return () => {
      scale.unsubscribeSizeChange(measure);
      observer?.disconnect();
    };
  }, [chart, toolbarRef]);

  /**
   * Following the live edge, a zoom keeps the newest candle where it is and
   * changes the bar width around it - the natural zoom for someone watching a
   * trade run. Anywhere else it zooms about the middle of the view, so the
   * stretch of history being read stays in front of the reader.
   */
  const zoom = useCallback(
    (factor: number) => {
      if (!chart) return;
      const scale = chart.timeScale();
      const spacing = scale.options().barSpacing;
      const next = Math.min(MAX_SPACING, Math.max(MIN_SPACING, spacing * factor));
      if (Math.abs(next - spacing) < 0.01) return;

      if (isFollowingRealTime(chart)) {
        scale.applyOptions({ barSpacing: next });
        return;
      }
      const range = scale.getVisibleLogicalRange();
      if (!range) return;
      const centre = (range.from + range.to) / 2;
      const half = ((range.to - range.from) / 2) * (spacing / next);
      scale.setVisibleLogicalRange({ from: centre - half, to: centre + half });
    },
    [chart],
  );

  const backToLatest = useCallback(() => chart?.timeScale().scrollToRealTime(), [chart]);

  if (!chart || !layout) return null;
  const column = layout.kind === 'column';

  return (
    <div
      className={clsx(
        // Just under the toolbar's layer, so a toolbar popover opening over
        // the controls covers them rather than the other way round.
        'pointer-events-none absolute z-[19] flex items-center gap-1',
        column ? 'flex-col-reverse' : 'flex-row',
      )}
      style={{ left: layout.left, bottom }}
      data-part="zoom-controls"
    >
      <div
        role="group"
        aria-label="Chart zoom"
        className={clsx('pointer-events-auto flex gap-1', column ? 'flex-col' : 'flex-row')}
      >
        {column ? (
          <>
            <ZoomButton label="Zoom in" onClick={() => zoom(STEP)}>
              <Plus className="h-4 w-4" />
            </ZoomButton>
            <ZoomButton label="Zoom out" onClick={() => zoom(1 / STEP)}>
              <Minus className="h-4 w-4" />
            </ZoomButton>
          </>
        ) : (
          <>
            <ZoomButton label="Zoom out" onClick={() => zoom(1 / STEP)}>
              <Minus className="h-4 w-4" />
            </ZoomButton>
            <ZoomButton label="Zoom in" onClick={() => zoom(STEP)}>
              <Plus className="h-4 w-4" />
            </ZoomButton>
          </>
        )}
      </div>

      {following ? null : (
        <button
          type="button"
          onClick={backToLatest}
          title="Back to latest price"
          aria-label="Back to latest price"
          className={clsx(
            'pointer-events-auto grid h-9 w-9 place-items-center rounded-lg border animate-fade-in',
            'border-accent/70 bg-surface-900/85 text-content-100 backdrop-blur transition hover:border-accent',
          )}
        >
          <ArrowRightToLine className="h-4 w-4" />
        </button>
      )}
    </div>
  );
}

function ZoomButton({ label, onClick, children }: { label: string; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={label}
      aria-label={label}
      className={clsx(
        'grid h-9 w-9 place-items-center rounded-lg border border-surface-700/70 bg-surface-900/80 text-content-300',
        'backdrop-blur transition hover:border-surface-600 hover:text-content-100 active:scale-95',
      )}
    >
      {children}
    </button>
  );
}
