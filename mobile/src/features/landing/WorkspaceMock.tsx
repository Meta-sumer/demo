import type { ReactNode } from 'react';
import { View } from 'react-native';
import Svg, { Circle, G, Line, Path, Polygon, Rect, Text as SvgText } from 'react-native-svg';
import { Text } from '../../components/ui';
import { cn } from '../../utils/cn';

/**
 * The trading workspace, drawn rather than screenshotted.
 *
 * A landing page for a trading platform has to show the thing it is selling,
 * and a captured image is the wrong way to do it here: it would need a
 * logged-in session to produce, go stale the first time the panel moved, blur
 * on a dense screen and weigh more than the rest of the page put together.
 *
 * So this is the real layout rebuilt in views - the same rail, the same top
 * bar, the same right-hand ticket, and the same order marker the chart draws
 * (banner, nose, entry dot, run, expiry cap). It scales to any width, stays
 * sharp, and when the workspace changes this is a file that can change with
 * it rather than an image nobody remembers to retake.
 *
 * `compact` drops the rail and the trade list for the phone frame, which is
 * what the real app does at that width too.
 *
 * It is a picture: it is laid out at a fixed size and scaled by `FitBox`, so
 * its type ignores the system font size, and a screen reader is given one
 * description of it rather than every label inside it.
 */
export function WorkspaceMock({ compact = false }: { compact?: boolean }) {
  return (
    <View
      accessible
      accessibilityRole="image"
      accessibilityLabel="The trading chart with an open position"
      className="h-full w-full flex-col overflow-hidden bg-[#1d1f23]"
    >
      <View importantForAccessibility="no-hide-descendants" accessibilityElementsHidden className="flex-1">
        <TopBar compact={compact} />

        {/* On a phone the real app stacks the chart over the ticket rather than
            putting them side by side, and the rail collapses into the menu. The
            mock follows it, because a picture that shows a layout the app does
            not have is worse than no picture. */}
        {compact ? (
          <View className="min-h-0 flex-1 flex-col">
            <View className="h-[44%] shrink-0">
              <Chart />
            </View>
            <Ticket compact />
          </View>
        ) : (
          <View className="min-h-0 flex-1 flex-row">
            <IconRail />
            <View className="relative min-w-0 flex-1">
              <Chart />
            </View>
            <Ticket compact={false} />
          </View>
        )}
      </View>
    </View>
  );
}

/** Text at the mock's fixed size: it is scaled with the picture, not by the system font setting. */
function M({ className, children }: { className?: string; children: ReactNode }) {
  return (
    <Text allowFontScaling={false} className={cn('text-white', className)}>
      {children}
    </Text>
  );
}

function TopBar({ compact }: { compact: boolean }) {
  return (
    <View className="shrink-0 flex-row items-center gap-2 border-b border-white/10 bg-[#292c32] px-2.5 py-2">
      <View className="h-5 w-5 items-center justify-center rounded-md bg-brand">
        <M className="text-[7px] font-black text-[#1d1f23]">BT</M>
      </View>
      {compact ? null : <M className="text-[9px] font-black tracking-tight">BINARY</M>}

      <View className="ml-1 flex-row items-center gap-1.5 rounded-md bg-[#1d1f23] px-2 py-1">
        <View className="h-1 w-1 rounded-full bg-up" />
        <M className="text-[8px] font-bold">BTC/USD</M>
        <M className="text-[8px] font-bold text-brand">84%</M>
      </View>
      {compact ? null : (
        <View className="flex-row items-center gap-1.5 rounded-md bg-[#1d1f23] px-2 py-1">
          <View className="h-1 w-1 rounded-full bg-up" />
          <M className="text-[8px] font-bold text-white/60">ETH/USD</M>
        </View>
      )}

      <View className="ml-auto flex-row items-center gap-2">
        <View className="items-end rounded-md bg-[#1d1f23] px-2 py-1">
          <M className="text-[6px] font-bold uppercase tracking-wider text-brand">Demo</M>
          <M className="text-[8px] font-bold tnum">₹96,199</M>
        </View>
        {compact ? null : (
          <View className="rounded-md bg-brand px-2 py-1">
            <M className="text-[8px] font-bold text-[#1d1f23]">Deposit</M>
          </View>
        )}
      </View>
    </View>
  );
}

