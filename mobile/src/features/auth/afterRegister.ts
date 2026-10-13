import { router } from 'expo-router';
import { useOnboardingStore } from '../../stores/onboardingStore';

/**
 * Where a brand-new account goes once the server has created it.
 *
 * A new account gets the five-step introduction once, ending in demo. The web
 * sends every registration to it; the onboarding flag is per device (see
 * onboardingStore), and it is read here - and only here - to skip it for a
 * phone that has already been through it.
 *
 * Signing in itself is not a navigation: the root layout's auth gate moves the
 * session into the app as soon as it becomes authenticated, and /onboarding is
 * only a route once it has. So the hop waits a tick for that render to land
 * instead of racing it - sent any earlier, it would be dropped as a route the
 * navigator does not have yet.
 */
export function continueAfterRegistration(): void {
  if (useOnboardingStore.getState().completed) return;
  setTimeout(() => router.replace('/onboarding'), 0);
}
