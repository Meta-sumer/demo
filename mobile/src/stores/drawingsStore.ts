import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { randomUUID } from 'expo-crypto';
import { persistStorage } from './storage';

/**
 * Chart drawings.
 *
 * What a trader marks by hand on the advanced chart: trend lines, rays,
 * horizontal and vertical lines, rectangles and Fibonacci retracements. They
 * are local to the browser and carry no meaning to the server - nothing here
 * influences an order, a payout or a settlement.
 *
 * Every point is stored in the chart's own terms, an instant and a price, and
 * never in pixels. That is what keeps a drawing on the candles it was drawn
 * against through a pan, a zoom, a resize or a change of timeframe: the layer
 * projects it afresh every frame, the same way the order overlay projects a
 * position.
 */
export type DrawingKind = 'trend' | 'ray' | 'hline' | 'vline' | 'rect' | 'fib';

export interface ChartPoint {
  /** Epoch milliseconds. Ignored by a horizontal line. */
  time: number;
  /** Ignored by a vertical line. */
  price: number;
}

export interface Drawing {
  id: string;
  kind: DrawingKind;
  /** One point for the horizontal and vertical lines, two for everything else. */
  points: ChartPoint[];
}

/** How many clicks each tool takes. */
export const POINTS_NEEDED: Record<DrawingKind, 1 | 2> = {
  trend: 2,
  ray: 2,
  hline: 1,
  vline: 1,
  rect: 2,
  fib: 2,
};

/**
 * The tool waiting for clicks, and the chart it belongs to.
 *
 * The asset is part of it because two charts can be on screen at once in the
 * advanced workspace. A tool picked from one chart's toolbar must not turn the
 * other chart's next click into a drawing.
 */
export interface ArmedTool {
  kind: DrawingKind;
  assetId: string;
}

interface DrawingsState {
  /** Keyed by asset id so drawings stay with the instrument they were drawn on. */
  drawings: Record<string, Drawing[]>;
  armed: ArmedTool | null;
  selectedId: string | null;

  arm: (kind: DrawingKind, assetId: string) => void;
  disarm: () => void;
  /** Stores a finished drawing, selects it and puts the tool down. Returns its id. */
  add: (assetId: string, kind: DrawingKind, points: ChartPoint[]) => string | null;
  remove: (assetId: string, id: string) => void;
  clear: (assetId: string) => void;
  select: (id: string | null) => void;

  /**
   * Per-asset undo and redo of what the trader drew. Adding, deleting and
   * clearing can be undone; a new change after an undo drops the redo trail,
   * the way every editor does. Kept for the session only - it is not saved.
   */
  history: Record<string, DrawingHistory>;
  undo: (assetId: string) => void;
  redo: (assetId: string) => void;
}

/** The asset's drawing list before each undoable change, and after each undo. */
export interface DrawingHistory {
  undo: Drawing[][];
  redo: Drawing[][];
}

/** How many steps back an asset's history reaches. */
export const MAX_HISTORY = 50;

export const MAX_DRAWINGS_PER_ASSET = 50;

/**
 * Shared empty result.
 *
 * A selector that builds `[]` inline hands back a new reference on every
 * call. Zustand compares with Object.is, reads that as a change, re-renders,
 * runs the selector again - and the render loop never settles. One frozen
 * array keeps the identity stable for every asset that has no drawings yet.
 */
const NO_DRAWINGS: readonly Drawing[] = Object.freeze([]);

export const useDrawingsStore = create<DrawingsState>()(
  persist(
    (set) => ({
      drawings: {},
      armed: null,
      selectedId: null,
      history: {},

      arm: (kind, assetId) => set({ armed: { kind, assetId }, selectedId: null }),
      disarm: () => set({ armed: null }),

      add: (assetId, kind, points) => {
        if (!isComplete(kind, points)) return null;
        const drawing: Drawing = {
          id: randomUUID(),
          kind,
          points: points.slice(0, POINTS_NEEDED[kind]).map((point) => ({ time: point.time, price: point.price })),
        };
        set((state) => ({
          // Oldest drawings fall off rather than letting the chart fill up.
          ...change(state, assetId, [...(state.drawings[assetId] ?? []), drawing].slice(-MAX_DRAWINGS_PER_ASSET)),
          armed: null,
          selectedId: drawing.id,
        }));
        return drawing.id;
      },

      remove: (assetId, id) =>
        set((state) => {
          const current = state.drawings[assetId] ?? [];
          if (!current.some((drawing) => drawing.id === id)) return {};
          return {
            ...change(
              state,
              assetId,
              current.filter((drawing) => drawing.id !== id),
            ),
            selectedId: state.selectedId === id ? null : state.selectedId,
          };
        }),

      clear: (assetId) =>
        set((state) => {
          const current = state.drawings[assetId] ?? [];
          if (current.length === 0) return {};
          const cleared = new Set(current.map((drawing) => drawing.id));
          return {
            ...change(state, assetId, []),
            selectedId: state.selectedId && cleared.has(state.selectedId) ? null : state.selectedId,
          };
        }),

      select: (selectedId) => set({ selectedId }),

      undo: (assetId) =>
        set((state) => {
          const trail = state.history[assetId];
          if (!trail || trail.undo.length === 0) return {};
          const previous = trail.undo[trail.undo.length - 1]!;
          const current = state.drawings[assetId] ?? [];
          return {
            drawings: withList(state.drawings, assetId, previous),
            history: {
              ...state.history,
              [assetId]: { undo: trail.undo.slice(0, -1), redo: [...trail.redo, current].slice(-MAX_HISTORY) },
            },
            selectedId: keepSelection(state.selectedId, current, previous),
          };
        }),

      redo: (assetId) =>
        set((state) => {
          const trail = state.history[assetId];
          if (!trail || trail.redo.length === 0) return {};
          const next = trail.redo[trail.redo.length - 1]!;
          const current = state.drawings[assetId] ?? [];
          return {
            drawings: withList(state.drawings, assetId, next),
            history: {
              ...state.history,
              [assetId]: { undo: [...trail.undo, current].slice(-MAX_HISTORY), redo: trail.redo.slice(0, -1) },
            },
            selectedId: keepSelection(state.selectedId, current, next),
          };
        }),
    }),
    {
      name: 'bt.drawings',
      storage: persistStorage,
      version: 1,
      // The armed tool and the selection are moments in an interaction, not
      // preferences worth restoring.
      partialize: (state) => ({ drawings: state.drawings }),
      migrate: (persisted, from) => migrateDrawings(persisted, from) as unknown as DrawingsState,
    },
  ),
);

