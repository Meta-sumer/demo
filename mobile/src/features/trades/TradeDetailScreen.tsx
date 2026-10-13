import type { ReactNode } from 'react';
import { Platform, View } from 'react-native';
import { useQuery } from '@tanstack/react-query';
import { Flag, Timer } from 'lucide-react-native';
import { api } from '../../services/api';
import { useCountdown } from '../../hooks/useCountdown';
import { usePullRefresh } from '../../hooks/usePullRefresh';
import { useMarketStore } from '../../stores/marketStore';
import { useSettingsStore } from '../../stores/settingsStore';
import { formatCountdown, formatDateTime, formatExpiry, formatMoney, formatPrice, formatSignedMoney } from '../../utils/format';
import { resolveZone, zoneChip } from '../../utils/time';
import { durationOf, isActive, statusLabel } from '../../utils/tradeStatus';
import { cn } from '../../utils/cn';
import { Screen, ScreenHeader } from '../../components/Screen';
import { Icon, Panel, Skeleton, Text } from '../../components/ui';
import { payoutOf, settlementOf } from '../trade/settlement';
import { accountOf } from '../trade/ticketModel';
import { DemoTag, DirectionWord, StatusPill, useClockFormatter } from '../trade/tradeBits';
import { TradeWindowChart } from '../trade/chart/TradeWindowChart';

/**
 * Trade detail (brief UX-15): one structure for every trade, whatever state it
 * is in and whichever screen it was opened from.
 *
 * Asset, status, direction, investment, entry price, duration, expiry time and
 * price, payout and return, result, when it was placed, its reference and its
 * account - in that order, so a trader comparing two trades finds each figure
 * in the same place. A running trade shows its countdown where the result will
 * be, and the current price where the expiry price will be.
 *
 * With the figures, the trade drawn against the price it was decided on
 * (Deriv's "Contract details"): on a phone the result first, then the chart,
 * then the figures. The chart's markers wear the same icons as the figures they
 * mark, so each one explains the other.
 *
 * Back returns to wherever the trade was opened from; opened from a link with
 * nothing behind it, it goes to the trade history.
 */
const MONO = Platform.select({ ios: 'Menlo', default: 'monospace' });

