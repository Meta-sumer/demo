import type { ReactNode } from 'react';

/**
 * Padding and width for the ordinary pages.
 *
 * The trading screen deliberately opts out: it fills the viewport and manages
 * its own regions, so a centred max-width container would waste the space the
 * chart needs.
 */
export function PageContainer({ children }: { children: ReactNode }) {
  return <div className="mx-auto w-full max-w-[1600px] px-3 py-4 sm:px-5">{children}</div>;
}
