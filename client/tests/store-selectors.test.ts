import { beforeEach, describe, expect, it } from 'vitest';
import {
  MAX_DRAWINGS_PER_ASSET,
  migrateDrawings,
  selectDrawings,
  useDrawingsStore,
  type ChartPoint,
} from '../src/stores/drawingsStore';

/**
 * Selector reference stability.
 *
 * Zustand compares a selector's result with Object.is. A selector that builds
 * its fallback inline - `state.drawings[id] ?? []` - returns a new array on
 * every call, so the store reports a change, the component re-renders, the
 * selector runs again, and React eventually throws "Maximum update depth
 * exceeded".
 *
 * That shipped once and took the whole trade page down. These tests assert the
 * property that prevents it, so a future selector rewrite cannot reintroduce it
 * without failing the build.
 */
const level = (price: number): ChartPoint[] => [{ time: 0, price }];

describe('selectDrawings', () => {
  beforeEach(() => {
    useDrawingsStore.setState({ drawings: {}, armed: null, selectedId: null });
  });

  it('returns the same reference twice for an asset with no drawings', () => {
    const state = useDrawingsStore.getState();

    expect(selectDrawings(state, 'asset-1')).toBe(selectDrawings(state, 'asset-1'));
  });

  it('returns the same reference for two different empty assets', () => {
    const state = useDrawingsStore.getState();

    // Any fresh array here would be a new reference per asset and per call.
    expect(selectDrawings(state, 'asset-1')).toBe(selectDrawings(state, 'asset-2'));
  });

  it('returns the same reference for a null asset', () => {
    const state = useDrawingsStore.getState();

    expect(selectDrawings(state, null)).toBe(selectDrawings(state, 'asset-1'));
  });

  it('stays stable across unrelated store updates', () => {
    const before = selectDrawings(useDrawingsStore.getState(), 'asset-1');
    useDrawingsStore.getState().arm('trend', 'asset-1');
    useDrawingsStore.getState().select('nothing');
    const after = selectDrawings(useDrawingsStore.getState(), 'asset-1');

    expect(after).toBe(before);
  });

  it('hands back the stored array itself once drawings exist', () => {
    useDrawingsStore.getState().add('asset-1', 'hline', level(100));
    const state = useDrawingsStore.getState();

    expect(selectDrawings(state, 'asset-1')).toBe(state.drawings['asset-1']);
    expect(selectDrawings(state, 'asset-1')).toHaveLength(1);
  });

  it('does not leak drawings between assets', () => {
    useDrawingsStore.getState().add('asset-1', 'hline', level(100));
    const state = useDrawingsStore.getState();

    expect(selectDrawings(state, 'asset-2')).toHaveLength(0);
  });

  it('is a new reference only when that asset actually changed', () => {
    useDrawingsStore.getState().add('asset-1', 'hline', level(100));
    const first = selectDrawings(useDrawingsStore.getState(), 'asset-1');

    useDrawingsStore.getState().add('asset-2', 'hline', level(200));
    const afterOtherAsset = selectDrawings(useDrawingsStore.getState(), 'asset-1');
    expect(afterOtherAsset).toBe(first);

    useDrawingsStore.getState().add('asset-1', 'hline', level(300));
    expect(selectDrawings(useDrawingsStore.getState(), 'asset-1')).not.toBe(first);
  });

  it('returns the shared empty reference again after the last drawing is cleared', () => {
    const empty = selectDrawings(useDrawingsStore.getState(), 'asset-1');
    useDrawingsStore.getState().add('asset-1', 'hline', level(100));
    useDrawingsStore.getState().clear('asset-1');

    expect(selectDrawings(useDrawingsStore.getState(), 'asset-1')).toBe(empty);
  });

  it('returns the shared empty reference after the last drawing is removed one by one', () => {
    const empty = selectDrawings(useDrawingsStore.getState(), 'asset-1');
    const id = useDrawingsStore.getState().add('asset-1', 'hline', level(100))!;
    useDrawingsStore.getState().remove('asset-1', id);

    expect(selectDrawings(useDrawingsStore.getState(), 'asset-1')).toBe(empty);
  });

  it('refuses mutation of the shared empty result', () => {
    const empty = selectDrawings(useDrawingsStore.getState(), 'asset-1') as { push: (value: unknown) => number };

    // Frozen, so a caller cannot corrupt the fallback every other asset shares.
    expect(() => empty.push({ id: 'x', kind: 'hline', points: [] })).toThrow();
  });

  it('caps the drawings it keeps for one asset, dropping the oldest', () => {
    for (let i = 0; i < MAX_DRAWINGS_PER_ASSET + 10; i += 1) useDrawingsStore.getState().add('asset-1', 'hline', level(i));
    const kept = selectDrawings(useDrawingsStore.getState(), 'asset-1');

    expect(kept).toHaveLength(MAX_DRAWINGS_PER_ASSET);
    expect(kept[0]!.points[0]!.price).toBe(10);
  });
});

