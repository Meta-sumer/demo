import clsx from 'clsx';

/**
 * The trading workspace, drawn rather than screenshotted.
 *
 * A landing page for a trading platform has to show the thing it is selling,
 * and a captured PNG is the wrong way to do it here: it would need a logged-in
 * session to produce, go stale the first time the panel moved, blur on a
 * retina screen and weigh more than the rest of the page put together.
 *
 * So this is the real layout rebuilt in markup - the same rail, the same top
 * bar, the same right-hand ticket, and the same order marker the chart draws
 * (banner, nose, entry dot, run, expiry cap). It scales to any width, stays
 * sharp, and when the workspace changes this is a file that can change with
 * it rather than an image nobody remembers to retake.
 *
 * `compact` drops the rail and the trade list for the phone frame, which is
 * what the real app does at that width too.
 */
export function WorkspaceMock({ compact = false }: { compact?: boolean }) {
  return (
    <div className="flex h-full w-full flex-col overflow-hidden bg-[#1d1f23] text-white">
      <TopBar compact={compact} />

      {/* On a phone the real app stacks the chart over the ticket rather than
          putting them side by side, and the rail collapses into the menu. The
          mock follows it, because a picture that shows a layout the app does
          not have is worse than no picture. */}
      {compact ? (
        <div className="flex min-h-0 flex-1 flex-col">
          <div className="h-[44%] shrink-0">
            <Chart />
          </div>
          <Ticket compact />
        </div>
      ) : (
        <div className="flex min-h-0 flex-1">
          <IconRail />
          <div className="relative min-w-0 flex-1">
            <Chart />
          </div>
          <Ticket compact={false} />
        </div>
      )}
    </div>
  );
}

function TopBar({ compact }: { compact: boolean }) {
  return (
    <div className="flex shrink-0 items-center gap-2 border-b border-white/10 bg-[#292c32] px-2.5 py-2">
      <span className="grid h-5 w-5 place-items-center rounded-md bg-brand text-[7px] font-black text-[#1d1f23]">
        BT
      </span>
      {compact ? null : <span className="text-[9px] font-black tracking-tight">BINARY</span>}

      <span className="ml-1 flex items-center gap-1.5 rounded-md bg-[#1d1f23] px-2 py-1">
        <span className="h-1 w-1 rounded-full bg-up" />
        <span className="text-[8px] font-bold">BTC/USD</span>
        <span className="text-[8px] font-bold text-brand">84%</span>
      </span>
      {compact ? null : (
        <span className="flex items-center gap-1.5 rounded-md bg-[#1d1f23] px-2 py-1">
          <span className="h-1 w-1 rounded-full bg-up" />
          <span className="text-[8px] font-bold text-white/60">ETH/USD</span>
        </span>
      )}

      <span className="ml-auto flex items-center gap-2">
        <span className="rounded-md bg-[#1d1f23] px-2 py-1 text-right">
          <span className="block text-[6px] font-bold uppercase tracking-wider text-brand">Demo</span>
          <span className="block text-[8px] font-bold tnum">₹96,199</span>
        </span>
        {compact ? null : (
          <span className="rounded-md bg-brand px-2 py-1 text-[8px] font-bold text-[#1d1f23]">Deposit</span>
        )}
      </span>
    </div>
  );
}