function IconRail() {
  return (
    <View className="w-10 shrink-0 flex-col items-center gap-3 border-r border-white/10 bg-[#292c32] py-3">
      {['Trade', 'Wallet', 'History', 'Account'].map((label, i) => (
        <View key={label} className="flex-col items-center gap-0.5">
          <View className={cn('h-4 w-4 rounded-md', i === 0 ? 'bg-brand/25' : 'bg-white/10')} />
          <M className={cn('text-[5px] font-bold', i === 0 ? 'text-brand' : 'text-white/35')}>
            {label.toUpperCase()}
          </M>
        </View>
      ))}
    </View>
  );
}

/** Candle geometry, fixed so the picture is the same on every render. */
const CANDLES: Array<[number, number, number, number]> = [
  [52, 60, 44, 48],
  [48, 52, 38, 42],
  [42, 50, 40, 47],
  [47, 58, 45, 55],
  [55, 57, 46, 49],
  [49, 54, 42, 44],
  [44, 48, 34, 38],
  [38, 46, 36, 44],
  [44, 56, 42, 54],
  [54, 64, 52, 61],
  [61, 63, 53, 56],
  [56, 66, 54, 64],
  [64, 72, 60, 70],
  [70, 76, 64, 74],
];

function Chart() {
  const step = 18;
  const left = 10;
  const toY = (v: number) => 150 - v * 1.35;
  // The newest candle, with the expiry out in the empty space the real chart
  // keeps to its right - which is where an open order actually sits.
  const entryIndex = CANDLES.length - 1;
  const entryX = left + entryIndex * step;
  const entryY = toY(74);
  const expiryX = entryX + 58;

  return (
    <Svg width="100%" height="100%" viewBox="0 0 400 168">
      <Rect width="400" height="168" fill="#1e2024" />

      {[28, 62, 96, 130].map((y) => (
        <Line key={y} x1="0" y1={y} x2="352" y2={y} stroke="#30343a" strokeWidth="0.7" />
      ))}
      {[70, 160, 250, 340].map((x) => (
        <Line key={x} x1={x} y1="0" x2={x} y2="158" stroke="#30343a" strokeWidth="0.7" />
      ))}

      {CANDLES.map(([o, h, l, c], i) => {
        const x = left + i * step;
        const up = c >= o;
        const colour = up ? '#62c88a' : '#ea6d6d';
        return (
          <G key={i}>
            <Line x1={x} y1={toY(h)} x2={x} y2={toY(l)} stroke={colour} strokeWidth="1" />
            <Rect
              x={x - 3.6}
              y={toY(Math.max(o, c))}
              width="7.2"
              height={Math.max(1.6, Math.abs(toY(o) - toY(c)))}
              fill={colour}
              rx="0.8"
            />
          </G>
        );
      })}

      {/* The order, in the chart's own language. */}
      <Line x1={entryX} y1={entryY} x2={expiryX} y2={entryY} stroke="#f6d84a" strokeWidth="1.6" />
      <Polygon
        points={`${expiryX},${entryY - 5} ${expiryX - 3.6},${entryY} ${expiryX + 3.6},${entryY}`}
        fill="#f6d84a"
      />
      <Circle cx={entryX} cy={entryY} r="3.4" fill="#f6d84a" />
      <G transform={`translate(${entryX - 56}, ${entryY - 7.5})`}>
        <Path d="M0 0 H46 L51 7.5 L46 15 H0 Z" fill="#f6d84a" />
        <SvgText x="6" y="10.5" fontSize="7.5" fontWeight="700" fill="#1d1f23">
          ↑ ₹500
        </SvgText>
      </G>
      <Line
        x1={expiryX}
        y1="6"
        x2={expiryX}
        y2="152"
        stroke="#f6d84a"
        strokeWidth="0.7"
        strokeDasharray="3 3"
        opacity="0.55"
      />
      <G transform={`translate(${expiryX - 12}, 6)`}>
        <Rect width="24" height="11" rx="3" fill="#292c32" stroke="#f6d84a" strokeWidth="0.5" />
        <SvgText x="12" y="8" fontSize="7" fontWeight="700" fill="#f5f5f5" textAnchor="middle">
          00:48
        </SvgText>
      </G>

      {/* Price axis. */}
      <Rect x="352" y="0" width="48" height="168" fill="#1e2024" />
      <Line x1="352" y1="0" x2="352" y2="168" stroke="#30343a" strokeWidth="0.7" />
      {[28, 62, 96, 130].map((y, i) => (
        <SvgText key={y} x="358" y={y + 2.5} fontSize="6.5" fill="#747982">
          {(148 - i * 4).toFixed(0) + ',000'}
        </SvgText>
      ))}
      <G transform={`translate(352, ${entryY - 5.5})`}>
        <Rect width="46" height="11" rx="2" fill="#f6d84a" />
        <SvgText x="4" y="8" fontSize="7" fontWeight="700" fill="#1d1f23">
          144,503.60
        </SvgText>
      </G>

      {/* Toolbar, bottom left, clear of the time axis. */}
      <G transform="translate(10, 140)">
        {['1m', '', '', ''].map((label, i) => (
          <G key={i} transform={`translate(${i * 17}, 0)`}>
            <Rect width="14" height="14" rx="3.5" fill="#292c32" stroke="#44474f" strokeWidth="0.5" />
            {label ? (
              <SvgText x="7" y="9.5" fontSize="6.5" fontWeight="700" fill="#f5f5f5" textAnchor="middle">
                {label}
              </SvgText>
            ) : (
              <Rect x="4" y="5" width="6" height="4" rx="1" fill="#777d87" />
            )}
          </G>
        ))}
      </G>
    </Svg>
  );
}