/**
 * Brings stored drawings forward.
 *
 * Version 0 kept only horizontal price levels, as `levels: { [assetId]:
 * [{ id, price }] }`. Each becomes a horizontal line with its id kept, so a
 * level someone drew last week is still there - now selectable and deletable
 * like everything else. Anything unreadable is dropped rather than drawn at a
 * nonsense price.
 */
export function migrateDrawings(persisted: unknown, from: number): { drawings: Record<string, Drawing[]> } {
  const state = (persisted ?? {}) as { levels?: unknown; drawings?: unknown };

  if (from < 1) {
    const drawings: Record<string, Drawing[]> = {};
    const levels = state.levels && typeof state.levels === 'object' ? (state.levels as Record<string, unknown>) : {};
    for (const [assetId, list] of Object.entries(levels)) {
      if (!Array.isArray(list)) continue;
      const lines: Drawing[] = [];
      for (const level of list as Array<{ id?: unknown; price?: unknown }>) {
        if (typeof level?.price !== 'number' || !Number.isFinite(level.price)) continue;
        lines.push({
          id: typeof level.id === 'string' ? level.id : randomUUID(),
          kind: 'hline',
          points: [{ time: 0, price: level.price }],
        });
      }
      if (lines.length > 0) drawings[assetId] = lines.slice(-MAX_DRAWINGS_PER_ASSET);
    }
    return { drawings };
  }

  return { drawings: (state.drawings as Record<string, Drawing[]> | undefined) ?? {} };
}

/**
 * Removing an asset's last drawing drops its key, so the asset falls back to
 * the shared empty result instead of an empty array of its own.
 */
function withList(drawings: Record<string, Drawing[]>, assetId: string, list: Drawing[]): Record<string, Drawing[]> {
  const next = { ...drawings };
  if (list.length > 0) next[assetId] = list;
  else delete next[assetId];
  return next;
}

/** An undoable change: the new list, with the old one kept to go back to and the redo trail dropped. */
function change(
  state: Pick<DrawingsState, 'drawings' | 'history'>,
  assetId: string,
  list: Drawing[],
): Pick<DrawingsState, 'drawings' | 'history'> {
  const trail = state.history[assetId];
  const before = state.drawings[assetId] ?? [];
  return {
    drawings: withList(state.drawings, assetId, list),
    history: { ...state.history, [assetId]: { undo: [...(trail?.undo ?? []), before].slice(-MAX_HISTORY), redo: [] } },
  };
}

/** A selection survives an undo or redo only while the drawing it points at is still there. */
function keepSelection(selectedId: string | null, before: Drawing[], after: Drawing[]): string | null {
  if (!selectedId) return null;
  // A selection on another asset is not this list's to drop.
  if (!before.some((drawing) => drawing.id === selectedId)) return selectedId;
  return after.some((drawing) => drawing.id === selectedId) ? selectedId : null;
}

/** Whether the asset has a change to undo. */
export function selectCanUndo(state: Pick<DrawingsState, 'history'>, assetId: string | null): boolean {
  return !!assetId && (state.history[assetId]?.undo.length ?? 0) > 0;
}

/** Whether the asset has an undone change to redo. */
export function selectCanRedo(state: Pick<DrawingsState, 'history'>, assetId: string | null): boolean {
  return !!assetId && (state.history[assetId]?.redo.length ?? 0) > 0;
}

function isComplete(kind: DrawingKind, points: ChartPoint[]): boolean {
  if (points.length < POINTS_NEEDED[kind]) return false;
  return points
    .slice(0, POINTS_NEEDED[kind])
    .every((point) => Number.isFinite(point.time) && Number.isFinite(point.price));
}

/**
 * Drawings for one asset, with a referentially stable result.
 *
 * Always use this rather than indexing `drawings` in a selector - see
 * NO_DRAWINGS.
 */
export function selectDrawings(state: Pick<DrawingsState, 'drawings'>, assetId: string | null): readonly Drawing[] {
  if (!assetId) return NO_DRAWINGS;
  return state.drawings[assetId] ?? NO_DRAWINGS;
}
