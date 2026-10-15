import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ActivityIndicator, Keyboard, Pressable, ScrollView, View } from 'react-native';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { randomUUID } from 'expo-crypto';
import * as Haptics from 'expo-haptics';
import Animated, { useAnimatedStyle, useSharedValue, withTiming } from 'react-native-reanimated';
import { ArrowDown, ArrowUp, CircleHelp, History, WifiOff } from 'lucide-react-native';
import { api } from '../../services/api';
import { ApiRequestError } from '../../services/http';
import { useSettingsStore, type TradeMode } from '../../stores/settingsStore';
import { useTradeStore } from '../../stores/tradeStore';
import { useMarketStore } from '../../stores/marketStore';
import { useAccountStore } from '../../stores/accountStore';
import { useOrderPreviewStore, type PreviewDraft } from '../../stores/orderPreviewStore';
import { currencySymbolFor, formatMoney, formatMoneyCompact, formatPrice } from '../../utils/format';
import { cn } from '../../utils/cn';
import { toast } from '../../hooks/useToast';
import { useNow } from '../../hooks/useCountdown';
import { Icon, Text } from '../../components/ui';
import { ActiveTradesBadge } from '../../components/BottomNav';
import { openFunds, useResetDemo } from '../account/funds';
import { ContractInfo, type ContractSummary } from './ContractInfo';
import { ConfirmTradeSheet } from './ConfirmTradeSheet';
import { AmountSheet } from './AmountSheet';
import { TimeSheet } from './TimeSheet';
import { HowItWorksSheet } from './HowItWorks';
import { DemoTag, useClockFormatter } from './tradeBits';
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
  parseAmount,
  pickDuration,
  stakeLimits,
  stepAmount,
} from './ticketModel';
import type { Asset, AssetSentiment, Direction } from '../../types/api';

/**
 * The trade ticket.
 *
 * The brief's fixed order (UX-04): duration, investment, contract information,
 * UP/DOWN, then confirmation. The asset and the chart sit above it on the
 * screen. The rules it exists to honour:
 *
 *   - holding UP or DOWN puts a preview on the chart before anything is sent,
 *     so the trader sees the level and the expiry they are about to commit to
 *     - and the preview holds no trade id, so it can never be mistaken for a
 *     position. With confirmation on, the preview stays on the chart for as
 *     long as the confirmation is open;
 *   - the button disables the moment it is pressed, so a double tap cannot
 *     become two trades;
 *   - every submission carries a fresh idempotency key, so a retry after a
 *     network blip cannot either;
 *   - the only price sent is the one on screen, as a slippage guard - the
 *     server fills at its own tick, and a refusal because the price moved is
 *     offered back at the new price rather than reported as a failure;
 *   - nothing is shown as accepted until the server answers with the entry
 *     price it actually used.
 *
 * On a phone the ticket is docked under the chart, which keeps its own pan
 * and pinch, so nothing here may scroll the chart away. Amount and time are
 * one compact row, the way Binomo's mobile app lays them out - the Amount box,
 * the trade history button, the Time box - and each box opens a sheet to
 * change it. The set-up part - that row, the contract information - scrolls
 * inside the ticket when the screen is short, while what stops a trade and
 * the UP and DOWN buttons stay pinned at the bottom, always in reach of the
 * thumb.
 */
interface Props {
  asset: Asset | null;
  mode: TradeMode;
  /** Whether the positions sheet (trade history) is open. */
  tradesOpen: boolean;
  onToggleTrades: () => void;
  /** Open positions on this account, for the count on the trade history button. */
  openCount: number;
}

const DISCONNECTED = 'Live prices disconnected — reconnecting…';