describe('drawing lifecycle', () => {
  beforeEach(() => {
    useDrawingsStore.setState({ drawings: {}, armed: null, selectedId: null });
  });

  it('puts the tool down and selects what was just drawn', () => {
    useDrawingsStore.getState().arm('trend', 'asset-1');
    const id = useDrawingsStore.getState().add('asset-1', 'trend', [
      { time: 1000, price: 1 },
      { time: 2000, price: 2 },
    ]);

    const state = useDrawingsStore.getState();
    expect(state.armed).toBeNull();
    expect(state.selectedId).toBe(id);
  });

  it('refuses a two-point drawing with one point, or a point that is not a number', () => {
    expect(useDrawingsStore.getState().add('asset-1', 'rect', [{ time: 1, price: 1 }])).toBeNull();
    expect(
      useDrawingsStore.getState().add('asset-1', 'fib', [
        { time: 1, price: 1 },
        { time: Number.NaN, price: 2 },
      ]),
    ).toBeNull();
    expect(selectDrawings(useDrawingsStore.getState(), 'asset-1')).toHaveLength(0);
  });

  it('forgets the selection when the selected drawing goes', () => {
    const id = useDrawingsStore.getState().add('asset-1', 'vline', [{ time: 5, price: 0 }])!;
    useDrawingsStore.getState().remove('asset-1', id);

    expect(useDrawingsStore.getState().selectedId).toBeNull();
  });

  it('keeps a selection on another asset when one asset is cleared', () => {
    const keep = useDrawingsStore.getState().add('asset-2', 'hline', level(1))!;
    useDrawingsStore.getState().add('asset-1', 'hline', level(2));
    useDrawingsStore.getState().select(keep);
    useDrawingsStore.getState().clear('asset-1');

    expect(useDrawingsStore.getState().selectedId).toBe(keep);
  });

  it('arming a tool drops the current selection', () => {
    const id = useDrawingsStore.getState().add('asset-1', 'hline', level(1))!;
    expect(useDrawingsStore.getState().selectedId).toBe(id);

    useDrawingsStore.getState().arm('ray', 'asset-1');
    expect(useDrawingsStore.getState().selectedId).toBeNull();
    expect(useDrawingsStore.getState().armed).toEqual({ kind: 'ray', assetId: 'asset-1' });
  });
});

describe('migrateDrawings', () => {
  it('turns the old stored price levels into horizontal lines, keeping their ids', () => {
    const migrated = migrateDrawings(
      {
        levels: {
          'asset-1': [
            { id: 'a', price: 1.2345 },
            { id: 'b', price: 1.3 },
          ],
          'asset-2': [{ id: 'c', price: 99 }],
        },
      },
      0,
    );

    expect(migrated.drawings['asset-1']).toEqual([
      { id: 'a', kind: 'hline', points: [{ time: 0, price: 1.2345 }] },
      { id: 'b', kind: 'hline', points: [{ time: 0, price: 1.3 }] },
    ]);
    expect(migrated.drawings['asset-2']).toHaveLength(1);
  });

  it('drops anything unreadable instead of drawing it at a nonsense price', () => {
    const migrated = migrateDrawings(
      { levels: { 'asset-1': [{ id: 'a', price: 'x' }, null, { price: Number.NaN }], 'asset-2': 'junk' } },
      0,
    );

    expect(migrated.drawings).toEqual({});
  });

  it('survives nothing having been stored', () => {
    expect(migrateDrawings(undefined, 0)).toEqual({ drawings: {} });
  });

  it('passes the current shape through untouched', () => {
    const drawings = { 'asset-1': [{ id: 'x', kind: 'vline' as const, points: [{ time: 5, price: 0 }] }] };

    expect(migrateDrawings({ drawings }, 1)).toEqual({ drawings });
  });
});
