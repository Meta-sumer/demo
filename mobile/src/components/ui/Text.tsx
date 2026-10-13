import { forwardRef } from 'react';
import { Text as RNText, type TextProps } from 'react-native';
import { cn } from '../../utils/cn';

/**
 * Text with the app's defaults.
 *
 * React Native does not inherit colour from a parent view the way the DOM
 * does, so every piece of text starts from the strongest content colour and
 * the body size, and a class passed in overrides either.
 *
 * `tnum` in the class list switches on tabular figures, as on the web: prices
 * change constantly and proportional digits make them jitter.
 */
export interface AppTextProps extends TextProps {
  className?: string;
}

export const Text = forwardRef<RNText, AppTextProps>(function Text({ className, style, ...props }, ref) {
  const tnum = !!className && /(^|\s)tnum(\s|$)/.test(className);
  return (
    <RNText
      ref={ref}
      maxFontSizeMultiplier={1.35}
      className={cn('text-sm text-content-100', className)}
      style={tnum ? [{ fontVariant: ['tabular-nums'] }, style] : style}
      {...props}
    />
  );
});
