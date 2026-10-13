import { create } from 'zustand';
import type { ExposureResponse, HedgeMonitorRow, RiskAssessment } from '../types/api';

/**
 * Live risk state fed by the websocket, so the exposure and hedging screens
 * update without polling (spec sections 21 and 22).
 */
interface RiskState {
  exposure: ExposureResponse | null;
  assessments: Record<string, RiskAssessment>;
  hedgeRows: HedgeMonitorRow[];
  lastUpdatedAt: number;

  setExposure: (exposure: ExposureResponse) => void;
  applyExposureUpdate: (snapshot: ExposureResponse['assets'][number]) => void;
  setAssessments: (assessments: RiskAssessment[]) => void;
  setHedgeRows: (rows: HedgeMonitorRow[]) => void;
}

export const useRiskStore = create<RiskState>((set) => ({
  exposure: null,
  assessments: {},
  hedgeRows: [],
  lastUpdatedAt: 0,

  setExposure: (exposure) => set({ exposure, lastUpdatedAt: Date.now() }),

  applyExposureUpdate(snapshot) {
    set((state) => {
      if (!state.exposure) return state;
      const assets = state.exposure.assets.map((asset) =>
        asset.assetId === snapshot.assetId ? { ...asset, ...snapshot } : asset,
      );
      const totals = assets.reduce(
        (acc, asset) => ({
          grossExposure: acc.grossExposure + asset.grossExposure,
          netExposure: acc.netExposure + asset.netExposure,
          potentialLiability: acc.potentialLiability + asset.potentialLiability,
          openTrades: acc.openTrades + asset.openTrades,
        }),
        { grossExposure: 0, netExposure: 0, potentialLiability: 0, openTrades: 0 },
      );
      return { exposure: { totals, assets }, lastUpdatedAt: Date.now() };
    });
  },

  setAssessments(assessments) {
    set((state) => ({
      assessments: { ...state.assessments, ...Object.fromEntries(assessments.map((a) => [a.assetId, a])) },
      lastUpdatedAt: Date.now(),
    }));
  },

  setHedgeRows: (hedgeRows) => set({ hedgeRows, lastUpdatedAt: Date.now() }),
}));
