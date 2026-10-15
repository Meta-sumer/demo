import type { ReactNode } from 'react';
import { View, type TextStyle } from 'react-native';
import { Text } from '../../../components/ui';
import { assetAvailability } from '../../../components/assetList';
import { formatPrice } from '../../../utils/format';
import { withAlpha } from '../../../utils/theme-colors';
import type { Asset } from '../../../types/api';
import type { ChartLook } from './engine/colors';

/**
 * The chart's legend, top-left, the way TradingView's mobile app writes it.
 *
 *   [flags] Asset name  (•)          - the instrument, and whether it trades now
 *   1.32430  +0.00062  (+0.05%)      - the price and its 24-hour move
 *
 * While the crosshair is out, the second line is the open, high, low and
 * close of the bar under it instead. Anything else the chart wants to say -
 * the indicators on it, the feed clock, the nearest expiry - follows as
 * further rows. It takes no touches: the candles under it stay reachable.
 */
export interface LegendBar {
  open: number;
  high: number;
  low: number;
  close: number;
}

interface Props {
  asset: Asset;
  look: ChartLook;
  digits: number;
  /** The bar under the crosshair, or null while the crosshair is away. */
  hovered: LegendBar | null;
  /** The newest price known. */
  price: number | null;
  maxWidth: number;
  children?: ReactNode;
}

export function ChartLegend({ asset, look, digits, hovered, price, maxWidth, children }: Props) {
  // Legible over a candle without a box: a halo in the plot's own colour.
  const halo: TextStyle = { textShadowColor: look.background, textShadowRadius: 3, textShadowOffset: { width: 0, height: 0 } };
  const tradable = assetAvailability(asset) === 'open';

  return (
    <View pointerEvents="none" className="absolute left-2 top-2 gap-1" style={{ maxWidth }}>
      <View className="flex-row items-center gap-1.5">
        <AssetIcons symbol={asset.symbol} look={look} />
        <Text numberOfLines={1} className="shrink text-[13px] font-semibold" style={[halo, { color: look.strongText }]}>
          {asset.name}
        </Text>
        <View
          accessibilityLabel={tradable ? 'Market open' : 'Market closed'}
          className="h-4 w-5 items-center justify-center rounded-full"
          style={{ backgroundColor: tradable ? withAlpha(look.bull, 0.18) : withAlpha(look.text, 0.18) }}
        >
          <View className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: tradable ? look.bull : look.text }} />
        </View>
      </View>

      {hovered ? (
        <OhlcRow bar={hovered} digits={digits} look={look} halo={halo} />
      ) : price !== null ? (
        <PriceRow price={price} percent={asset.change24hPercent} digits={digits} look={look} halo={halo} />
      ) : null}

      {children}
    </View>
  );
}

function PriceRow({
  price,
  percent,
  digits,
  look,
  halo,
}: {
  price: number;
  percent: number | null;
  digits: number;
  look: ChartLook;
  halo: TextStyle;
}) {
  const known = percent !== null && Number.isFinite(percent) && percent > -100;
  // The move in price terms, from the price now and its percentage change.
  const change = known ? price - price / (1 + percent! / 100) : 0;
  const colour = !known || change === 0 ? look.text : change > 0 ? look.bull : look.bear;
  const sign = change > 0 ? '+' : change < 0 ? '-' : '';
  return (
    <View className="flex-row flex-wrap items-baseline gap-x-2">
      <Text className="text-[13px] font-semibold tnum" style={[halo, { color: known ? colour : look.strongText }]}>
        {formatPrice(price, digits)}
      </Text>
      {known ? (
        <>
          <Text className="text-xs font-medium tnum" style={[halo, { color: colour }]}>
            {sign + formatPrice(Math.abs(change), digits)}
          </Text>
          <Text className="text-xs font-medium tnum" style={[halo, { color: colour }]}>
            {'(' + (percent! > 0 ? '+' : '') + percent!.toFixed(2) + '%)'}
          </Text>
        </>
      ) : null}
    </View>
  );
}

function OhlcRow({ bar, digits, look, halo }: { bar: LegendBar; digits: number; look: ChartLook; halo: TextStyle }) {
  const colour = bar.close >= bar.open ? look.bull : look.bear;
  const cell = (label: string, value: number) => (
    <View key={label} className="flex-row items-baseline gap-1">
      <Text className="text-xs font-medium" style={[halo, { color: look.text }]}>
        {label}
      </Text>
      <Text className="text-xs font-semibold tnum" style={[halo, { color: colour }]}>
        {value.toFixed(digits)}
      </Text>
    </View>
  );
  return (
    <View className="flex-row flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
      {cell('O', bar.open)}
      {cell('H', bar.high)}
      {cell('L', bar.low)}
      {cell('C', bar.close)}
    </View>
  );
}

