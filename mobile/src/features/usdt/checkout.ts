import { useCallback } from 'react';
import { router } from 'expo-router';
import * as Linking from 'expo-linking';
import * as WebBrowser from 'expo-web-browser';
import { useQueryClient } from '@tanstack/react-query';
import { randomUUID } from 'expo-crypto';
import { toast } from '../../hooks/useToast';
import { createAttemptKey } from './model';
import { USDT_KEY } from './queries';

/**
 * `createAttemptKey` with the device's own random source.
 *
 * The default generator reads `globalThis.crypto`, which React Native does
 * not provide; expo-crypto does the same job natively.
 */
export function createDeviceAttemptKey() {
  return createAttemptKey(() => randomUUID());
}

/**
 * Opens the payment provider's hosted checkout for a deposit.
 *
 * On the web the checkout replaced the page and the provider sent the
 * customer back to the status page. The provider's return address points at
 * the web client, so the app does not rely on it: the checkout opens in the
 * in-app browser and, once that hands control back, the deposit is fetched
 * again and its status screen is shown - the same screen the web's redirect
 * lands on, asking the server rather than trusting the return.
 *
 * iOS hands control back when the browser is closed; Android as soon as the
 * browser has opened, so there the status screen is already waiting when the
 * customer comes back (it checks again when the app returns to the front).
 *
 * `onLeave` runs before the status screen is pushed - a sheet showing the
 * deposit has to be closed first, or it would stay drawn over the new screen.
 * `showStatus: false` is for the status screen itself.
 */
export function useOpenCheckout() {
  const queryClient = useQueryClient();

  return useCallback(
    async (
      depositId: string,
      url: string,
      options: { onLeave?: () => void; onReturn?: () => void; showStatus?: boolean } = {},
    ) => {
      try {
        await WebBrowser.openBrowserAsync(url);
      } catch {
        // No in-app browser (an Android device without Custom Tabs support):
        // hand the address to the system instead.
        try {
          await Linking.openURL(url);
        } catch {
          toast('error', 'Could not open the payment page', url);
          return;
        }
      }

      // Everything under `usdt` - the deposit itself, the balances, the lists.
      void queryClient.invalidateQueries({ queryKey: USDT_KEY });
      options.onReturn?.();
      if (options.showStatus === false) return;
      options.onLeave?.();
      router.push({ pathname: '/wallet/usdt/success', params: { deposit: depositId } });
    },
    [queryClient],
  );
}
