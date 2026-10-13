import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import clsx from 'clsx';
import { ChevronDown, ChevronUp, X } from 'lucide-react';
import { useTradeResultStore } from '../../stores/tradeResultStore';
import { useSettingsStore } from '../../stores/settingsStore';
import { useWorkspaceStore } from '../../stores/workspaceStore';
import { formatExpiry, formatMoney, formatSignedMoney } from '../../utils/format';
import { durationOf, statusLabel } from '../../utils/tradeStatus';
import { payoutOf, settlementOf } from './settlement';
import { useTicketStore } from './ticketStore';
import { DirectionWord, TradeDemoTag, useClockFormatter } from './tradeBits';
import type { Trade } from '../../types/api';

/**
 * The result of a trade, announced as it settles (brief UX-13).
 *
 * Deliberately not a toast. The toasts carry things that happened to the
 * session - an order accepted, a request refused - and are worded as sentences.
 * This is the answer the trader is waiting on, so it is built like a receipt:
 * the status in words, the terms the trade ran under, what came back and the
 * net result, and three ways on - Trade Again, View Trade, View History.
 *
 * The newest result stays until it is closed; older ones fold under it as
 * "+N more". Nothing promotional ever shares this card.
 *
 * A win is gold and a loss is plain. That asymmetry is on purpose - a loss is
 * already a zero, and dressing it in red would make it louder than the wins.
 *
 * The "hide trade results" setting silences it entirely; the results are
 * still in the history.
 */