/* ------------------------------------------------------------------ icons */

/** Currencies a pair can be written in, with the flag that stands for each. */
const FLAGS: Record<string, string> = {
  USD: '🇺🇸',
  EUR: '🇪🇺',
  GBP: '🇬🇧',
  JPY: '🇯🇵',
  AUD: '🇦🇺',
  NZD: '🇳🇿',
  CAD: '🇨🇦',
  CHF: '🇨🇭',
  CNY: '🇨🇳',
  CNH: '🇨🇳',
  HKD: '🇭🇰',
  SGD: '🇸🇬',
  INR: '🇮🇳',
  SEK: '🇸🇪',
  NOK: '🇳🇴',
  DKK: '🇩🇰',
  PLN: '🇵🇱',
  ZAR: '🇿🇦',
  MXN: '🇲🇽',
  TRY: '🇹🇷',
  AED: '🇦🇪',
  SAR: '🇸🇦',
  BRL: '🇧🇷',
  RUB: '🇷🇺',
  KRW: '🇰🇷',
  THB: '🇹🇭',
};

/** Badge colours for the instruments that are not a country's money. */
const BADGES: Record<string, string> = {
  BTC: '#f7931a',
  ETH: '#627eea',
  XAU: '#c9a227',
  XAG: '#8e9aa6',
  SOL: '#9945ff',
  XRP: '#23292f',
  LTC: '#345d9d',
  BNB: '#f0b90b',
  DOG: '#c2a633',
  ADA: '#0033ad',
  USDT: '#26a17b',
};

const PALETTE = ['#2962ff', '#7e57c2', '#00897b', '#ef6c00', '#5d4037', '#c2185b'];

/**
 * The instrument's icons: for a currency pair, its two flags overlapping;
 * for a coin or a metal, a round badge with the symbol's first letters (and
 * the quote currency's flag behind it when there is one).
 */
export function splitSymbol(symbol: string): { base: string; quote: string | null } {
  const letters = symbol.toUpperCase().replace(/[^A-Z0-9]/g, '');
  const slash = symbol.indexOf('/');
  if (slash > 0) {
    return {
      base: symbol.slice(0, slash).toUpperCase().replace(/[^A-Z0-9]/g, ''),
      quote: symbol.slice(slash + 1).toUpperCase().replace(/[^A-Z0-9]/g, '') || null,
    };
  }
  if (letters.length >= 6 && letters.length <= 7) return { base: letters.slice(0, 3), quote: letters.slice(3) };
  return { base: letters, quote: null };
}

function AssetIcons({ symbol, look }: { symbol: string; look: ChartLook }) {
  const { base, quote } = splitSymbol(symbol);
  return (
    <View className="flex-row items-center">
      <Disc code={base} look={look} />
      {quote ? (
        <View style={{ marginLeft: -6 }}>
          <Disc code={quote} look={look} />
        </View>
      ) : null}
    </View>
  );
}

const DISC = 18;

function Disc({ code, look }: { code: string; look: ChartLook }) {
  const flag = FLAGS[code];
  const ring = { borderWidth: 1.5, borderColor: look.background };
  if (flag) {
    return (
      <View
        className="items-center justify-center overflow-hidden rounded-full"
        style={[{ width: DISC + 3, height: DISC + 3, backgroundColor: look.panel }, ring]}
      >
        <Text
          allowFontScaling={false}
          style={{ fontSize: 16, lineHeight: DISC + 1, textAlign: 'center', includeFontPadding: false }}
        >
          {flag}
        </Text>
      </View>
    );
  }
  const letters = (code || '?').slice(0, code.length > 3 ? 4 : 3);
  const colour = BADGES[code] ?? BADGES[code.slice(0, 3)] ?? PALETTE[hash(code) % PALETTE.length]!;
  return (
    <View
      className="items-center justify-center rounded-full"
      style={[{ width: DISC + 3, height: DISC + 3, backgroundColor: colour }, ring]}
    >
      <Text
        allowFontScaling={false}
        className="font-black text-white"
        style={{ fontSize: letters.length > 3 ? 5.5 : 6.5, includeFontPadding: false }}
      >
        {letters}
      </Text>
    </View>
  );
}

function hash(text: string): number {
  let value = 0;
  for (let i = 0; i < text.length; i += 1) value = (value * 31 + text.charCodeAt(i)) >>> 0;
  return value;
}
