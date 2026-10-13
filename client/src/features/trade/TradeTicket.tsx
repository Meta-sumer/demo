import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import { ArrowDown, ArrowUp, CircleHelp, History, Loader2, Minus, Plus, WifiOff } from 'lucide-react';
import { api } from '../../services/api';
import { ApiRequestError } from '../../services/http';
import { useSettingsStore, type TradeMode } from '../../stores/settingsStore';
import { useTradeStore } from '../../stores/tradeStore';
import { useMarketStore } from '../../stores/marketStore';
import { useAccountStore } from '../../stores/accountStore';
import { useOrderPreviewStore, type PreviewDraft } from '../../stores/orderPreviewStore';
import { currencySymbolFor, formatMoney, formatMoneyCompact, formatPrice } from '../../utils/format';
import { toast } from '../../hooks/useToast';
import { openFunds, useResetDemo } from '../account/funds';
import { ContractInfo, type ContractSummary } from './ContractInfo';
import { ConfirmTradeSheet } from './ConfirmTradeSheet';
import { DurationSheet } from './DurationSheet';
import { HowItWorksSheet } from './HowItWorks';
import { DemoTag } from './tradeBits';
import { useTicketStore } from './ticketStore';
import { useTradingWallet } from './useTradingWallet';
import { useConnectionState } from './useConnectionState';
import {
  amountMessage,
  amountPresets,
  amountState,
  amountStep,
  amountText,
  durationChips,
  durationRow,
  parseAmount,
  pickDuration,
  stakeLimits,
  stepAmount,
  type DurationChip,
} from './ticketModel';
import type { Asset, AssetSentiment, Direction } from '../../types/api';

/**
 * The trade ticket.
 *
 * The brief's fixed order (UX-04): duration, investment, contract information,
 * UP/DOWN, then confirmation. The asset and the chart sit above it on the
 * page. The rules it exists to honour:
 *
 *   - pointing at UP or DOWN puts a preview on the chart before anything is
 *     sent, so the trader sees the level and the expiry they are about to
 *     commit to - and the preview holds no trade id, so it can never be
 *     mistaken for a position. With confirmation on, the preview stays on the
 *     chart for as long as the confirmation is open;
 *   - the button disables the moment it is pressed, so a double click cannot
 *     become two trades;
 *   - every submission carries a fresh idempotency key, so a retry after a
 *     network blip cannot either;
 *   - the only price sent is the one on screen, as a slippage guard - the
 *     server fills at its own tick, and a refusal because the price moved is
 *     offered back at the new price rather than reported as a failure;
 *   - nothing is shown as accepted until the server answers with the entry
 *     price it actually used.
 */
interface Props {
  asset: Asset | null;
  mode: TradeMode;
  /** Whether the positions panel below is expanded. */
  tradesOpen: boolean;
  onToggleTrades: () => void;
  /** Open positions on this account, for the count on the Trades button. */
  openCount: number;
}

const DISCONNECTED = 'Live prices disconnected — reconnecting…';

