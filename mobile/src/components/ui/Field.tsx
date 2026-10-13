import { forwardRef, useState, type ReactNode } from 'react';
import { TextInput, View, type TextInputProps } from 'react-native';
import { cn } from '../../utils/cn';
import { useTokenColor } from '../../utils/theme-colors';
import { Label } from './basics';
import { Text } from './Text';

/**
 * Text input, the web's `.field`.
 *
 * The accent border on focus is drawn from state rather than a `focus:`
 * class so it also wraps the adornments either side of the input.
 */
export interface FieldProps extends TextInputProps {
  label?: string;
  /** Under the input; shown in the error tone when `error` is set. */
  hint?: ReactNode;
  error?: string | null;
  left?: ReactNode;
  right?: ReactNode;
  className?: string;
  inputClassName?: string;
}

export const Field = forwardRef<TextInput, FieldProps>(function Field(
  { label, hint, error, left, right, className, inputClassName, onFocus, onBlur, editable = true, ...props },
  ref,
) {
  const [focused, setFocused] = useState(false);
  const placeholderColor = useTokenColor('content-400');
  const selectionColor = useTokenColor('accent');

  return (
    <View className={className}>
      {label ? <Label>{label}</Label> : null}
      <View
        className={cn(
          'min-h-[44px] flex-row items-center rounded-lg border bg-surface-850 px-3',
          error ? 'border-down/70' : focused ? 'border-accent' : 'border-surface-700',
          !editable && 'opacity-60',
        )}
      >
        {left ? <View className="mr-2">{left}</View> : null}
        <TextInput
          ref={ref}
          editable={editable}
          placeholderTextColor={placeholderColor}
          selectionColor={selectionColor}
          className={cn('flex-1 py-2.5 text-sm text-content-100', inputClassName)}
          onFocus={(event) => {
            setFocused(true);
            onFocus?.(event);
          }}
          onBlur={(event) => {
            setFocused(false);
            onBlur?.(event);
          }}
          {...props}
        />
        {right ? <View className="ml-2">{right}</View> : null}
      </View>
      {error ? (
        <Text className="mt-1 text-xs text-down">{error}</Text>
      ) : hint ? (
        typeof hint === 'string' ? <Text className="mt-1 text-xs text-content-400">{hint}</Text> : hint
      ) : null}
    </View>
  );
});