function IconRail() {
  return (
    <div className="flex w-10 shrink-0 flex-col items-center gap-3 border-r border-white/10 bg-[#292c32] py-3">
      {['Trade', 'Wallet', 'History', 'Account'].map((label, i) => (
        <span key={label} className="flex flex-col items-center gap-0.5">
          <span className={clsx('h-4 w-4 rounded-md', i === 0 ? 'bg-brand/25' : 'bg-white/10')} />
          <span className={clsx('text-[5px] font-bold', i === 0 ? 'text-brand' : 'text-white/35')}>
            {label.toUpperCase()}
          </span>
        </span>
      ))}
    </div>
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
    <svg
      viewBox="0 0 400 168"
      className="h-full w-full"
      role="img"
      aria-label="The trading chart with an open position"
    >
      <rect width="400" height="168" fill="#1e2024" />

      {[28, 62, 96, 130].map((y) => (
        <line key={y} x1="0" y1={y} x2="352" y2={y} stroke="#30343a" strokeWidth="0.7" />
      ))}
      {[70, 160, 250, 340].map((x) => (
        <line key={x} x1={x} y1="0" x2={x} y2="158" stroke="#30343a" strokeWidth="0.7" />
      ))}

      {CANDLES.map(([o, h, l, c], i) => {
        const x = left + i * step;
        const up = c >= o;
        const colour = up ? '#62c88a' : '#ea6d6d';
        return (
          <g key={i}>
            <line x1={x} y1={toY(h)} x2={x} y2={toY(l)} stroke={colour} strokeWidth="1" />
            <rect
              x={x - 3.6}
              y={toY(Math.max(o, c))}
              width="7.2"
              height={Math.max(1.6, Math.abs(toY(o) - toY(c)))}
              fill={colour}
              rx="0.8"
            />
          </g>
        );
      })}

      {/* The order, in the chart's own language. */}
      <line x1={entryX} y1={entryY} x2={expiryX} y2={entryY} stroke="#f6d84a" strokeWidth="1.6" />
      <polygon
        points={`${expiryX},${entryY - 5} ${expiryX - 3.6},${entryY} ${expiryX + 3.6},${entryY}`}
        fill="#f6d84a"
      />
      <circle cx={entryX} cy={entryY} r="3.4" fill="#f6d84a" />
      <g transform={`translate(${entryX - 56}, ${entryY - 7.5})`}>
        <path d="M0 0 H46 L51 7.5 L46 15 H0 Z" fill="#f6d84a" />
        <text x="6" y="10.5" fontSize="7.5" fontWeight="700" fill="#1d1f23">
          ↑ ₹500
        </text>
      </g>
      <line
        x1={expiryX}
        y1="6"
        x2={expiryX}
        y2="152"
        stroke="#f6d84a"
        strokeWidth="0.7"
        strokeDasharray="3 3"
        opacity="0.55"
      />
      <g transform={`translate(${expiryX - 12}, 6)`}>
        <rect width="24" height="11" rx="3" fill="#292c32" stroke="#f6d84a" strokeWidth="0.5" />
        <text x="12" y="8" fontSize="7" fontWeight="700" fill="#f5f5f5" textAnchor="middle">
          00:48
        </text>
      </g>

      {/* Price axis. */}
      <rect x="352" y="0" width="48" height="168" fill="#1e2024" />
      <line x1="352" y1="0" x2="352" y2="168" stroke="#30343a" strokeWidth="0.7" />
      {[28, 62, 96, 130].map((y, i) => (
        <text key={y} x="358" y={y + 2.5} fontSize="6.5" fill="#747982">
          {(148 - i * 4).toFixed(0)},000
        </text>
      ))}
      <g transform={`translate(352, ${entryY - 5.5})`}>
        <rect width="46" height="11" rx="2" fill="#f6d84a" />
        <text x="4" y="8" fontSize="7" fontWeight="700" fill="#1d1f23">
          144,503.60
        </text>
      </g>

      {/* Toolbar, bottom left, clear of the time axis. */}
      <g transform="translate(10, 140)">
        {['1m', '', '', ''].map((label, i) => (
          <g key={i} transform={`translate(${i * 17}, 0)`}>
            <rect width="14" height="14" rx="3.5" fill="#292c32" stroke="#44474f" strokeWidth="0.5" />
            {label ? (
              <text x="7" y="9.5" fontSize="6.5" fontWeight="700" fill="#f5f5f5" textAnchor="middle">
                {label}
              </text>
            ) : (
              <rect x="4" y="5" width="6" height="4" rx="1" fill="#777d87" />
            )}
          </g>
        ))}
      </g>
    </svg>
  );
}

function Ticket({ compact }: { compact: boolean }) {
  return (
    <div
      className={clsx(
        'flex min-h-0 flex-col gap-1.5 bg-[#292c32] p-2',
        compact ? 'flex-1' : 'w-[118px] shrink-0 border-l border-white/10',
      )}
    >
      <Stepper label="Amount" value="₹500" />
      <Stepper label="Time" value="16:53" />

      <div className="flex items-baseline justify-between px-0.5">
        <span className="text-[7px] text-white/50">Earnings</span>
        <span className="text-[8px] font-bold text-brand">+84% ₹920</span>
      </div>

      <div className="grid grid-cols-2 gap-1.5">
        <span className="grid h-8 place-items-center rounded-lg bg-[#2e9962] text-sm font-black text-white">
          ↑
        </span>
        <span className="grid h-8 place-items-center rounded-lg bg-[#cc4747] text-sm font-black text-white">
          ↓
        </span>
      </div>

      <div className="mt-1 rounded-lg bg-[#1d1f23] p-1.5">
        <div className="flex items-center justify-between">
          <span className="rounded bg-up/20 px-1 py-px text-[6px] font-bold text-up">UP</span>
          <span className="text-[7px] font-bold tnum">₹500</span>
          <span className="text-[7px] font-bold tnum text-white/60">00:48</span>
        </div>
        <div className="mt-1 text-[6px] tnum text-white/45">144,503.60 → 148,965.44</div>
        <div className="mt-1 rounded bg-brand px-1 py-0.5 text-center text-[6.5px] font-bold text-[#1d1f23]">
          Sell now ₹762.31
        </div>
      </div>
    </div>
  );
}

function Stepper({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between rounded-lg bg-[#1d1f23] px-1.5 py-1">
      <span className="text-[9px] font-bold text-white/40">−</span>
      <span className="text-center">
        <span className="block text-[6px] text-white/45">{label}</span>
        <span className="block text-[9px] font-bold tnum">{value}</span>
      </span>
      <span className="text-[9px] font-bold text-white/40">+</span>
    </div>
  );
}