export function TradeTicket({ asset, mode, tradesOpen, onToggleTrades, openCount }: Props) {
  const queryClient = useQueryClient();
  const wallet = useTradingWallet();
  const account = useAccountStore((s) => s.active);
  const upsertTrade = useTradeStore((s) => s.upsertTrade);
  const resetDemo = useResetDemo();
  const connection = useConnectionState();

  const showIntent = useOrderPreviewStore((s) => s.showIntent);
  const clearIntent = useOrderPreviewStore((s) => s.clearIntent);
  const commitPreview = useOrderPreviewStore((s) => s.commit);
  const rejectPreview = useOrderPreviewStore((s) => s.reject);
  const resolvePreview = useOrderPreviewStore((s) => s.resolve);
  const clearPreview = useOrderPreviewStore((s) => s.clear);
  const previewEmpty = useOrderPreviewStore((s) => s.preview === null);

  const confirmBeforeTrade = useSettingsStore((s) => s.confirmBeforeTrade);
  const setDefaultStake = useSettingsStore((s) => s.setDefaultStake);

  const text = useTicketStore((s) => s.amountText);
  const setText = useTicketStore((s) => s.setAmountText);
  const expirySeconds = useTicketStore((s) => s.expirySeconds);
  const setExpiry = useTicketStore((s) => s.setExpiry);
  const focusRequest = useTicketStore((s) => s.focusRequest);

  const [pending, setPending] = useState<Direction | null>(null);
  /** The direction being confirmed; the confirmation is open while set. */
  const [confirming, setConfirming] = useState<Direction | null>(null);
  const confirmingRef = useRef<Direction | null>(null);
  confirmingRef.current = confirming;
  const [dontAskAgain, setDontAskAgain] = useState(false);
  const [sheetError, setSheetError] = useState<string | null>(null);
  /** Set when the server refused the fill because the price moved. */
  const [reQuote, setReQuote] = useState<{ direction: Direction; serverPrice: number } | null>(null);
  const cooldownRef = useRef<number | null>(null);
  const rootRef = useRef<HTMLDivElement | null>(null);
  const amountRef = useRef<HTMLInputElement | null>(null);
  /** The "More" duration chip, which the durations popover opens beside. */
  const moreRef = useRef<HTMLButtonElement | null>(null);
  const [durationsOpen, setDurationsOpen] = useState(false);
  const [guideOpen, setGuideOpen] = useState(false);

  // The price this ticket is showing, which is what the guard compares against.
  const livePrice =
    useMarketStore((s) => (asset ? s.ticks[asset.id]?.price : undefined)) ?? asset?.feed.lastPrice ?? null;

  const chips = useMemo(() => durationChips(asset), [asset]);
  const row = useMemo(() => durationRow(chips, expirySeconds), [chips, expirySeconds]);

  // A duration the asset does not offer is replaced, without touching the
  // trader's saved default - another asset may still offer it.
  useEffect(() => {
    if (chips.length === 0) return;
    const usable = pickDuration(chips, expirySeconds);
    if (usable !== null && usable !== expirySeconds) useTicketStore.setState({ expirySeconds: usable });
  }, [chips, expirySeconds]);

  // The durations belong to the asset they were listed for.
  useEffect(() => setDurationsOpen(false), [asset?.id]);

  useEffect(() => setReQuote(null), [asset?.id, expirySeconds, text]);

  useEffect(
    () => () => {
      if (cooldownRef.current) window.clearTimeout(cooldownRef.current);
    },
    [],
  );

  // A preview belongs to the instrument it was drawn on. Leaving the ticket,
  // or switching instruments, must take it off the chart with it - and a
  // confirmation for the old instrument closes with it.
  useEffect(() => () => clearPreview(), [clearPreview]);
  useEffect(() => {
    clearIntent();
    setConfirming(null);
  }, [asset?.id, account, clearIntent]);

  // "Trade Again" from a result: bring the ticket into view with the amount
  // ready to change.
  useEffect(() => {
    if (focusRequest === 0) return;
    rootRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    amountRef.current?.focus({ preventScroll: true });
    amountRef.current?.select();
  }, [focusRequest]);

  const payout = useMemo(
    () => asset?.payouts.find((p) => p.expirySeconds === expirySeconds) ?? null,
    [asset, expirySeconds],
  );
  const limits = stakeLimits(asset, payout);
  const available = wallet ? wallet.availableBalance : null;
  const currency = wallet?.currency ?? 'INR';
  const amount = amountState(text, limits, available);
  const amountError = asset ? amountMessage(amount, currency) : null;
  const stake = amount.kind === 'valid' ? amount.value : null;
  const presets = amountPresets(limits, available);

  const feedBlocked = !asset || asset.feed.status === 'OFFLINE' || asset.feed.status === 'STALE';

  // What stops a trade that is not about the amount. The amount has its own
  // message under its own field.
  const blockedReason = !asset
    ? 'Choose an asset'
    : !asset.marketOpen
      ? 'Market closed'
      : connection !== 'open'
        ? DISCONNECTED
        : feedBlocked
          ? 'Price feed ' + asset.feed.status.toLowerCase() + ' - trading paused'
          : !payout
            ? 'This duration is not available right now'
            : null;
  const cannotTrade = !!blockedReason || stake === null;

  /**
   * What the chart needs to draw this ticket as an order.
   *
   * Built from what is on screen right now, so the preview and the request
   * that follows it describe the same order - there is no second source for
   * the stake or the expiry to drift away from.
   */
  const draft = useMemo<PreviewDraft | null>(() => {
    if (!asset || !payout || stake === null) return null;
    return {
      assetId: asset.id,
      direction: 'UP',
      stake,
      currency,
      expirySeconds,
      payoutPercent: payout.payoutPercent,
      potentialReturn: stake + Math.round((stake * payout.payoutPercent) / 100),
    };
  }, [asset, payout, stake, currency, expirySeconds]);

  // While the confirmation is open the chart shows the order it describes.
  // Re-shown once a refused attempt's cancelled marker has cleared, so the
  // trader deciding on a re-quote still sees where it would sit.
  useEffect(() => {
    if (!confirming || pending || !draft) return;
    showIntent({ ...draft, direction: confirming });
  }, [confirming, pending, draft, previewEmpty, showIntent]);

  /**
   * Pointing at a direction button, with nothing sent yet.
   *
   * Mouse only. A touch screen synthesises an enter when a finger lands but
   * never a matching leave, so a tap that does not become an order - a scroll
   * that began on the button, say - would strand a preview on the chart with
   * nothing left to take it off again.
   */
  function previewIntent(direction: Direction, pointerType: string) {
    if (pointerType !== 'mouse') return;
    if (!draft || cannotTrade || pending || confirming) return;
    showIntent({ ...draft, direction });
  }

  /**
   * The same preview, for the keyboard.
   *
   * Only on a focus the user navigated to. Clicking a button focuses it too,
   * and on touch that focus is the one event that does arrive - so reacting to
   * every focus would put the leak back.
   */
  function previewFocus(direction: Direction, element: HTMLElement) {
    if (!element.matches(':focus-visible')) return;
    if (!draft || cannotTrade || pending || confirming) return;
    showIntent({ ...draft, direction });
  }

  /** Pointer or focus left a button. The confirmation keeps its own preview. */
  function leave() {
    if (confirmingRef.current) return;
    clearIntent();
  }

  const mutation = useMutation({
    mutationFn: ({ direction, quotedPrice }: { direction: Direction; quotedPrice?: number }) =>
      // A new key per attempt: the server collapses retries of THIS attempt,
      // while a deliberate second trade gets its own key.
      api.trades.create(
        {
          assetId: asset!.id,
          direction,
          amount: stake!,
          expirySeconds,
          quotedPrice,
          // Read at the moment of sending, not from the render: an account
          // switch a moment ago must not be undone by a stale closure.
          accountType: useAccountStore.getState().active,
        },
        crypto.randomUUID(),
      ),
    onSuccess: (trade) => {
      setReQuote(null);
      setSheetError(null);
      setConfirming(null);
      // The server's row takes over the drawing; the preview has done its job.
      resolvePreview();
      upsertTrade(trade);
      void queryClient.invalidateQueries({ queryKey: ['wallet'] });
      void queryClient.invalidateQueries({ queryKey: ['trades'] });
      toast(
        'success',
        trade.direction + ' ' + trade.symbol + ' placed',
        'Entry ' +
          formatPrice(trade.entryPrice, asset?.pricePrecision ?? 2) +
          ' - returns ' +
          formatMoney(trade.potentialReturn, trade.currency) +
          ' if it wins',
      );
    },
    onError: (error, variables) => {
      // Nothing was placed. The marker shows that rather than disappearing.
      rejectPreview();
      if (error instanceof ApiRequestError && error.code === 'PRICE_MOVED') {
        const details = error.details as { serverPrice?: number } | undefined;
        // Offer the new price rather than just reporting the refusal - the
        // trader still wants the trade, at a price they can now see.
        if (typeof details?.serverPrice === 'number') {
          setReQuote({ direction: variables.direction, serverPrice: details.serverPrice });
          return;
        }
      }
      const message = error instanceof ApiRequestError ? error.message : 'Could not place the trade';
      // Inside the confirmation the reason belongs next to the button that
      // failed; with one-tap trading there is nowhere else to say it.
      if (confirmingRef.current) setSheetError(message);
      else toast('error', 'Trade rejected', message);
    },
    onSettled: () => {
      // Short cooldown after the round trip - a fast double tap is almost never
      // two intended trades.
      cooldownRef.current = window.setTimeout(() => setPending(null), 350);
    },
  });

  function send(direction: Direction, quotedPrice: number | null) {
    setPending(direction);
    setSheetError(null);
    // Freeze the preview at the price and instant of the press. The chart's
    // marker then sits exactly where the order was asked for, whatever the
    // market does while the request is in flight.
    if (draft) commitPreview({ ...draft, direction }, quotedPrice);
    // Undefined when no tick has arrived, in which case the server simply
    // applies no guard.
    mutation.mutate({ direction, quotedPrice: quotedPrice ?? undefined });
  }

  /** UP or DOWN was pressed. */
  function press(direction: Direction) {
    if (pending || cannotTrade) return;
    setReQuote(null);
    if (confirmBeforeTrade) {
      setSheetError(null);
      setDontAskAgain(false);
      setConfirming(direction);
      return;
    }
    send(direction, livePrice);
  }

  /** "Confirm Trade": the price on screen now, or the re-quote the server offered. */
  function confirm() {
    if (!confirming || pending || cannotTrade) return;
    if (dontAskAgain && useSettingsStore.getState().confirmBeforeTrade) useSettingsStore.getState().toggleConfirm();
    if (reQuote) {
      const { serverPrice } = reQuote;
      setReQuote(null);
      send(confirming, serverPrice);
      return;
    }
    send(confirming, livePrice);
  }

  const edit = useCallback(() => {
    setConfirming(null);
    setReQuote(null);
    setSheetError(null);
    clearIntent();
  }, [clearIntent]);

  /** Re-send at the price the server just quoted back (one-tap trading). */
  function confirmReQuote() {
    if (!reQuote || pending || cannotTrade) return;
    const { direction, serverPrice } = reQuote;
    setReQuote(null);
    send(direction, serverPrice);
  }

  function writeAmount(minor: number) {
    setText(amountText(minor));
    setDefaultStake(minor);
  }

  const parsed = parseAmount(text);

  const summary: ContractSummary = {
    symbol: asset?.symbol ?? null,
    direction: null,
    stake,
    currency,
    expirySeconds: payout ? expirySeconds : null,
    payoutPercent: payout?.payoutPercent ?? null,
    account,
  };

  /*
   * The wallet's symbol, taken from the formatter rather than hard-coded, so
   * an account held in another currency does not end up labelled in rupees.
   */
  const currencySymbol = currencySymbolFor(currency);
  const selectedChip = chips.find((chip) => chip.seconds === expirySeconds);

  return (
    <div ref={rootRef} className="flex flex-col gap-3 p-3">
      {connection !== 'open' ? (
        <p
          role="status"
          className="flex items-center gap-2 rounded-lg border border-warn/40 bg-warn/10 px-3 py-2 text-[12px] font-medium text-warn"
        >
          <WifiOff className="h-4 w-4 shrink-0" />
          {DISCONNECTED}
        </p>
      ) : null}

      {/* Duration (UX-07). The duration is the value; the clock time it
          expires at is in the contract information below. Four chips at
          most: the rest, and the same choice written as end times, are behind
          "More" (reference record 10). */}
      <div role="group" aria-labelledby="ticket-duration-label">
        <div className="mb-1.5 flex items-center justify-between gap-2">
          <span
            id="ticket-duration-label"
            className="text-[11px] font-semibold uppercase tracking-wider text-content-400"
          >
            Duration
          </span>
          {/* The in-context guide (reference record 19, Deriv's "How to trade
              Rise/Fall?"): on the ticket's first line, where a first-time
              trader starts reading, and on the heading's own line so it costs
              no height. */}
          <button
            type="button"
            onClick={() => setGuideOpen(true)}
            aria-haspopup="dialog"
            aria-expanded={guideOpen}
            className="-my-1 inline-flex shrink-0 items-center gap-1 py-1 text-[11px] font-semibold text-accent hover:underline"
          >
            <CircleHelp className="h-3.5 w-3.5" aria-hidden />
            How a trade works
          </button>
        </div>
        {chips.length === 0 ? (
          <p className="text-xs text-content-400">No durations available for this asset.</p>
        ) : (
          <div
            className={clsx(
              'grid gap-1.5',
              GRID_COLUMNS[Math.max(3, row.chips.length + (row.hidden > 0 ? 1 : 0))],
            )}
          >
            {row.chips.map((chip) => (
              <DurationButton
                key={chip.seconds}
                chip={chip}
                selected={chip.seconds === expirySeconds}
                onSelect={() => setExpiry(chip.seconds)}
              />
            ))}
            {row.hidden > 0 ? (
              <button
                ref={moreRef}
                type="button"
                onClick={() => setDurationsOpen((open) => !open)}
                aria-haspopup="dialog"
                aria-expanded={durationsOpen}
                aria-label={'More durations, ' + chips.length + ' in all'}
                className={clsx(
                  'flex flex-col items-center rounded-lg border px-1 py-1.5 transition',
                  durationsOpen
                    ? 'border-accent bg-accent/10 text-content-100'
                    : 'border-dashed border-surface-600 bg-surface-850 text-content-200 hover:border-surface-500',
                )}
              >
                <span className="text-sm font-bold leading-tight">More</span>
                <span className="text-[10px] font-semibold tnum leading-tight text-content-400">+{row.hidden}</span>
              </button>
            ) : null}
          </div>
        )}
        {selectedChip?.unavailableReason ? (
          <p className="mt-1 text-[11px] text-down">{selectedChip.unavailableReason}</p>
        ) : chips.some((chip) => chip.unavailableReason) ? (
          <p className="mt-1 text-[11px] text-content-400">Greyed durations have no payout on this asset.</p>
        ) : null}
      </div>

      {/* Investment (UX-08). */}
      <div>
        <div className="mb-1.5 flex items-baseline justify-between gap-2">
          <label
            htmlFor="ticket-amount"
            className="text-[11px] font-semibold uppercase tracking-wider text-content-400"
          >
            Investment
          </label>
          <span className="flex min-w-0 items-center gap-1 truncate text-[11px] text-content-400">
            <DemoTag account={account} />
            Available{' '}
            <span className="font-semibold tnum text-content-200">
              {available !== null ? formatMoney(available, currency) : '--'}
            </span>
          </span>
        </div>

        <div
          className={clsx(
            'flex items-center gap-1 rounded-xl border bg-surface-850 px-1 py-1',
            amountError ? 'border-down/60' : 'border-surface-700 focus-within:border-accent',
          )}
        >
          <StepButton
            label="Decrease investment"
            disabled={parsed !== null && parsed <= limits.min}
            onClick={() => writeAmount(stepAmount(parsed, -amountStep(parsed === null ? null : parsed - 1), limits))}
          >
            <Minus className="h-4 w-4" />
          </StepButton>
          {/* The symbol is its own element rather than part of the value: a
              currency prefix inside the field would be editable, and deleting
              it - or typing in front of it - would fight the caret on every
              keystroke. */}
          <label className="flex min-w-0 flex-1 items-baseline justify-center gap-0.5 text-lg font-bold tnum text-content-100">
            <span aria-hidden>{currencySymbol}</span>
            <input
              id="ticket-amount"
              ref={amountRef}
              inputMode="decimal"
              autoComplete="off"
              aria-label="Investment amount"
              aria-invalid={!!amountError}
              aria-describedby={amountError ? 'ticket-amount-message' : undefined}
              placeholder="0"
              className="w-full min-w-0 max-w-[9ch] bg-transparent text-left outline-none placeholder:text-content-400"
              value={text}
              onChange={(event) => {
                const next = event.target.value;
                setText(next);
                const value = parseAmount(next);
                if (value !== null && value > 0) setDefaultStake(value);
              }}
            />
          </label>
          <StepButton
            label="Increase investment"
            disabled={parsed !== null && parsed >= limits.max}
            onClick={() => writeAmount(stepAmount(parsed, amountStep(parsed), limits))}
          >
            <Plus className="h-4 w-4" />
          </StepButton>
        </div>

        {presets.length > 0 ? (
          <div className="mt-1.5 flex flex-wrap gap-1">
            {presets.map((preset) => (
              <button
                key={(preset.isMax ? 'max:' : '') + preset.value}
                type="button"
                onClick={() => writeAmount(preset.value)}
                aria-pressed={stake === preset.value}
                title={preset.isMax ? 'The most you can invest now: ' + formatMoney(preset.value, currency) : undefined}
                className={clsx(
                  'rounded-md px-2 py-1 text-[11px] font-semibold tnum transition',
                  stake === preset.value
                    ? 'bg-accent/20 text-content-100 ring-1 ring-accent/60'
                    : 'bg-surface-850 text-content-300 hover:bg-surface-800 hover:text-content-100',
                )}
              >
                {preset.isMax ? 'Max' : formatMoneyCompact(preset.value, currency)}
              </button>
            ))}
          </div>
        ) : null}

        {amountError ? (
          <div id="ticket-amount-message" className="mt-1.5 text-[11px] font-medium text-down" role="alert">
            {amountError}
            {amount.kind === 'insufficient' ? (
              account === 'LIVE' ? (
                <button
                  type="button"
                  onClick={() => openFunds('DEPOSIT')}
                  className="ml-1.5 font-bold text-accent underline-offset-2 hover:underline"
                >
                  Deposit
                </button>
              ) : (
                <button
                  type="button"
                  onClick={() => resetDemo.mutate()}
                  disabled={resetDemo.isPending}
                  className="ml-1.5 font-bold text-accent underline-offset-2 hover:underline disabled:opacity-50"
                >
                  Reset demo balance
                </button>
              )
            ) : null}
          </div>
        ) : (
          <p className="mt-1.5 text-[11px] text-content-400">
            {formatMoneyCompact(limits.min, currency)} to {formatMoneyCompact(limits.max, currency)}
          </p>
        )}
      </div>

      <ContractInfo summary={summary} />

      {blockedReason && blockedReason !== DISCONNECTED ? (
        <p className="rounded-lg border border-down/30 bg-down/10 px-3 py-2 text-[11px] font-medium text-down">
          {blockedReason}
        </p>
      ) : null}

      {/* The price moved while the order was in flight. The trade was refused,
          not placed - so this asks again at the price the server just quoted,
          rather than reporting a failure the trader can do nothing with. With
          confirmation on, the same offer is made inside the confirmation. */}
      {reQuote && !confirming && !blockedReason ? (
        <div className="rounded-lg border border-warn/40 bg-warn/10 px-3 py-2.5" role="alert">
          <p className="text-[11px] font-semibold text-warn">Price moved before your order landed</p>
          <p className="mt-1 text-[11px] text-content-300">
            Nothing was placed. {reQuote.direction} is now at{' '}
            <span className="tnum font-bold text-content-100">
              {formatPrice(reQuote.serverPrice, asset?.pricePrecision ?? 2)}
            </span>
            .
          </p>
          <div className="mt-2 flex gap-1.5">
            <button
              type="button"
              onClick={confirmReQuote}
              disabled={pending !== null}
              className={clsx(
                'flex-1 rounded-md px-2 py-1.5 text-[11px] font-bold text-white transition disabled:opacity-40',
                reQuote.direction === 'UP' ? 'bg-up-solid' : 'bg-down-solid',
              )}
            >
              Place {reQuote.direction} at new price
            </button>
            <button
              type="button"
              onClick={() => setReQuote(null)}
              className="rounded-md px-2 py-1.5 text-[11px] font-semibold text-content-400 transition hover:text-content-200"
            >
              Cancel
            </button>
          </div>
        </div>
      ) : null}

      {/* Majority opinion is market information, not advice: advanced only
          (product decision 2), so a beginner is not nudged to follow a crowd. */}
      {mode === 'advanced' ? <MajorityOpinion sentiment={asset?.sentiment} /> : null}

      <div className="grid grid-cols-2 gap-2">
        <DirectionButton
          direction="UP"
          blocked={cannotTrade}
          pending={pending === 'UP'}
          busy={pending !== null}
          onPlace={() => press('UP')}
          onPreview={(type) => previewIntent('UP', type)}
          onPreviewFocus={(element) => previewFocus('UP', element)}
          onLeave={leave}
        />
        <DirectionButton
          direction="DOWN"
          blocked={cannotTrade}
          pending={pending === 'DOWN'}
          busy={pending !== null}
          onPlace={() => press('DOWN')}
          onPreview={(type) => previewIntent('DOWN', type)}
          onPreviewFocus={(element) => previewFocus('DOWN', element)}
          onLeave={leave}
        />
      </div>
      <p className="-mt-1 text-center text-[11px] text-content-400">
        {confirmBeforeTrade
          ? 'You review the trade before it is placed.'
          : 'One-tap trading: pressing UP or DOWN places the trade.'}
      </p>

      {/* Opens the positions panel below. Carries the count, so an open trade
          is still visible while the panel is shut. */}
      <button
        type="button"
        onClick={onToggleTrades}
        aria-expanded={tradesOpen}
        className={clsx(
          'flex h-10 w-full items-center justify-center gap-2 rounded-xl text-sm font-bold transition',
          tradesOpen ? 'bg-surface-700 text-content-100' : 'bg-surface-800 text-content-200 hover:bg-surface-700',
        )}
      >
        <History className="h-4 w-4" />
        Trades
        {openCount > 0 ? (
          <span className="ml-1 rounded-full bg-brand px-1.5 py-0.5 text-[10px] font-black tnum text-surface-950">
            {openCount}
          </span>
        ) : null}
      </button>

      <ConfirmTradeSheet
        direction={confirming}
        summary={summary}
        pending={pending !== null}
        blockedReason={blockedReason ?? (stake === null ? amountError : null)}
        reQuotePrice={reQuote && confirming ? reQuote.serverPrice : null}
        pricePrecision={asset?.pricePrecision ?? 2}
        error={sheetError}
        dontAskAgain={dontAskAgain}
        onDontAskAgainChange={setDontAskAgain}
        onConfirm={confirm}
        onEdit={edit}
      />

      <DurationSheet
        open={durationsOpen}
        onClose={() => setDurationsOpen(false)}
        chips={chips}
        selected={expirySeconds}
        onSelect={setExpiry}
        anchorRef={moreRef}
        besideRef={rootRef}
      />

      <HowItWorksSheet
        open={guideOpen}
        onClose={() => setGuideOpen(false)}
        example={{ stake, payoutPercent: payout?.payoutPercent ?? null, currency }}
      />
    </div>
  );
}

