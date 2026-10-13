import { useCallback, useState } from 'react';
import { useFocusEffect } from 'expo-router';

/**
 * Whether this screen is the one on display.
 *
 * The web unmounts a page when the customer leaves it, which is what stops
 * its polls. Tab screens stay mounted for the whole session, so a screen that
 * polls asks this instead and pauses while another tab is in front.
 */
export function useScreenFocused(): boolean {
  const [focused, setFocused] = useState(true);
  useFocusEffect(
    useCallback(() => {
      setFocused(true);
      return () => setFocused(false);
    }, []),
  );
  return focused;
}
