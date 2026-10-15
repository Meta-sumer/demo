import { beforeEach, describe, expect, it } from 'vitest';
import {
  MAX_HISTORY,
  selectCanRedo,
  selectCanUndo,
  selectDrawings,
  useDrawingsStore,
  type ChartPoint,
} from '../src/stores/drawingsStore';

/**
 * Undo and redo of drawings, per asset.
 *
 * Adding, deleting and clearing are undoable; a fresh change after an undo
 * drops whatever could have been redone; one asset's history never touches
 * another's. The history is not saved.
 */
const level = (price: number): ChartPoint[] => [{ time: 0, price }];
const store = () => useDrawingsStore.getState();
const prices = (assetId: string) => selectDrawings(store(), assetId).map((drawing) => drawing.points[0]!.price);

describe('drawing undo and redo', () => {
  beforeEach(() => {
    useDrawingsStore.setState({ drawings: {}, armed: null, selectedId: null, history: {} });
  });

  it('has nothing to undo or redo before anything is drawn', () => {
    expect(selectCanUndo(store(), 'asset-1')).toBe(false);
    expect(selectCanRedo(store(), 'asset-1')).toBe(false);
    expect(selectCanUndo(store(), null)).toBe(false);
    store().undo('asset-1');
    expect(prices('asset-1')).toEqual([]);
  });

  it('undoes and redoes an add', () => {
    store().add('asset-1', 'hline', level(1));
    store().add('asset-1', 'hline', level(2));
    expect(prices('asset-1')).toEqual([1, 2]);

    store().undo('asset-1');
    expect(prices('asset-1')).toEqual([1]);
    expect(selectCanRedo(store(), 'asset-1')).toBe(true);

    store().redo('asset-1');
    expect(prices('asset-1')).toEqual([1, 2]);
    expect(selectCanRedo(store(), 'asset-1')).toBe(false);
  });

  it('undoes a delete and a clear', () => {
    const id = store().add('asset-1', 'hline', level(1))!;
    store().add('asset-1', 'hline', level(2));
    store().remove('asset-1', id);
    expect(prices('asset-1')).toEqual([2]);
    store().undo('asset-1');
    expect(prices('asset-1')).toEqual([1, 2]);

    store().clear('asset-1');
    expect(prices('asset-1')).toEqual([]);
    store().undo('asset-1');
    expect(prices('asset-1')).toEqual([1, 2]);
  });

  it('drops the redo trail on a new change', () => {
    store().add('asset-1', 'hline', level(1));
    store().add('asset-1', 'hline', level(2));
    store().undo('asset-1');
    store().add('asset-1', 'hline', level(3));

    expect(selectCanRedo(store(), 'asset-1')).toBe(false);
    expect(prices('asset-1')).toEqual([1, 3]);
  });

  it('falls back to the shared empty result when an undo empties the asset', () => {
    const empty = selectDrawings(store(), 'asset-1');
    store().add('asset-1', 'hline', level(1));
    store().undo('asset-1');

    expect(selectDrawings(store(), 'asset-1')).toBe(empty);
    expect(store().drawings['asset-1']).toBeUndefined();
  });

  it('lets go of a selection whose drawing an undo took away', () => {
    const id = store().add('asset-1', 'hline', level(1))!;
    expect(store().selectedId).toBe(id);
    store().undo('asset-1');

    expect(store().selectedId).toBeNull();
  });

  it('keeps each asset to its own history', () => {
    store().add('asset-1', 'hline', level(1));
    store().add('asset-2', 'hline', level(9));
    store().undo('asset-1');

    expect(prices('asset-1')).toEqual([]);
    expect(prices('asset-2')).toEqual([9]);
    expect(selectCanUndo(store(), 'asset-2')).toBe(true);
  });

  it('does not record a change that changed nothing', () => {
    store().remove('asset-1', 'missing');
    store().clear('asset-1');
    expect(selectCanUndo(store(), 'asset-1')).toBe(false);
  });

  it('reaches back a bounded number of steps', () => {
    for (let i = 0; i < MAX_HISTORY + 5; i += 1) store().add('asset-1', 'hline', level(i));
    expect(store().history['asset-1']!.undo).toHaveLength(MAX_HISTORY);
  });

  it('is not saved with the drawings', () => {
    store().add('asset-1', 'hline', level(1));
    const persist = (useDrawingsStore as unknown as { persist: { getOptions: () => { partialize: (s: unknown) => object } } })
      .persist;
    const saved = persist.getOptions().partialize(store());

    expect(Object.keys(saved)).toEqual(['drawings']);
  });
});
