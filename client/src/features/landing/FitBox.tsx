import { useEffect, useRef, useState, type ReactNode } from 'react';

/**
 * Renders fixed-size content scaled to whatever width it is given.
 *
 * The workspace mock is built at one size, in pixels, because that is the only
 * way its type, its rails and its panels keep the proportions of the real app.
 * Dropped straight into a responsive layout that backfires: its minimum
 * content width becomes the grid track's minimum, and on a phone the column
 * grows wider than the screen instead of the mock getting smaller.
 *
 * Scaling sidesteps that. The content is laid out at `width` x `height` and
 * then transformed to fit, so the box reports only the size it was given and
 * the picture shrinks as one piece - type, borders and spacing together -
 * rather than reflowing into a different, wrong layout.
 *
 * A ResizeObserver rather than a media query: what matters is the width of
 * this box, which depends on the grid around it, not on the viewport.
 */
export function FitBox({
  width,
  height,
  children,
  className,
}: {
  width: number;
  height: number;
  children: ReactNode;
  className?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [scale, setScale] = useState(1);

  useEffect(() => {
    const node = ref.current;
    if (!node) return;

    const measure = () => {
      const available = node.clientWidth;
      if (available > 0) setScale(available / width);
    };

    measure();
    if (typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(measure);
    observer.observe(node);
    return () => observer.disconnect();
  }, [width]);

  return (
    <div ref={ref} className={className} style={{ aspectRatio: `${width} / ${height}`, overflow: 'hidden' }}>
      <div
        style={{
          width,
          height,
          transformOrigin: 'top left',
          transform: `scale(${scale})`,
        }}
      >
        {children}
      </div>
    </div>
  );
}