/** Tailwind needs whole class names to find them. */
const GRID_COLUMNS: Record<number, string> = { 3: 'grid-cols-3', 4: 'grid-cols-4', 5: 'grid-cols-5' };

/** One duration and what it pays. */
function DurationButton({
  chip,
  selected,
  onSelect,
}: {
  chip: DurationChip;
  selected: boolean;
  onSelect: () => void;
}) {
  const unavailable = chip.unavailableReason !== null;
  return (
    <button
      type="button"
      onClick={onSelect}
      disabled={unavailable}
      aria-pressed={selected}
      title={chip.unavailableReason ?? chip.payoutPercent + '% payout'}
      className={clsx(
        'flex min-w-0 flex-col items-center rounded-lg border px-1 py-1.5 transition',
        selected
          ? 'border-accent bg-accent/15 text-content-100'
          : 'border-surface-700 bg-surface-850 text-content-200 hover:border-surface-600',
        'disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:border-surface-700',
      )}
    >
      <span className="text-sm font-bold tnum leading-tight">{chip.label}</span>
      <span
        className={clsx(
          'text-[10px] font-semibold tnum leading-tight',
          unavailable ? 'text-content-400' : selected ? 'text-accent' : 'text-up',
        )}
      >
        {chip.payoutPercent !== null ? chip.payoutPercent + '%' : 'N/A'}
      </span>
    </button>
  );
}

