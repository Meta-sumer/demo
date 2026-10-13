import { useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent } from 'react';
import clsx from 'clsx';
import { X } from 'lucide-react';
import { useWorkspaceStore } from '../../stores/workspaceStore';
import { useFavouritesStore } from '../../stores/favouritesStore';
import { usePanelTransition } from '../../hooks/usePanelTransition';
import { AssetBrowser } from '../../components/AssetBrowser';
import type { Asset } from '../../types/api';

/**
 * Asset selector inside Trade (brief UX-03).
 *
 * On a phone it is a bottom sheet: it rises from where the thumb already is,
 * keeps the chart visible above it, and goes back down with a drag, a tap on
 * the scrim or Escape. On a wide screen it is a compact panel anchored by the
 * top-left of the trading area, next to the asset header that opened it,
 * instead of a dialog in the middle of the chart.
 *
 * The body - search, Favourites / Recent / categories and the rows - is the
 * same component the Markets page uses, so the two can never disagree.
 *
 * Mounted by the trade page; opened through `useWorkspaceStore.setAssetPickerOpen`.
 */
const TRANSITION_MS = 280;
const EASE = 'ease-[cubic-bezier(0.32,0.72,0,1)]';
/** How far the sheet has to be dragged down before letting go closes it. */
const DISMISS_DRAG_PX = 90;

export function AssetPicker({ assets }: { assets: Asset[] }) {
  const open = useWorkspaceStore((s) => s.assetPickerOpen);
  const setOpen = useWorkspaceStore((s) => s.setAssetPickerOpen);
  const openAsset = useWorkspaceStore((s) => s.openAsset);
  const activeAssetId = useWorkspaceStore((s) => s.activeAssetId);
  const settingsOpen = useWorkspaceStore((s) => s.settingsOpen);
  const pushRecent = useFavouritesStore((s) => s.pushRecent);

  const { mounted, entered } = usePanelTransition(open, TRANSITION_MS);
  const panelRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const [drag, setDrag] = useState<{ startY: number; dy: number } | null>(null);

  const close = () => setOpen(false);

  // Remember what opened it, and give focus back there on the way out, so a
  // keyboard user is not dropped at the top of the document.
  useEffect(() => {
    if (open) {
      returnFocusRef.current = document.activeElement as HTMLElement | null;
      return;
    }
    const target = returnFocusRef.current;
    returnFocusRef.current = null;
    if (target && document.contains(target)) target.focus();
  }, [open]);

  // Wide screens get the cursor in the search box straight away. On a phone
  // that would raise the keyboard over half the sheet before the customer has
  // seen their favourites, so the sheet itself takes focus instead.
  useEffect(() => {
    if (!entered) return;
    if (window.matchMedia('(min-width: 1024px)').matches) searchRef.current?.focus();
    else panelRef.current?.focus();
  }, [entered]);

  if (!mounted) return null;

  function pick(asset: Asset) {
    openAsset(asset.id);
    pushRecent(asset.id);
  }

  /** Escape closes; Tab cycles inside the panel while it is open. */
  function onKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    if (event.key === 'Escape') {
      event.stopPropagation();
      close();
      return;
    }
    if (event.key !== 'Tab' || !panelRef.current) return;
    const focusable = Array.from(
      panelRef.current.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), [href], [tabindex]:not([tabindex="-1"])',
      ),
    );
    if (focusable.length === 0) return;
    const first = focusable[0]!;
    const last = focusable[focusable.length - 1]!;
    if (event.shiftKey && (document.activeElement === first || document.activeElement === panelRef.current)) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  function onHandleDown(event: PointerEvent<HTMLDivElement>) {
    event.currentTarget.setPointerCapture(event.pointerId);
    setDrag({ startY: event.clientY, dy: 0 });
  }
  function onHandleMove(event: PointerEvent<HTMLDivElement>) {
    if (!drag) return;
    setDrag({ ...drag, dy: Math.max(0, event.clientY - drag.startY) });
  }
  function onHandleUp() {
    if (drag && drag.dy > DISMISS_DRAG_PX) close();
    setDrag(null);
  }

  return (
    <div className="fixed inset-0 z-50" role="presentation" onKeyDown={onKeyDown}>
      {/* The scrim is light on a wide screen: the panel is a dropdown there,
          and the chart behind it is still what the trader is looking at. */}
      <button
        type="button"
        aria-label="Close market selector"
        tabIndex={-1}
        onClick={close}
        className={clsx(
          'absolute inset-0 bg-scrim/60 backdrop-blur-[2px] transition-opacity lg:bg-scrim/20 lg:backdrop-blur-0',
          EASE,
          entered ? 'opacity-100' : 'opacity-0',
        )}
        style={{ transitionDuration: TRANSITION_MS + 'ms' }}
      />

      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label="Choose a market"
        tabIndex={-1}
        className={clsx(
          'absolute flex flex-col overflow-hidden border border-surface-700 bg-surface-900 shadow-overlay outline-none',
          // Phone: a sheet from the bottom edge, never taller than 85% of the
          // screen so the chart stays in view above it.
          'inset-x-0 bottom-0 max-h-[85svh] rounded-t-2xl pb-[env(safe-area-inset-bottom)]',
          // Desktop: a panel under the top bar, beside the rail (and beside
          // the settings drawer when that is open).
          'lg:bottom-auto lg:right-auto lg:top-[72px] lg:max-h-[min(640px,calc(100vh-88px))] lg:w-[400px] lg:rounded-xl lg:pb-0',
          settingsOpen ? 'lg:left-[398px]' : 'lg:left-[68px]',
          !drag && 'transition-[transform,opacity]',
          EASE,
          entered ? 'translate-y-0 opacity-100' : 'translate-y-full opacity-0 lg:-translate-y-2',
        )}
        style={{
          transitionDuration: TRANSITION_MS + 'ms',
          transform: drag && drag.dy > 0 ? `translateY(${drag.dy}px)` : undefined,
        }}
      >
        {/* Drag handle - the whole strip is the grab area, not just the pill. */}
        <div
          className="flex shrink-0 cursor-grab touch-none justify-center pb-1 pt-2.5 lg:hidden"
          onPointerDown={onHandleDown}
          onPointerMove={onHandleMove}
          onPointerUp={onHandleUp}
          onPointerCancel={() => setDrag(null)}
          aria-hidden="true"
        >
          <span className="h-1 w-10 rounded-full bg-surface-600" />
        </div>

        <div className="flex shrink-0 items-center justify-between px-4 pb-2 pt-1 lg:pt-3">
          <h2 className="text-base font-bold text-content-100">Choose a market</h2>
          <button
            type="button"
            onClick={close}
            className="-mr-1.5 rounded-lg p-1.5 text-content-400 transition hover:bg-surface-850 hover:text-content-100"
            aria-label="Close"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        <AssetBrowser
          assets={assets}
          loading={assets.length === 0}
          onPick={pick}
          selectedId={activeAssetId}
          searchRef={searchRef}
          className="flex-1"
          headerClassName="shrink-0 border-b border-surface-800 px-4 pb-3"
          listClassName="flex-1 overflow-y-auto overscroll-contain"
        />
      </div>
    </div>
  );
}
