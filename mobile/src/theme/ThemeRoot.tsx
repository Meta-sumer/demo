import { useEffect, useMemo, type ReactNode } from 'react';
import { View } from 'react-native';
import { StatusBar } from 'expo-status-bar';
import * as SystemUI from 'expo-system-ui';
import { vars } from 'nativewind';
import { useThemeStore, watchSystemTheme } from '../stores/themeStore';
import { THEME_TOKENS, TOKEN_NAMES, tokenColor, type ResolvedTheme } from './tokens';

/**
 * Sets the palette for everything below it.
 *
 * NativeWind variables cascade through React context rather than the native
 * view tree, so sheets and modals rendered from inside the app pick them up
 * too. The status bar and the window background follow the theme so a light
 * screen never sits under a dark system bar.
 */
const VARIABLES: Record<ResolvedTheme, ReturnType<typeof vars>> = Object.fromEntries(
  (Object.keys(THEME_TOKENS) as ResolvedTheme[]).map((theme) => [
    theme,
    vars(Object.fromEntries(TOKEN_NAMES.map((name) => ['--' + name, THEME_TOKENS[theme][name]]))),
  ]),
) as Record<ResolvedTheme, ReturnType<typeof vars>>;

export function ThemeRoot({ children }: { children: ReactNode }) {
  const resolved = useThemeStore((s) => s.resolved);
  const background = useMemo(() => tokenColor(resolved, 'surface-950'), [resolved]);

  useEffect(() => watchSystemTheme(), []);

  useEffect(() => {
    SystemUI.setBackgroundColorAsync(background).catch(() => undefined);
  }, [background]);

  return (
    <View style={[VARIABLES[resolved], { flex: 1, backgroundColor: background }]}>
      <StatusBar style={resolved === 'light' ? 'dark' : 'light'} />
      {children}
    </View>
  );
}