function Ticket({ compact }: { compact: boolean }) {
  return (
    <View
      className={cn(
        'min-h-0 flex-col gap-1.5 bg-[#292c32] p-2',
        compact ? 'flex-1' : 'w-[118px] shrink-0 border-l border-white/10',
      )}
    >
      <Stepper label="Amount" value="₹500" />
      <Stepper label="Time" value="16:53" />

      <View className="flex-row items-baseline justify-between px-0.5">
        <M className="text-[7px] text-white/50">Earnings</M>
        <M className="text-[8px] font-bold text-brand">+84% ₹920</M>
      </View>

      <View className="flex-row gap-1.5">
        <View className="h-8 flex-1 items-center justify-center rounded-lg bg-[#2e9962]">
          <M className="text-sm font-black text-white">↑</M>
        </View>
        <View className="h-8 flex-1 items-center justify-center rounded-lg bg-[#cc4747]">
          <M className="text-sm font-black text-white">↓</M>
        </View>
      </View>

      <View className="mt-1 rounded-lg bg-[#1d1f23] p-1.5">
        <View className="flex-row items-center justify-between">
          <View className="rounded bg-up/20 px-1 py-px">
            <M className="text-[6px] font-bold text-up">UP</M>
          </View>
          <M className="text-[7px] font-bold tnum">₹500</M>
          <M className="text-[7px] font-bold text-white/60 tnum">00:48</M>
        </View>
        <M className="mt-1 text-[6px] text-white/45 tnum">144,503.60 → 148,965.44</M>
        <View className="mt-1 rounded bg-brand px-1 py-0.5">
          <M className="text-center text-[6.5px] font-bold text-[#1d1f23]">Sell now ₹762.31</M>
        </View>
      </View>
    </View>
  );
}

function Stepper({ label, value }: { label: string; value: string }) {
  return (
    <View className="flex-row items-center justify-between rounded-lg bg-[#1d1f23] px-1.5 py-1">
      <M className="text-[9px] font-bold text-white/40">−</M>
      <View className="items-center">
        <M className="text-[6px] text-white/45">{label}</M>
        <M className="text-[9px] font-bold tnum">{value}</M>
      </View>
      <M className="text-[9px] font-bold text-white/40">+</M>
    </View>
  );
}
