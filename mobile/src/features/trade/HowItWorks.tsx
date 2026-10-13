import type { ReactNode } from 'react';
import { Pressable, View } from 'react-native';
import { router } from 'expo-router';
import Svg, { Circle, Line, Path, Polyline, Rect, Text as SvgText } from 'react-native-svg';
import { ArrowRight } from 'lucide-react-native';
import { Icon, Sheet, Text } from '../../components/ui';
import { formatMoney, formatSignedMoney, toMinor } from '../../utils/format';
import { useTokenColor } from '../../utils/theme-colors';
import { contractTerms } from './ticketModel';
import { TIE_CLAUSE, TIE_LABEL, TIE_SENTENCE, useDrawPolicy } from './drawRule';
import type { DrawPolicy } from '../../types/api';

/**
 * "How a trade works", opened from the ticket (brief UX-18, reference record
 * 19 - Deriv's "How to trade Rise/Fall?" link at the top of its trade panel).
 *
 * The explanation has to be reachable at the moment the question comes up,
 * which is on the trade screen, without taking the trade screen away. So it is
 * a bottom sheet over the workspace rather than a trip to another screen.
 *
 * One picture and four facts, all matching how the platform actually settles
 * a trade; Learn keeps the full guide. The example uses the trader's own amount
 * and payout when the ticket holds a valid one, so the numbers they read here
 * are the numbers they are about to agree to.
 */

interface Example {
  stake: number | null;
  payoutPercent: number | null;
  currency: string;
}

/** The illustration used when the ticket has nothing valid to show. */
const FALLBACK = { stake: toMinor(100), payoutPercent: 82 };

const BODY = 'text-sm leading-relaxed text-content-300';

export function HowItWorksSheet({ open, onClose, example }: { open: boolean; onClose: () => void; example: Example }) {
  const drawPolicy = useDrawPolicy();
  return (
    <Sheet
      open={open}
      onClose={onClose}
      title="How a trade works"
      subtitle="Fixed-time trading in one picture and four facts."
    >
      <View className="gap-4 px-4 pb-5">
        <Text className={BODY}>
          You predict whether the price will be <Text className="text-sm font-bold text-up">higher</Text> or{' '}
          <Text className="text-sm font-bold text-down">lower</Text> than your entry price when the trade ends.
        </Text>

        <TradeDiagram drawPolicy={drawPolicy} />

        <View className="gap-2.5">
          <Fact n={1} title="Your entry price">
            {"It is the platform's price at the moment your trade is accepted. If the price moves too far while your order is on its way, nothing is placed and you are shown the new price instead."}
          </Fact>
          <Fact n={2} title="UP or DOWN">
            <Text className="text-sm font-bold text-up">UP</Text> wins if the price at expiry is above your entry price.{' '}
            <Text className="text-sm font-bold text-down">DOWN</Text> wins if it is below.
          </Fact>
          <Fact n={3} title="The expiry decides">
            The trade ends at the expiry time shown before you confirm. The last price at or before that moment is
            compared with your entry price. {TIE_SENTENCE[drawPolicy]}
          </Fact>
          <Fact n={4} title="What a win pays, what a loss costs">
            A win returns your investment plus the profit: investment × payout, fixed when the trade is accepted. A
            loss costs the investment, never more.
          </Fact>
        </View>

        <Outcomes example={example} drawPolicy={drawPolicy} />

        <Text className="text-xs text-content-400">
          You can also sell a running trade before expiry. That price is worked out live and includes a fee.
        </Text>

        <Pressable
          onPress={() => {
            onClose();
            router.navigate({ pathname: '/learn', params: { section: 'how-it-works' } });
          }}
          accessibilityRole="link"
          className="flex-row items-center gap-1.5 self-start py-1"
        >
          <Text className="text-sm font-semibold text-accent">Read the full guide in Learn</Text>
          <Icon as={ArrowRight} size={16} className="text-accent" />
        </Pressable>
      </View>
    </Sheet>
  );
}

function Fact({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <View className="flex-row gap-3">
      <View
        importantForAccessibility="no-hide-descendants"
        accessibilityElementsHidden
        className="h-6 w-6 items-center justify-center rounded-full bg-surface-800"
      >
        <Text className="text-xs font-bold tnum text-content-100">{n}</Text>
      </View>
      <View className="min-w-0 flex-1">
        <Text className="text-sm font-semibold leading-relaxed text-content-100">{title}</Text>
        <Text className={BODY}>{children}</Text>
      </View>
    </View>
  );
}

/** The three ways a trade can end, in money, for the ticket's own terms. */
function Outcomes({ example, drawPolicy }: { example: Example; drawPolicy: DrawPolicy }) {
  const own = contractTerms(example.stake, example.payoutPercent);
  const stake = own ? example.stake! : FALLBACK.stake;
  const payout = own ? example.payoutPercent! : FALLBACK.payoutPercent;
  const terms = own ?? contractTerms(FALLBACK.stake, FALLBACK.payoutPercent)!;
  const currency = own ? example.currency : 'INR';
  const money = (minor: number) => formatMoney(minor, currency);
  const aside = (text: string) => <Text className="text-[13px] font-normal text-content-400">{text}</Text>;

  return (
    <View accessibilityLabel="What your trade can pay" className="rounded-xl bg-surface-850 px-3 py-2.5">
      <Text className="text-xs text-content-400">
        {own ? 'With your ' : 'For example, '}
        <Text className="text-xs font-bold tnum text-content-200">{money(stake)}</Text> at a{' '}
        <Text className="text-xs font-bold tnum text-content-200">{payout}%</Text> payout:
      </Text>
      <View className="mt-1.5 gap-1">
        <Row label="You are right" tone="text-up">
          {formatSignedMoney(terms.profit, currency)} {aside('(' + money(terms.totalReturn) + ' back)')}
        </Row>
        <Row label="You are wrong" tone="text-down">
          {formatSignedMoney(-terms.maxLoss, currency)}
        </Row>
        {drawPolicy === 'LOSS' ? (
          <Row label="Same price" tone="text-down">
            {formatSignedMoney(-terms.maxLoss, currency)} {aside('(a loss)')}
          </Row>
        ) : drawPolicy === 'WIN' ? (
          <Row label="Same price" tone="text-up">
            {formatSignedMoney(terms.profit, currency)} {aside('(a win)')}
          </Row>
        ) : (
          <Row label="Same price" tone="text-content-100">
            {money(stake)} {aside('back (draw)')}
          </Row>
        )}
      </View>
    </View>
  );
}