export function TradeDetailScreen({ id }: { id: string }) {
  const query = useQuery({
    queryKey: ['trade', id],
    queryFn: () => api.trades.get(id),
    enabled: !!id,
    refetchInterval: (result) => {
      const status = result.state.data?.status;
      return status === 'OPEN' || status === 'SETTLING' ? 2000 : false;
    },
  });

  const trade = query.data;

  const remaining = useCountdown(trade && trade.status === 'OPEN' ? trade.expiryTimestamp : null);
  const tick = useMarketStore((s) => (trade ? s.ticks[trade.assetId] : undefined));
  const clock = useClockFormatter();
  const zone = resolveZone(useSettingsStore((s) => s.timezone));
  // Same cache as the Trade screen, so this is normally already loaded.
  const assetsQuery = useQuery({ queryKey: ['assets'], queryFn: api.assets.list });
  const { refreshing, onRefresh } = usePullRefresh(() => query.refetch());

  const header = (
    <ScreenHeader title="Trade detail" subtitle={trade?.reference} fallbackHref="/trades?tab=history" />
  );

  if (query.isError) {
    return (
      <View className="flex-1 bg-surface-950">
        {header}
        <Screen bottomInset onRefresh={onRefresh} refreshing={refreshing}>
          <Panel className="px-5 py-8">
            <Text className="text-center text-sm text-content-300">This trade could not be loaded.</Text>
          </Panel>
        </Screen>
      </View>
    );
  }

  if (query.isLoading || !trade) {
    return (
      <View className="flex-1 bg-surface-950">
        {header}
        <Screen bottomInset>
          <View className="gap-3">
            <Skeleton className="h-8 w-40" />
            <Skeleton className="h-64 w-full" />
            <Skeleton className="h-64 w-full" />
          </View>
        </Screen>
      </View>
    );
  }

  const open = isActive(trade.status);
  const net = settlementOf(trade);
  const account = accountOf(trade);
  // The asset's own precision, as the chart and ticket use - not a fixed five places.
  const asset = assetsQuery.data?.find((a) => a.id === trade.assetId);
  const precision = asset?.pricePrecision ?? 2;
  const currentPrice = tick?.price ?? null;
  const money = (minor: number) => formatMoney(minor, trade.currency);

  const fields: FieldProps[] = [
    { label: 'Asset', value: trade.symbol },
    { label: 'Status', value: statusLabel(trade.status) },
    { label: 'Direction', value: <DirectionWord direction={trade.direction} className="text-sm" /> },
    { label: 'Investment', value: money(trade.stake) },
    { label: 'Entry price', icon: <EntrySpot />, value: formatPrice(trade.entryPrice, precision), mono: true },
    { label: 'Duration', value: formatExpiry(durationOf(trade)) },
    {
      label: 'Expiry time',
      icon: <Icon as={Flag} size={12} className="text-content-400" />,
      value:
        clock(trade.expiryTimestamp) +
        ', ' +
        new Date(trade.expiryTimestamp).toLocaleDateString('en-GB', { day: 'numeric', month: 'short' }),
    },
    {
      label: open ? 'Current price' : 'Expiry price',
      icon: open ? undefined : <ExitSpot />,
      value: open
        ? currentPrice !== null
          ? formatPrice(currentPrice, precision)
          : '--'
        : trade.expiryPrice !== null
          ? formatPrice(trade.expiryPrice, precision)
          : '--',
      mono: true,
    },
    { label: 'Payout', value: trade.payoutPercent + '%' },
    {
      label: open ? 'Return if it wins' : 'Returned',
      value: open ? money(trade.potentialReturn) : money(payoutOf(trade)),
    },
    {
      label: 'Result',
      value: open ? 'Pending' : net !== null ? formatSignedMoney(net, trade.currency) : '--',
      tone: open || net === null ? undefined : net > 0 ? 'up' : net < 0 ? 'down' : undefined,
    },
    { label: 'Maximum loss', value: money(trade.stake) },
    {
      label: 'Placed at',
      icon: <Icon as={Timer} size={12} className="text-content-400" />,
      value: formatDateTime(trade.entryTimestamp),
    },
    { label: 'Settled at', value: trade.settledAt ? formatDateTime(trade.settledAt) : '--' },
    { label: 'Account', value: account === 'DEMO' ? 'Demo' : 'Live' },
    { label: 'Trade ID', value: trade.reference, mono: true },
  ];

  return (
    <View className="flex-1 bg-surface-950">
      {header}
      <Screen bottomInset onRefresh={onRefresh} refreshing={refreshing}>
        <View className="gap-3">
          <Panel className="overflow-hidden">
            <View className="flex-row flex-wrap items-center gap-3 px-5 py-4">
              <View className="min-w-0 shrink">
                <View className="flex-row items-center gap-2">
                  <Text accessibilityRole="header" className="text-lg font-semibold text-content-100">
                    {trade.symbol}
                  </Text>
                  <DemoTag account={account} />
                </View>
                {asset ? <Text className="text-xs text-content-400">{asset.name}</Text> : null}
              </View>

              <View className="ml-auto items-end">
                <StatusPill trade={trade} />
                <Text
                  className={cn(
                    'mt-1 text-xl font-bold tnum',
                    open
                      ? 'text-content-100'
                      : (net ?? 0) > 0
                        ? 'text-up'
                        : (net ?? 0) < 0
                          ? 'text-down'
                          : 'text-content-200',
                  )}
                >
                  {open ? formatCountdown(remaining) : net !== null ? formatSignedMoney(net, trade.currency) : '--'}
                </Text>
              </View>
            </View>
          </Panel>

          <Panel className="min-w-0 overflow-hidden">
            <View className="flex-row items-center gap-2 border-b border-surface-800 px-4 py-2.5">
              <Text className="text-sm font-semibold text-content-100">Price during this trade</Text>
              <DirectionWord direction={trade.direction} className="text-xs" />
              <Text className="ml-auto text-[11px] text-content-400">Times in {zoneChip(zone)}</Text>
            </View>
            <TradeWindowChart trade={trade} precision={precision} zone={zone} />
          </Panel>

          <Panel className="overflow-hidden">
            <View className="flex-row flex-wrap">
              {fields.map((field, index) => (
                <Field
                  key={field.label}
                  {...field}
                  className={cn(
                    'w-1/2',
                    index % 2 === 0 && 'border-r border-surface-800/60',
                    index < fields.length - 2 && 'border-b border-surface-800/60',
                  )}
                />
              ))}
            </View>

            <Text className="border-t border-surface-800 px-5 py-3 text-[11px] leading-[18px] text-content-400">
              Entry price, timestamps and payout were fixed by the platform when this trade was accepted and are never
              changed afterwards. The expiry price is the last quoted price at or before the expiry instant.
            </Text>
          </Panel>
        </View>
      </Screen>
    </View>
  );
}

/** The chart's entry marker, as a legend beside the figure it marks. */
function EntrySpot() {
  return <View className="h-2.5 w-2.5 rounded-full border-[1.5px] border-content-400" />;
}

function ExitSpot() {
  return <View className="h-2.5 w-2.5 rounded-full bg-content-400" />;
}

interface FieldProps {
  label: string;
  value: ReactNode;
  mono?: boolean;
  tone?: 'up' | 'down';
  /** The chart marker this figure corresponds to. */
  icon?: ReactNode;
}

function Field({ label, value, mono, tone, icon, className }: FieldProps & { className?: string }) {
  return (
    <View className={cn('min-w-0 bg-surface-900 px-5 py-3', className)}>
      <View className="flex-row items-center gap-1.5">
        {icon}
        <Text className="text-[10px] uppercase tracking-wider text-content-400">{label}</Text>
      </View>
      {typeof value === 'string' || typeof value === 'number' ? (
        <Text
          className={cn(
            'mt-0.5 text-sm font-medium tnum',
            tone === 'up' ? 'text-up' : tone === 'down' ? 'text-down' : 'text-content-100',
          )}
          style={mono ? { fontFamily: MONO } : undefined}
        >
          {value}
        </Text>
      ) : (
        <View className="mt-0.5">{value}</View>
      )}
    </View>
  );
}
