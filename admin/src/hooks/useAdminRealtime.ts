import { useEffect } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { channels, socket, WsEvents } from '../websocket/client';
import { useAuthStore } from '../stores/authStore';
import { useRiskStore } from '../stores/riskStore';
import { isUsdtEvent, useUsdtLiveStore } from './useUsdtLive';
import type { ExposureResponse, RiskAssessment } from '../types/api';

/**
 * Subscribes the console to the aggregated admin channels.
 *
 * The server rejects any channel the signed-in role is not entitled to, so
 * subscribing optimistically here is safe.
 */
export function useAdminRealtime(): void {
  const status = useAuthStore((s) => s.status);
  const queryClient = useQueryClient();

  useEffect(() => {
    if (status !== 'authenticated') return;

    socket.connect();
    socket.subscribe([channels.adminRisk, channels.adminOps, channels.adminMarket, channels.adminSupport]);

    const unsubscribers = [
      socket.on(WsEvents.EXPOSURE_UPDATED, (payload) => {
        useRiskStore.getState().applyExposureUpdate(payload as ExposureResponse['assets'][number]);
      }),
      socket.on(WsEvents.RISK_UPDATED, (payload) => {
        useRiskStore.getState().setAssessments(payload as RiskAssessment[]);
      }),
      socket.on(WsEvents.HEDGE_CREATED, () => {
        void queryClient.invalidateQueries({ queryKey: ['hedging'] });
      }),
      socket.on(WsEvents.HEDGE_UPDATED, () => {
        void queryClient.invalidateQueries({ queryKey: ['hedging'] });
      }),
      // The inbox and the nav badge refresh on any support activity; the open
      // thread listens for itself on the support page.
      socket.on(WsEvents.SUPPORT_CONVERSATION, () => {
        void queryClient.invalidateQueries({ queryKey: ['support'] });
      }),
      socket.on(WsEvents.TRADE_CREATED, () => {
        void queryClient.invalidateQueries({ queryKey: ['admin', 'trades'] });
      }),
      socket.on(WsEvents.TRADE_SETTLED, () => {
        void queryClient.invalidateQueries({ queryKey: ['admin', 'trades'] });
        void queryClient.invalidateQueries({ queryKey: ['dashboard'] });
      }),
      // USDT deposits, withdrawals and webhooks change from outside the console;
      // any usdt.* event on admin:ops refreshes every USDT screen and the nav badge.
      socket.on('*', (_payload, event) => {
        if (!isUsdtEvent(event)) return;
        useUsdtLiveStore.getState().mark();
        void queryClient.invalidateQueries({ queryKey: ['usdt'] });
      }),
    ];

    return () => {
      for (const off of unsubscribers) off();
      socket.disconnect();
    };
  }, [status, queryClient]);
}