function StepButton({
  children,
  onClick,
  disabled,
  label,
}: {
  children: React.ReactNode;
  onClick: () => void;
  disabled?: boolean;
  label: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      aria-label={label}
      className={clsx(
        // Unboxed: inside a field of its own, a bordered button competes with
        // the value it is there to nudge.
        'grid h-9 w-9 shrink-0 place-items-center rounded-lg text-content-400 transition',
        'hover:bg-surface-800 hover:text-content-100 disabled:cursor-not-allowed disabled:opacity-30',
        'disabled:hover:bg-transparent',
      )}
    >
      {children}
    </button>
  );
}

/**
 * How the open book on this instrument is split.
 *
 * Only the ratio, never the amounts: the size of the book is commercially
 * sensitive. With nothing open there is no crowd to report, and it says so
 * rather than showing an invented even split.
 */
function MajorityOpinion({ sentiment }: { sentiment: AssetSentiment | null | undefined }) {
  const hasData = !!sentiment && sentiment.upPercent !== null && sentiment.downPercent !== null;
  const up = hasData ? sentiment.upPercent! : 50;
  const down = hasData ? sentiment.downPercent! : 50;

  return (
    <div className="px-1">
      <div className="text-[12px] text-content-400">Majority opinion</div>

      <div className="mt-1.5 flex h-1.5 overflow-hidden rounded-full bg-surface-800">
        <div
          className={clsx('transition-[width] duration-500', hasData ? 'bg-up' : 'bg-surface-700')}
          style={{ width: up + '%' }}
        />
        <div
          className={clsx('transition-[width] duration-500', hasData ? 'bg-down' : 'bg-surface-700')}
          style={{ width: down + '%' }}
        />
      </div>

      <div className="mt-1 flex items-baseline justify-between text-[12px] font-semibold tnum">
        <span className={hasData ? 'text-up' : 'text-content-400'}>{hasData ? 'UP ' + up + '%' : '--'}</span>
        <span className={hasData ? 'text-down' : 'text-content-400'}>{hasData ? 'DOWN ' + down + '%' : '--'}</span>
      </div>
    </div>
  );
}

