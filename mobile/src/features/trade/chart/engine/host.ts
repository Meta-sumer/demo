import type { SkPicture } from '@shopify/react-native-skia';
import type { SharedValue } from 'react-native-reanimated';
import type { ChartEngine } from './useChartEngine';

/**
 * What a layer drawn over the price chart is handed.
 *
 * Each layer (the drawings, the expiry guide, the orders) records its own
 * Skia picture on the UI thread and hands it to the chart, which draws every
 * layer into one canvas in a fixed order - so a pan moves all of them in the
 * same frame. Taps arrive the same way: the chart owns the one gesture
 * surface and offers each tap to the layers, highest priority first, before
 * it falls through to the crosshair.
 */
export type LayerKey = 'drawings' | 'guide' | 'orders';

/** Returns true when the layer used the tap. Coordinates are the canvas's. */
export type TapHandler = (x: number, y: number) => boolean;

export interface ChartHost {
  engine: ChartEngine;
  setLayer: (key: LayerKey, picture: SharedValue<SkPicture> | null) => void;
  setTap: (key: string, priority: number, handler: TapHandler | null) => void;
  /** Something to let go of when a drag or a pinch starts. */
  setPanBegin: (key: string, handler: (() => void) | null) => void;
}

/** Tap priorities: the order markers sit above the drawings, which sit above the crosshair. */
export const TAP_PRIORITY = { orders: 30, drawings: 10 } as const;
