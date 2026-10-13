import { useRef, type MutableRefObject, type RefObject } from 'react';
import type { IChartApi } from 'lightweight-charts';
import { ArrowRight, Flag } from 'lucide-react';
import { formatClock } from '../../../utils/time';
import { withAlpha, type ChartPalette } from '../../../utils/theme-colors';
import { readLogicalAxis, timeToX } from './timeProjection';
import { clamp, useFrameLoop } from './useFrameLoop';

/**
 * Where a trade placed now would end (records 07 and 10).
 *
 * Before anything is placed, the chart shows the expiry of the duration the
 * ticket is set to: a vertical line at now plus that duration, with the clock
 * time on the time axis. It moves with the clock and jumps when the duration
 * changes, so changing 1m to 5m visibly moves the end of the trade rather than
 * only changing a number in the ticket (UX-07).
 *
 * It is a guide, not a position, and is drawn so it cannot be mistaken for one:
 *
 *   - neutral grey and dotted, where an order is yellow, green or red and
 *     dashed;
 *   - its label is a clock time on the time axis, where an order's is a
 *     countdown at the top of the plot;
 *   - no price, no entry, no tag - nothing in it says an amount is at stake.
 *
 * The page hides it while an order preview is on the chart: the preview is the
 * same line with a direction and an amount, and two of them on one instant
 * would only be noise.
 *
 * Like the order overlay it lives in the DOM and a frame loop moves it, so the
 * canvas is never redrawn for it.
 */
interface Props {
  chartRef: MutableRefObject<IChartApi | null>;
  containerRef: MutableRefObject<HTMLDivElement | null>;
  barsRef: RefObject<ReadonlyArray<{ time: number }>>;
  bucketMs: number;
  /** The ticket's duration. */
  expirySeconds: number;
  zone: string;
  palette: ChartPalette;
  performanceMode: boolean;
}

/** Clearance the label keeps from the ends of the time axis. */
const EDGE = 2;

export function ExpiryGuide({
  chartRef,
  containerRef,
  barsRef,
  bucketMs,
  expirySeconds,
  zone,
  palette,
  performanceMode,
}: Props) {
  const lineRef = useRef<HTMLDivElement | null>(null);
  const labelRef = useRef<HTMLDivElement | null>(null);
  const timeRef = useRef<HTMLSpanElement | null>(null);
  const writtenRef = useRef({
    x: Number.NaN,
    height: -1,
    top: -1,
    left: Number.NaN,
    clamped: false,
    text: '',
    labelWidth: 0,
    labelHeight: 0,
  });

  useFrameLoop(
    () => {
      const chart = chartRef.current;
      const container = containerRef.current;
      const line = lineRef.current;
      const label = labelRef.current;
      if (!chart || !container || !line || !label) return;

      const written = writtenRef.current;
      const scale = chart.timeScale();
      const plotWidth = scale.width();
      const axisHeight = scale.height();
      const plotHeight = container.clientHeight - axisHeight;
      const expiry = Date.now() + expirySeconds * 1000;
      const raw = timeToX(readLogicalAxis(chart), expiry, barsRef.current ?? [], bucketMs);

      if (raw === null || plotWidth <= 0 || plotHeight <= 0) {
        if (line.style.visibility !== 'hidden') {
          line.style.visibility = 'hidden';
          label.style.visibility = 'hidden';
          written.x = Number.NaN;
        }
        return;
      }

      // Past the right edge - a long duration on a short frame - the line is
      // held at the edge and says so, rather than silently disappearing.
      const x = clamp(raw, 0, plotWidth - 1);
      const clamped = raw > plotWidth - 1;

      // Written only when the second changes, which is all the label shows -
      // and measured only then, so the loop does not force a layout a frame.
      const text = formatClock(expiry, zone);
      if (text !== written.text && timeRef.current) {
        timeRef.current.textContent = text;
        written.text = text;
        written.labelWidth = label.offsetWidth;
        written.labelHeight = label.offsetHeight;
      }

      if (Math.abs(x - written.x) >= 0.5 || plotHeight !== written.height || clamped !== written.clamped) {
        line.style.transform = 'translate3d(' + x + 'px, 0, 0)';
        line.style.height = plotHeight + 'px';
        line.style.opacity = clamped ? '0.5' : '1';
        if (clamped !== written.clamped) {
          // The arrow changes the label's width; measure it again.
          label.dataset.clamped = clamped ? 'true' : 'false';
          written.labelWidth = label.offsetWidth;
        }
        written.x = x;
        written.height = plotHeight;
        written.clamped = clamped;
      }

      // On the time axis, centred on the line - that is where an instant is
      // read. Without an axis (a very short chart), just above the plot's
      // foot instead.
      const width = written.labelWidth || 110;
      const height = written.labelHeight || 18;
      const left = clamp(x - width / 2, EDGE, plotWidth - width - EDGE);
      const top = axisHeight > height ? plotHeight + (axisHeight - height) / 2 : plotHeight - height - 4;
      if (Math.abs(left - written.left) >= 0.5 || top !== written.top) {
        label.style.transform = 'translate3d(' + left + 'px, ' + top + 'px, 0)';
        written.left = left;
        written.top = top;
      }

      if (line.style.visibility !== 'visible') {
        line.style.visibility = 'visible';
        label.style.visibility = 'visible';
      }
    },
    { maxFps: performanceMode ? 30 : 60 },
  );

  const rule = withAlpha(palette.text, 0.85);

  return (
    <div className="pointer-events-none absolute inset-0 z-[15] overflow-hidden" aria-hidden data-part="expiry-guide">
      <div ref={lineRef} className="absolute left-0 top-0 w-0" style={{ visibility: 'hidden' }}>
        {/* Dotted, and finer than an order's dashes. */}
        <div
          className="absolute inset-y-0 left-0 w-px"
          style={{ backgroundImage: 'repeating-linear-gradient(to bottom, ' + rule + ' 0 2px, transparent 2px 5px)' }}
        />
        {/* A pennant at the top: the finish of the trade, pointing on in time. */}
        <svg className="absolute left-0 top-0" width="11" height="12" viewBox="0 0 11 12" aria-hidden>
          <path d="M0.5 0.5h9.5l-2.6 3.2 2.6 3.2H0.5z" fill={rule} />
        </svg>
      </div>

      <div
        ref={labelRef}
        className="group absolute left-0 top-0 flex items-center gap-1 whitespace-nowrap rounded px-1.5 py-[3px] text-[10px] font-semibold leading-none"
        style={{
          visibility: 'hidden',
          color: palette.text,
          backgroundColor: withAlpha(palette.panel, 0.94),
          boxShadow: 'inset 0 0 0 1px ' + withAlpha(palette.text, 0.4),
        }}
      >
        <Flag className="h-3 w-3 shrink-0" strokeWidth={2.25} />
        Expires
        <span ref={timeRef} className="tnum font-bold" style={{ color: palette.strongText }}>
          --:--:--
        </span>
        {/* Only when the instant is past the right edge of the view. */}
        <ArrowRight className="hidden h-3 w-3 shrink-0 group-data-[clamped=true]:block" strokeWidth={2.5} />
      </div>
    </div>
  );
}