/** Haptics are a courtesy: a device without them must never break a trade. */
function feelPlaced() {
  void Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Light).catch(() => undefined);
}

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
  const cooldownRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  /**
   * True from the moment an order is sent until its cooldown ends.
   *
   * `pending` alone is not enough: it is state, so it only changes on the next
   * render. When the phone is busy, several taps can queue up and all run
   * before that render, each still reading `pending` as null - and each one
   * would place a trade. A ref changes at once.
   */
  const sendingRef = useRef(false);
  const scrollRef = useRef<ScrollView | null>(null);
  const [amountOpen, setAmountOpen] = useState(false);
  const [timeOpen, setTimeOpen] = useState(false);
  const [guideOpen, setGuideOpen] = useState(false);

  // The price this ticket is showing, which is what the guard compares against.
  const livePrice =
    useMarketStore((s) => (asset ? s.ticks[asset.id]?.price : undefined)) ?? asset?.feed.lastPrice ?? null;

  const chips = useMemo(() => durationChips(asset), [asset]);

  // A duration the asset does not offer is replaced, without touching the
  // trader's saved default - another asset may still offer it.
  useEffect(() => {
    if (chips.length === 0) return;
    const usable = pickDuration(chips, expirySeconds);
    if (usable !== null && usable !== expirySeconds) useTicketStore.setState({ expirySeconds: usable });
  }, [chips, expirySeconds]);

  // The durations belong to the asset they were listed for.
  useEffect(() => setTimeOpen(false), [asset?.id]);

  useEffect(() => setReQuote(null), [asset?.id, expirySeconds, text]);

  useEffect(
    () => () => {
      if (cooldownRef.current) clearTimeout(cooldownRef.current);
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

  // "Trade Again" from a result: bring the amount into view, ready to change -
  // the Amount sheet, its field focused with the amount selected.
  useEffect(() => {
    if (focusRequest === 0) return;
    scrollRef.current?.scrollTo({ y: 0, animated: true });
    setAmountOpen(true);
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
   * A finger is down on a direction button, with nothing sent yet.
   *
   * The phone's form of the web's hover: the preview shows while the button
   * is held, so a trader can look before letting go - and a press that slides
   * off the button without becoming a tap takes it away again (`leave`).
   */
  function previewIntent(direction: Direction) {
    if (!draft || cannotTrade || pending || confirming) return;
    showIntent({ ...draft, direction });
  }

  /**
   * The finger lifted, or slid off. The confirmation keeps its own preview.
   *
   * A tap's press lands after its release, so the check waits for it: a tap
   * that opened the confirmation keeps the preview it was showing, and a tap
   * that sent the order has already frozen it (an intent clear never touches
   * an order in flight). Anything else - a press that slid away - clears it.
   */
  function leave() {
    setTimeout(() => {
      if (confirmingRef.current) return;
      clearIntent();
    }, 0);
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
        randomUUID(),
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
      cooldownRef.current = setTimeout(() => {
        sendingRef.current = false;
        setPending(null);
      }, 350);
    },
  });

  function send(direction: Direction, quotedPrice: number | null) {
    if (sendingRef.current) return;
    sendingRef.current = true;
    setPending(direction);
    setSheetError(null);
    // Freeze the preview at the price and instant of the press. The chart's
    // marker then sits exactly where the order was asked for, whatever the
    // market does while the request is in flight.
    if (draft) commitPreview({ ...draft, direction }, quotedPrice);
    feelPlaced();
    // Undefined when no tick has arrived, in which case the server simply
    // applies no guard.
    mutation.mutate({ direction, quotedPrice: quotedPrice ?? undefined });
  }

  /** UP or DOWN was pressed. */
  function press(direction: Direction) {
    if (pending || sendingRef.current || cannotTrade) return;
    Keyboard.dismiss();
    setReQuote(null);
    if (confirmBeforeTrade) {
      setSheetError(null);
      setDontAskAgain(false);
      // Ahead of the render, so the release that came just before this press
      // (see `leave`) already sees the confirmation as open.
      confirmingRef.current = direction;
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
  // The Amount box shows what the sheet holds: the amount when it reads as
  // one, otherwise exactly what was typed, with the box marked as wrong.
  const amountShown = parsed !== null ? formatMoneyCompact(parsed, currency) : currencySymbol + (text.trim() || '0');

  /** What is wrong with the amount, and the one thing that fixes a short balance. */
  const amountProblem = amountError ? (
    <View accessibilityRole="alert" className="flex-row flex-wrap items-center gap-x-1.5">
      <Text className="text-[11px] font-medium text-down">{amountError}</Text>
      {amount.kind === 'insufficient' ? (
        account === 'LIVE' ? (
          <Pressable onPress={() => openFunds('DEPOSIT')} hitSlop={6} accessibilityRole="button">
            <Text className="text-[11px] font-bold text-accent">Deposit</Text>
          </Pressable>
        ) : (
          <Pressable
            onPress={() => resetDemo.mutate()}
            disabled={resetDemo.isPending}
            hitSlop={6}
            accessibilityRole="button"
            accessibilityState={{ disabled: resetDemo.isPending }}
            className={resetDemo.isPending ? 'opacity-50' : undefined}
          >
            <Text className="text-[11px] font-bold text-accent">Reset demo balance</Text>
          </Pressable>
        )
      ) : null}
    </View>
  ) : null;

  return (
    <View className="shrink border-t border-surface-800 bg-surface-900">
      {connection !== 'open' ? (
        <View
          accessibilityLiveRegion="polite"
          className="mx-3 mt-2 flex-row items-center gap-2 rounded-lg border border-warn/40 bg-warn/10 px-3 py-2"
        >
          <Icon as={WifiOff} size={16} className="text-warn" />
          <Text className="shrink text-[12px] font-medium text-warn">{DISCONNECTED}</Text>
        </View>
      ) : null}

      <ScrollView
        ref={scrollRef}
        className="shrink grow-0"
        contentContainerClassName="gap-3 px-3 pb-1 pt-2.5"
        keyboardShouldPersistTaps="handled"
        keyboardDismissMode="on-drag"
        showsVerticalScrollIndicator={false}
      >
        {/* The guide and the balance the amount is checked against, on one
            small line over the controls they explain. The guide is the
            in-context help (reference record 19, Deriv's "How to trade
            Rise/Fall?"), where a first-time trader starts reading. */}
        <View className="flex-row items-center justify-between gap-2">
          <Pressable
            onPress={() => setGuideOpen(true)}
            hitSlop={8}
            accessibilityRole="button"
            accessibilityState={{ expanded: guideOpen }}
            className="-my-1 flex-row items-center gap-1 py-1"
          >
            <Icon as={CircleHelp} size={14} className="text-accent" />
            <Text className="text-[11px] font-semibold text-accent">How a trade works</Text>
          </Pressable>
          <View className="min-w-0 shrink flex-row items-center gap-1">
            <DemoTag account={account} />
            <Text numberOfLines={1} className="shrink text-[11px] text-content-400">
              Available{' '}
              <Text className="text-[11px] font-semibold tnum text-content-200">
                {available !== null ? formatMoney(available, currency) : '--'}
              </Text>
            </Text>
          </View>
        </View>

        {/* Investment (UX-08) and duration (UX-07) as one row, the way Binomo
            lays them out: the Amount box, trade history, the Time box. Each
            box opens a sheet to change it. */}
        <View>
          <View className="h-14 flex-row gap-2">
            <Pressable
              onPress={() => setAmountOpen(true)}
              accessibilityRole="button"
              accessibilityLabel={'Amount, ' + amountShown}
              accessibilityHint={amountError ? amountError + '. Opens the amount' : 'Opens the amount'}
              className={cn(
                'min-w-0 flex-1 justify-center rounded-xl border bg-surface-850 px-3 active:bg-surface-800',
                amountError ? 'border-down/60' : 'border-surface-700',
              )}
            >
              <Text className="text-[11px] text-content-400">Amount</Text>
              <Text numberOfLines={1} className="text-lg font-bold leading-6 tnum text-content-100">
                {amountShown}
              </Text>
            </Pressable>

            {/* Trade history: the positions sheet, with its Active and History
                tabs. Carries the open count, so a running trade is still
                visible while the sheet is shut. */}
            <Pressable
              onPress={onToggleTrades}
              accessibilityRole="button"
              accessibilityState={{ expanded: tradesOpen }}
              accessibilityLabel={'Trade history' + (openCount > 0 ? ', ' + openCount + ' active' : '')}
              className={cn(
                'relative h-14 w-14 items-center justify-center rounded-xl border',
                tradesOpen ? 'border-accent/50 bg-surface-700' : 'border-surface-700 bg-surface-800 active:bg-surface-700',
              )}
            >
              <Icon as={History} size={22} className={tradesOpen ? 'text-content-100' : 'text-content-200'} />
              <ActiveTradesBadge count={openCount} className="-right-1 -top-1" />
            </Pressable>

            <TimeBox
              expirySeconds={chips.length > 0 ? expirySeconds : null}
              unavailable={!!selectedChip?.unavailableReason}
              onPress={() => setTimeOpen(true)}
            />
          </View>

          {chips.length === 0 || selectedChip?.unavailableReason || amountProblem ? (
            <View className="mt-1.5 gap-1">
              {chips.length === 0 ? (
                <Text className="text-[11px] text-content-400">No durations available for this asset.</Text>
              ) : selectedChip?.unavailableReason ? (
                <Text className="text-[11px] text-down">{selectedChip.unavailableReason}</Text>
              ) : null}
              {amountProblem}
            </View>
          ) : null}
        </View>

        <ContractInfo summary={summary} />

        {/* Majority opinion is market information, not advice: advanced only
            (product decision 2), so a beginner is not nudged to follow a crowd. */}
        {mode === 'advanced' ? <MajorityOpinion sentiment={asset?.sentiment} /> : null}
      </ScrollView>

      <View className="gap-2 px-3 pb-2 pt-2">
        {/* Pinned with the buttons rather than scrolled with the set-up above:
            a disabled UP and DOWN must always come with the reason. */}
        {blockedReason && blockedReason !== DISCONNECTED ? (
          <View className="rounded-lg border border-down/30 bg-down/10 px-3 py-2">
            <Text className="text-[11px] font-medium text-down">{blockedReason}</Text>
          </View>
        ) : null}

        {/* The price moved while the order was in flight. The trade was refused,
            not placed - so this asks again at the price the server just quoted,
            rather than reporting a failure the trader can do nothing with. With
            confirmation on, the same offer is made inside the confirmation. */}
        {reQuote && !confirming && !blockedReason ? (
          <View accessibilityRole="alert" className="rounded-lg border border-warn/40 bg-warn/10 px-3 py-2.5">
            <Text className="text-[11px] font-semibold text-warn">Price moved before your order landed</Text>
            <Text className="mt-1 text-[11px] text-content-300">
              Nothing was placed. {reQuote.direction} is now at{' '}
              <Text className="text-[11px] font-bold tnum text-content-100">
                {formatPrice(reQuote.serverPrice, asset?.pricePrecision ?? 2)}
              </Text>
              .
            </Text>
            <View className="mt-2 flex-row gap-1.5">
              <Pressable
                onPress={confirmReQuote}
                disabled={pending !== null}
                accessibilityRole="button"
                className={cn(
                  'flex-1 items-center rounded-md px-2 py-2 active:opacity-90',
                  reQuote.direction === 'UP' ? 'bg-up-solid' : 'bg-down-solid',
                  pending !== null && 'opacity-40',
                )}
              >
                <Text className="text-[11px] font-bold text-white">Place {reQuote.direction} at new price</Text>
              </Pressable>
              <Pressable
                onPress={() => setReQuote(null)}
                accessibilityRole="button"
                className="items-center rounded-md px-3 py-2 active:bg-surface-800"
              >
                <Text className="text-[11px] font-semibold text-content-400">Cancel</Text>
              </Pressable>
            </View>
          </View>
        ) : null}

        <View className="flex-row gap-2">
          <DirectionButton
            direction="UP"
            blocked={cannotTrade}
            pending={pending === 'UP'}
            busy={pending !== null}
            onPlace={() => press('UP')}
            onPreview={() => previewIntent('UP')}
            onLeave={leave}
          />
          <DirectionButton
            direction="DOWN"
            blocked={cannotTrade}
            pending={pending === 'DOWN'}
            busy={pending !== null}
            onPlace={() => press('DOWN')}
            onPreview={() => previewIntent('DOWN')}
            onLeave={leave}
          />
        </View>

        <Text className="text-center text-[11px] leading-4 text-content-400">
          {confirmBeforeTrade
            ? 'You review the trade before it is placed.'
            : 'One-tap trading: pressing UP or DOWN places the trade.'}
        </Text>
      </View>

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

      <AmountSheet
        open={amountOpen}
        onClose={() => setAmountOpen(false)}
        text={text}
        onChangeText={(next) => {
          setText(next);
          const value = parseAmount(next);
          if (value !== null && value > 0) setDefaultStake(value);
        }}
        currency={currency}
        currencySymbol={currencySymbol}
        invalid={!!amountError}
        canDecrease={!(parsed !== null && parsed <= limits.min)}
        canIncrease={!(parsed !== null && parsed >= limits.max)}
        onDecrease={() => writeAmount(stepAmount(parsed, -amountStep(parsed === null ? null : parsed - 1), limits))}
        onIncrease={() => writeAmount(stepAmount(parsed, amountStep(parsed), limits))}
        presets={presets}
        stake={stake}
        onPreset={writeAmount}
        account={account}
        available={available}
        message={
          amountProblem ?? (
            <Text className="text-[11px] text-content-400">
              {formatMoneyCompact(limits.min, currency)} to {formatMoneyCompact(limits.max, currency)}
            </Text>
          )
        }
      />

      <TimeSheet
        open={timeOpen}
        onClose={() => setTimeOpen(false)}
        chips={chips}
        selected={expirySeconds}
        onSelect={setExpiry}
      />

      <HowItWorksSheet
        open={guideOpen}
        onClose={() => setGuideOpen(false)}
        example={{ stake, payoutPercent: payout?.payoutPercent ?? null, currency }}
      />
    </View>
  );
}

/**
 * The Time box: the clock time a trade placed now would end at, to the second.
 *
 * This platform's expiry is relative - entry plus the duration - so the time
 * is worked out from the clock every second rather than rounded to a minute
 * mark, in the zone the trader chose for the chart (the expiry guide on the
 * chart reads the same). Its own component, so the one-second tick redraws
 * this box and not the whole ticket.
 */
function TimeBox({
  expirySeconds,
  unavailable,
  onPress,
}: {
  /** Null when the asset offers no duration. */
  expirySeconds: number | null;
  /** The chosen duration has no payout on this asset. */
  unavailable: boolean;
  onPress: () => void;
}) {
  const now = useNow(1000);
  const clock = useClockFormatter();
  const endsAt = expirySeconds !== null ? clock(now + expirySeconds * 1000) : '--';

  return (
    <Pressable
      onPress={onPress}
      accessibilityRole="button"
      accessibilityLabel={'Time, ends at ' + endsAt}
      accessibilityHint="Opens the time"
      className={cn(
        'min-w-0 flex-1 justify-center rounded-xl border bg-surface-850 px-3 active:bg-surface-800',
        unavailable ? 'border-down/60' : 'border-surface-700',
      )}
    >
      <Text className="text-[11px] text-content-400">Time</Text>
      <Text numberOfLines={1} className="text-lg font-bold leading-6 tnum text-content-100">
        {endsAt}
      </Text>
    </Pressable>
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

  const upShare = useSharedValue(up);
  const downShare = useSharedValue(down);
  useEffect(() => {
    upShare.value = withTiming(up, { duration: 500 });
    downShare.value = withTiming(down, { duration: 500 });
  }, [up, down, upShare, downShare]);
  const upStyle = useAnimatedStyle(() => ({ flexGrow: upShare.value }));
  const downStyle = useAnimatedStyle(() => ({ flexGrow: downShare.value }));

  return (
    <View
      accessible
      accessibilityLabel={hasData ? 'Majority opinion: UP ' + up + '%, DOWN ' + down + '%' : 'Majority opinion: no open trades'}
      className="px-1"
    >
      <Text className="text-[12px] text-content-400">Majority opinion</Text>

      <View className="mt-1.5 h-1.5 flex-row overflow-hidden rounded-full bg-surface-800">
        <Animated.View className={hasData ? 'bg-up' : 'bg-surface-700'} style={[{ flexBasis: 0 }, upStyle]} />
        <Animated.View className={hasData ? 'bg-down' : 'bg-surface-700'} style={[{ flexBasis: 0 }, downStyle]} />
      </View>

      <View className="mt-1 flex-row items-baseline justify-between">
        <Text className={cn('text-[12px] font-semibold tnum', hasData ? 'text-up' : 'text-content-400')}>
          {hasData ? 'UP ' + up + '%' : '--'}
        </Text>
        <Text className={cn('text-[12px] font-semibold tnum', hasData ? 'text-down' : 'text-content-400')}>
          {hasData ? 'DOWN ' + down + '%' : '--'}
        </Text>
      </View>
    </View>
  );
}

/**
 * One direction to trade.
 *
 * `busy` is the other button's request as well as this one's: once anything is
 * in flight both lock, so a fast second tap cannot become a second trade.
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
  onLeave,
}: {
  direction: Direction;
  blocked: boolean;
  pending: boolean;
  busy: boolean;
  onPlace: () => void;
  onPreview: () => void;
  onLeave: () => void;
}) {
  const up = direction === 'UP';
  const disabled = blocked || busy;

  return (
    <Pressable
      disabled={disabled}
      onPress={onPlace}
      onPressIn={onPreview}
      onPressOut={onLeave}
      accessibilityRole="button"
      accessibilityLabel={up ? 'Place UP trade' : 'Place DOWN trade'}
      accessibilityState={{ disabled, busy: pending }}
      className={cn(
        'h-[52px] flex-1 flex-row items-center justify-center gap-1.5 rounded-xl active:opacity-90',
        up ? 'bg-up-solid' : 'bg-down-solid',
        disabled && 'opacity-40',
      )}
    >
      {pending ? (
        <ActivityIndicator size="small" color="#fff" />
      ) : (
        <Icon as={up ? ArrowUp : ArrowDown} size={20} strokeWidth={3} color="#fff" />
      )}
      <Text className="text-base font-black tracking-wide text-white">{direction}</Text>
    </Pressable>
  );
}
