import type { ComponentType, ReactNode } from 'react';
import { ActivityIndicator, Pressable, type PressableProps, View } from 'react-native';
import { cn } from '../../utils/cn';
import { useTokenColor } from '../../utils/theme-colors';
import { Icon, type IconGlyphProps } from './Icon';
import { Text } from './Text';

/**
 * Buttons.
 *
 * The web client's `.btn-*` classes as one component. Filled buttons keep the
 * split the palette was built around: `*-solid` backgrounds, which white or
 * near-black text clears at button size, never the vibrant text colours.
 */
export type ButtonVariant = 'primary' | 'ghost' | 'danger' | 'up' | 'down' | 'subtle' | 'link';
export type ButtonSize = 'sm' | 'md' | 'lg';

const CONTAINER: Record<ButtonVariant, string> = {
  primary: 'bg-accent-solid',
  ghost: 'border border-surface-700 bg-surface-850',
  danger: 'bg-down-solid',
  up: 'bg-up-solid',
  down: 'bg-down-solid',
  subtle: 'bg-surface-700',
  link: 'bg-transparent px-0',
};

const LABEL: Record<ButtonVariant, string> = {
  primary: 'text-surface-950',
  ghost: 'text-content-200',
  danger: 'text-white',
  up: 'text-white',
  down: 'text-white',
  subtle: 'text-content-100',
  link: 'text-accent',
};

const SIZE: Record<ButtonSize, { box: string; text: string; icon: number }> = {
  sm: { box: 'min-h-[34px] rounded-lg px-3 py-1.5', text: 'text-xs', icon: 14 },
  md: { box: 'min-h-[42px] rounded-lg px-4 py-2', text: 'text-sm', icon: 16 },
  lg: { box: 'min-h-[52px] rounded-xl px-5 py-3', text: 'text-base', icon: 18 },
};

export interface ButtonProps extends Omit<PressableProps, 'children'> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  icon?: ComponentType<IconGlyphProps>;
  /** Icon after the label instead of before it. */
  iconRight?: ComponentType<IconGlyphProps>;
  loading?: boolean;
  className?: string;
  textClassName?: string;
  children?: ReactNode;
}

export function Button({
  variant = 'primary',
  size = 'md',
  icon,
  iconRight,
  loading = false,
  disabled,
  className,
  textClassName,
  children,
  ...props
}: ButtonProps) {
  const inactive = disabled || loading;
  const sizing = SIZE[size];
  const labelClass = cn(sizing.text, 'font-semibold', LABEL[variant], textClassName);
  const spinnerColor = useTokenColor(variant === 'primary' ? 'surface-950' : 'content-200');

  return (
    <Pressable
      accessibilityRole="button"
      accessibilityState={{ disabled: !!inactive, busy: loading }}
      disabled={inactive}
      className={cn(
        'flex-row items-center justify-center gap-2 active:opacity-80',
        sizing.box,
        CONTAINER[variant],
        inactive && 'opacity-45',
        className,
      )}
      {...props}
    >
      {loading ? (
        <ActivityIndicator size="small" color={variant === 'ghost' || variant === 'link' ? spinnerColor : '#fff'} />
      ) : icon ? (
        <Icon as={icon} size={sizing.icon} className={labelClass} />
      ) : null}
      {typeof children === 'string' || typeof children === 'number' ? (
        <Text className={labelClass} numberOfLines={1}>
          {children}
        </Text>
      ) : children ? (
        <View className="flex-row items-center gap-2">{children}</View>
      ) : null}
      {iconRight && !loading ? <Icon as={iconRight} size={sizing.icon} className={labelClass} /> : null}
    </Pressable>
  );
}

/** A square icon-only button, for headers and toolbars. */
export function IconButton({
  icon,
  size = 18,
  className,
  iconClassName,
  accessibilityLabel,
  ...props
}: Omit<PressableProps, 'children'> & {
  icon: ComponentType<IconGlyphProps>;
  size?: number;
  className?: string;
  iconClassName?: string;
  accessibilityLabel: string;
}) {
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={accessibilityLabel}
      hitSlop={6}
      className={cn('items-center justify-center rounded-lg p-2 active:bg-surface-850', className)}
      {...props}
    >
      <Icon as={icon} size={size} className={cn('text-content-300', iconClassName)} />
    </Pressable>
  );
}