function Row({ label, tone, children }: { label: string; tone: string; children: ReactNode }) {
  return (
    <View className="flex-row items-baseline justify-between gap-3">
      <Text className="text-[13px] text-content-400">{label}</Text>
      <Text className={'shrink text-right text-[13px] font-semibold tnum ' + tone}>{children}</Text>
    </View>
  );
}

/**
 * Entry, expiry and the two zones between them.
 *
 * Drawn from theme tokens, so it reads the same in all three themes. The price
 * path stops at the entry: what happens after it is the trader's call, and a
 * drawn continuation would look like a forecast.
 */
function TradeDiagram({ drawPolicy }: { drawPolicy: DrawPolicy }) {
  const upFill = useTokenColor('up', 0.1);
  const downFill = useTokenColor('down', 0.1);
  const up = useTokenColor('up');
  const down = useTokenColor('down');
  const content200 = useTokenColor('content-200');
  const content300 = useTokenColor('content-300');
  const content400 = useTokenColor('content-400');
  const accent = useTokenColor('accent');
  const surface900 = useTokenColor('surface-900');
  const surface600 = useTokenColor('surface-600');

  const entryX = 104;
  const expiryX = 284;
  const entryY = 80;
  const top = 14;
  const bottom = 146;
  const middle = (entryX + expiryX) / 2;

  return (
    <View
      accessible
      accessibilityRole="image"
      accessibilityLabel={
        'How a trade is decided. The trade starts at your entry price and runs until expiry. If the price at expiry is above the entry price, UP wins. If it is below, DOWN wins. If it is exactly the same, ' +
        TIE_CLAUSE[drawPolicy] +
        '.'
      }
      className="rounded-xl border border-surface-700 bg-surface-950/40 p-2"
    >
      <Svg viewBox="0 0 320 178" width="100%" style={{ aspectRatio: 320 / 178 }}>
        {/* The two outcomes, between entry and expiry. */}
        <Rect x={entryX} y={top} width={expiryX - entryX} height={entryY - top} fill={upFill} />
        <Rect x={entryX} y={entryY} width={expiryX - entryX} height={bottom - entryY} fill={downFill} />
        <SvgText x={middle} y={42} textAnchor="middle" fill={up} fontSize={12} fontWeight="bold">
          UP wins
        </SvgText>
        <SvgText x={middle} y={56} textAnchor="middle" fill={content400} fontSize={9.5}>
          price ends above the entry
        </SvgText>
        <SvgText x={middle} y={112} textAnchor="middle" fill={down} fontSize={12} fontWeight="bold">
          DOWN wins
        </SvgText>
        <SvgText x={middle} y={126} textAnchor="middle" fill={content400} fontSize={9.5}>
          price ends below the entry
        </SvgText>

        {/* The market before the trade. */}
        <Polyline
          points={`10,104 22,94 34,99 46,82 58,90 70,72 82,86 94,76 ${entryX},${entryY}`}
          fill="none"
          stroke={content300}
          strokeWidth={2}
          strokeLinejoin="round"
          strokeLinecap="round"
        />

        {/* Entry price, carried to expiry. */}
        <Line x1={entryX} y1={entryY} x2={expiryX} y2={entryY} stroke={content200} strokeWidth={1.5} strokeDasharray="5 4" />
        <SvgText x={entryX + 6} y={entryY - 5} fill={content200} fontSize={9.5} fontWeight="600">
          Entry price
        </SvgText>
        <SvgText x={expiryX - 5} y={entryY - 5} textAnchor="end" fill={content400} fontSize={9}>
          {'same price = ' + TIE_LABEL[drawPolicy]}
        </SvgText>
        <Circle cx={entryX} cy={entryY} r={4.5} fill={accent} stroke={surface900} strokeWidth={2} />

        {/* Expiry. */}
        <Line x1={expiryX} y1={top} x2={expiryX} y2={bottom} stroke={content400} strokeWidth={1.5} />
        <Path d={`M${expiryX} ${top} h14 l-4 5 l4 5 h-14 z`} fill={content400} />

        {/* Time. */}
        <Line x1={6} y1={bottom + 6} x2={314} y2={bottom + 6} stroke={surface600} strokeWidth={1} />
        <Line x1={entryX} y1={bottom + 3} x2={entryX} y2={bottom + 9} stroke={content400} strokeWidth={1} />
        <Line x1={expiryX} y1={bottom + 3} x2={expiryX} y2={bottom + 9} stroke={content400} strokeWidth={1} />
        <SvgText x={entryX} y={bottom + 22} textAnchor="middle" fill={content300} fontSize={10} fontWeight="600">
          Entry
        </SvgText>
        <SvgText x={middle} y={bottom + 22} textAnchor="middle" fill={content400} fontSize={9.5}>
          ← duration →
        </SvgText>
        <SvgText x={expiryX} y={bottom + 22} textAnchor="middle" fill={content300} fontSize={10} fontWeight="600">
          Expiry
        </SvgText>
      </Svg>
    </View>
  );
}