/**
 * One direction to trade.
 *
 * `busy` is the other button's request as well as this one's: once anything is
 * in flight both lock, so a fast second click cannot become a second trade.
 * `pending` is only this button's, and is the one that shows the spinner.
 *
 * The word is on the button, not only in its accessible name: a first-time
 * trader must know what pressing it does without decoding an arrow, and the
 * brief's terminology is UP and DOWN - never "Buy".
 */
function DirectionButton({
  direction,
  blocked,
  pending,
  busy,
  onPlace,
  onPreview,
  onPreviewFocus,
  onLeave,
}: {
  direction: Direction;
  blocked: boolean;
  pending: boolean;
  busy: boolean;
  onPlace: () => void;
  onPreview: (pointerType: string) => void;
  onPreviewFocus: (element: HTMLElement) => void;
  onLeave: () => void;
}) {
  const up = direction === 'UP';
  const Arrow = up ? ArrowUp : ArrowDown;

  return (
    <button
      type="button"
      disabled={blocked || busy}
      onClick={onPlace}
      onPointerEnter={(event) => onPreview(event.pointerType)}
      onPointerLeave={onLeave}
      onFocus={(event) => onPreviewFocus(event.currentTarget)}
      onBlur={onLeave}
      aria-label={up ? 'Place UP trade' : 'Place DOWN trade'}
      className={clsx(
        'flex h-[52px] items-center justify-center gap-1.5 rounded-xl text-base font-black tracking-wide text-white transition',
        'hover:brightness-110 active:scale-[0.98] disabled:cursor-not-allowed disabled:opacity-40 disabled:shadow-none',
        up ? 'bg-up-solid shadow-up-glow' : 'bg-down-solid shadow-down-glow',
      )}
    >
      {pending ? <Loader2 className="h-5 w-5 animate-spin" /> : <Arrow className="h-5 w-5" strokeWidth={3} />}
      {direction}
    </button>
  );
}
