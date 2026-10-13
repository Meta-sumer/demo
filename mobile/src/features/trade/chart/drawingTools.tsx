import Svg, { Circle, Path, Rect } from 'react-native-svg';
import type { DrawingKind } from '../../../stores/drawingsStore';

/**
 * The drawing tools, as the toolbar and the drawing layer describe them.
 *
 * `steps` is what the hint above the chart says while the tool waits for its
 * clicks: one line per click still needed. The words say "click"; the layer
 * swaps in "tap" for a finger - which on a phone is every time.
 */
export interface DrawingToolInfo {
  kind: DrawingKind;
  name: string;
  /** Short enough for a tile. */
  short: string;
  steps: [string] | [string, string];
}

export const DRAWING_TOOLS: DrawingToolInfo[] = [
  { kind: 'trend', name: 'Trend line', short: 'Trend', steps: ['Click where the line starts', 'Now click where it ends'] },
  { kind: 'ray', name: 'Ray', short: 'Ray', steps: ['Click where the ray starts', 'Now click a point it passes through'] },
  { kind: 'hline', name: 'Horizontal line', short: 'Horizontal', steps: ['Click the price to mark'] },
  { kind: 'vline', name: 'Vertical line', short: 'Vertical', steps: ['Click the moment to mark'] },
  { kind: 'rect', name: 'Rectangle', short: 'Rectangle', steps: ['Click one corner', 'Now click the opposite corner'] },
  {
    kind: 'fib',
    name: 'Fibonacci retracement',
    short: 'Fibonacci',
    steps: ['Click where the move starts', 'Now click where the move ends'],
  },
];

export function toolInfo(kind: DrawingKind): DrawingToolInfo {
  return DRAWING_TOOLS.find((tool) => tool.kind === kind) ?? DRAWING_TOOLS[0]!;
}

/**
 * The tool's glyph.
 *
 * Drawn here rather than taken from the icon set, which has nothing for a
 * ray or a retracement: each shows the shape the tool makes, with dots where
 * the clicks go - the convention every charting package uses.
 */
export function DrawingIcon({ kind, size = 16, color }: { kind: DrawingKind; size?: number; color: string }) {
  const stroke = { stroke: color, strokeWidth: 1.5, strokeLinecap: 'round' as const, fill: 'none' };
  const dot = (cx: number, cy: number) => <Circle cx={cx} cy={cy} r={1.7} fill={color} />;

  const body = (() => {
    switch (kind) {
      case 'trend':
        return (
          <>
            <Path d="M3.5 12.5 12.5 3.5" {...stroke} />
            {dot(3.5, 12.5)}
            {dot(12.5, 3.5)}
          </>
        );
      case 'ray':
        return (
          <>
            <Path d="M3 13 14.5 1.5" {...stroke} />
            {dot(3, 13)}
            {dot(8, 8)}
          </>
        );
      case 'hline':
        return (
          <>
            <Path d="M1.5 8h13" {...stroke} />
            {dot(8, 8)}
          </>
        );
      case 'vline':
        return (
          <>
            <Path d="M8 1.5v13" {...stroke} />
            {dot(8, 8)}
          </>
        );
      case 'rect':
        return (
          <>
            <Rect x="3" y="4" width="10" height="8" rx="0.5" {...stroke} />
            {dot(3, 12)}
            {dot(13, 4)}
          </>
        );
      case 'fib':
        return (
          <>
            <Path d="M2 3h12M2 6.5h9M2 9.5h9M2 13h12" {...stroke} />
            <Path d="M3 13 13 3" {...stroke} strokeDasharray="1.5 2" strokeWidth={1} />
          </>
        );
      default:
        return null;
    }
  })();

  return (
    <Svg width={size} height={size} viewBox="0 0 16 16" accessibilityElementsHidden importantForAccessibility="no">
      {body}
    </Svg>
  );
}