export function TradeResultToasts() {
  const results = useTradeResultStore((s) => s.results);
  const dismiss = useTradeResultStore((s) => s.dismiss);
  const dismissAll = useTradeResultStore((s) => s.dismissAll);
  const hidden = useSettingsStore((s) => s.hideTradeResults);
  const [expanded, setExpanded] = useState(false);

  if (hidden || results.length === 0) return null;

  const [latest, ...older] = results as [Trade, ...Trade[]];

  // Above the bottom nav on a phone. On a wide screen, under the top bar: the
  // bottom centre there is the chart's zoom and back-to-latest controls.
  return (
    <div
      className="pointer-events-none fixed bottom-[calc(4.25rem+env(safe-area-inset-bottom))] left-1/2 z-50 flex w-[min(400px,calc(100vw-2rem))]
                 -translate-x-1/2 flex-col gap-2 lg:bottom-auto lg:top-[4.75rem]"
      role="status"
      aria-live="polite"
    >
      <ResultCard trade={latest} onClose={() => dismiss(latest.id)} />

      {older.length > 0 ? (
        <div className="pointer-events-auto rounded-2xl bg-surface-850 shadow-overlay ring-1 ring-surface-700">
          <div className="flex items-center">
            <button
              type="button"
              onClick={() => setExpanded((value) => !value)}
              aria-expanded={expanded}
              className="flex flex-1 items-center gap-1.5 px-3 py-2 text-left text-xs font-semibold text-content-300 hover:text-content-100"
            >
              {expanded ? <ChevronDown className="h-4 w-4" /> : <ChevronUp className="h-4 w-4" />}+{older.length} more
              result{older.length === 1 ? '' : 's'}
            </button>
            <button
              type="button"
              onClick={() => {
                dismissAll();
                setExpanded(false);
              }}
              className="px-3 py-2 text-xs font-semibold text-content-400 hover:text-content-100"
            >
              Clear all
            </button>
          </div>
          {expanded ? (
            <ul className="max-h-48 divide-y divide-surface-700/60 overflow-y-auto border-t border-surface-700/60">
              {older.map((trade) => (
                <OlderRow key={trade.id} trade={trade} onClose={() => dismiss(trade.id)} />
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function ResultCard({ trade, onClose }: { trade: Trade; onClose: () => void }) {
  const navigate = useNavigate();
  const openAsset = useWorkspaceStore((s) => s.openAsset);
  const prefill = useTicketStore((s) => s.prefill);
  const clock = useClockFormatter();

  const net = settlementOf(trade) ?? 0;
  const returned = payoutOf(trade);
  const won = net > 0;

  function tradeAgain() {
    // Same asset, same duration, same amount - and the ticket takes focus, so
    // the only thing left to choose is the direction.
    openAsset(trade.assetId);
    prefill(trade.stake, durationOf(trade));
    onClose();
    navigate('/trade');
  }

  return (
    <article
      aria-label={'Result: ' + statusLabel(trade.status) + ' ' + trade.symbol}
      className={clsx(
        'pointer-events-auto animate-fade-in rounded-2xl bg-surface-850 p-3 shadow-overlay ring-1',
        won ? 'ring-brand/50' : 'ring-surface-700',
      )}
    >
      <header className="flex items-center gap-2">
        <span
          className={clsx(
            'rounded-md px-2 py-0.5 text-xs font-bold',
            won ? 'bg-brand text-surface-950' : 'bg-content-100 text-surface-950',
          )}
        >
          {statusLabel(trade.status)}
        </span>
        <span className="min-w-0 flex-1 truncate text-sm font-semibold text-content-100">{trade.symbol}</span>
        <TradeDemoTag trade={trade} />
        <button
          type="button"
          onClick={onClose}
          aria-label={'Close result for ' + trade.symbol}
          className="grid h-7 w-7 shrink-0 place-items-center rounded-lg text-content-400 transition hover:bg-surface-800 hover:text-content-100"
        >
          <X className="h-4 w-4" />
        </button>
      </header>

      <dl className="mt-2 grid grid-cols-3 gap-x-3 gap-y-1.5 text-[11px]">
        <Cell label="Direction">
          <DirectionWord direction={trade.direction} />
        </Cell>
        <Cell label="Investment">{formatMoney(trade.stake, trade.currency)}</Cell>
        <Cell label="Expiry">
          {formatExpiry(durationOf(trade))} · {clock(trade.expiryTimestamp)}
        </Cell>
        <Cell label="Returned">{formatMoney(returned, trade.currency)}</Cell>
        <Cell label="Net result" wide>
          <span className={clsx('text-base', won ? 'text-brand' : 'text-content-100')}>
            {formatSignedMoney(net, trade.currency)}
          </span>
        </Cell>
      </dl>

      <div className="mt-3 grid grid-cols-3 gap-1.5">
        <button type="button" onClick={tradeAgain} className="btn-primary px-2 py-2 text-xs">
          Trade Again
        </button>
        <button
          type="button"
          onClick={() => {
            onClose();
            navigate('/trades/' + trade.id);
          }}
          className="btn-ghost px-2 py-2 text-xs"
        >
          View Trade
        </button>
        <button
          type="button"
          onClick={() => {
            onClose();
            navigate('/trades?tab=history');
          }}
          className="btn-ghost px-2 py-2 text-xs"
        >
          View History
        </button>
      </div>
    </article>
  );
}

function Cell({ label, children, wide }: { label: string; children: React.ReactNode; wide?: boolean }) {
  return (
    <div className={clsx('min-w-0', wide && 'col-span-2')}>
      <dt className="text-content-400">{label}</dt>
      <dd className="truncate font-semibold tnum text-content-100">{children}</dd>
    </div>
  );
}

function OlderRow({ trade, onClose }: { trade: Trade; onClose: () => void }) {
  const navigate = useNavigate();
  const net = settlementOf(trade) ?? 0;
  return (
    <li className="flex items-center gap-2 px-3 py-1.5 text-xs">
      <button
        type="button"
        onClick={() => {
          onClose();
          navigate('/trades/' + trade.id);
        }}
        className="flex min-w-0 flex-1 items-center gap-2 text-left"
      >
        <span className="w-16 shrink-0 font-semibold text-content-300">{statusLabel(trade.status)}</span>
        <span className="min-w-0 flex-1 truncate font-semibold text-content-100">{trade.symbol}</span>
        <TradeDemoTag trade={trade} />
        <span className={clsx('font-bold tnum', net > 0 ? 'text-brand' : 'text-content-100')}>
          {formatSignedMoney(net, trade.currency)}
        </span>
      </button>
      <button
        type="button"
        onClick={onClose}
        aria-label={'Close result for ' + trade.symbol}
        className="grid h-6 w-6 shrink-0 place-items-center rounded text-content-400 hover:text-content-100"
      >
        <X className="h-3.5 w-3.5" />
      </button>
    </li>
  );
}
