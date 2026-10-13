import { useState, type ComponentType, type ReactNode } from 'react';
import { Pressable, Switch as RNSwitch, View } from 'react-native';
import { Check, ChevronDown } from 'lucide-react-native';
import { cn } from '../../utils/cn';
import { useTokenColor } from '../../utils/theme-colors';
import { Icon, type IconGlyphProps } from './Icon';
import { Sheet } from './Sheet';
import { Text } from './Text';

/** A themed on/off switch. */
export function Switch({
  value,
  onValueChange,
  disabled,
  accessibilityLabel,
}: {
  value: boolean;
  onValueChange: (value: boolean) => void;
  disabled?: boolean;
  accessibilityLabel?: string;
}) {
  const on = useTokenColor('accent-solid');
  const off = useTokenColor('surface-700');
  const thumb = useTokenColor('content-100');
  return (
    <RNSwitch
      value={value}
      onValueChange={onValueChange}
      disabled={disabled}
      accessibilityLabel={accessibilityLabel}
      trackColor={{ true: on, false: off }}
      thumbColor={thumb}
      ios_backgroundColor={off}
    />
  );
}

/** A settings row: label and description on the left, a switch on the right. */
export function ToggleRow({
  label,
  description,
  value,
  onValueChange,
  disabled,
  className,
}: {
  label: string;
  description?: ReactNode;
  value: boolean;
  onValueChange: (value: boolean) => void;
  disabled?: boolean;
  className?: string;
}) {
  return (
    <Pressable
      onPress={() => !disabled && onValueChange(!value)}
      accessibilityRole="switch"
      accessibilityState={{ checked: value, disabled }}
      className={cn('flex-row items-center gap-3 py-3', disabled && 'opacity-50', className)}
    >
      <View className="min-w-0 flex-1">
        <Text className="text-sm font-medium text-content-100">{label}</Text>
        {description ? (
          typeof description === 'string' ? (
            <Text className="mt-0.5 text-xs leading-4 text-content-400">{description}</Text>
          ) : (
            description
          )
        ) : null}
      </View>
      <Switch value={value} onValueChange={onValueChange} disabled={disabled} accessibilityLabel={label} />
    </Pressable>
  );
}

export interface SegmentOption<T extends string | number> {
  value: T;
  label: string;
  icon?: ComponentType<IconGlyphProps>;
}

/** A row of mutually exclusive choices - filters, the theme control, chart types. */
export function Segmented<T extends string | number>({
  options,
  value,
  onChange,
  className,
  size = 'md',
  stretch = true,
  accessibilityLabel,
}: {
  options: ReadonlyArray<SegmentOption<T>>;
  value: T;
  onChange: (value: T) => void;
  className?: string;
  size?: 'sm' | 'md';
  /** Share the width equally. */
  stretch?: boolean;
  accessibilityLabel?: string;
}) {
  return (
    <View
      accessibilityRole="radiogroup"
      accessibilityLabel={accessibilityLabel}
      className={cn('flex-row gap-0.5 rounded-lg border border-surface-700 bg-surface-850 p-0.5', className)}
    >
      {options.map((option) => {
        const active = option.value === value;
        return (
          <Pressable
            key={String(option.value)}
            accessibilityRole="radio"
            accessibilityState={{ checked: active }}
            accessibilityLabel={option.label}
            onPress={() => onChange(option.value)}
            className={cn(
              'flex-row items-center justify-center gap-1.5 rounded-md',
              size === 'sm' ? 'px-2 py-1' : 'px-3 py-1.5',
              stretch && 'flex-1',
              active ? 'bg-surface-700' : 'active:bg-surface-800',
            )}
          >
            {option.icon ? (
              <Icon
                as={option.icon}
                size={size === 'sm' ? 13 : 15}
                className={active ? 'text-content-100' : 'text-content-400'}
              />
            ) : null}
            <Text
              numberOfLines={1}
              className={cn(
                size === 'sm' ? 'text-[11px]' : 'text-xs',
                'font-semibold',
                active ? 'text-content-100' : 'text-content-400',
              )}
            >
              {option.label}
            </Text>
          </Pressable>
        );
      })}
    </View>
  );
}

export interface SelectOption<T extends string | number> {
  value: T;
  label: string;
  description?: string;
}

/**
 * The phone form of a `<select>`: a field showing the choice, opening a sheet
 * of the options.
 */
export function Select<T extends string | number>({
  value,
  options,
  onChange,
  title,
  label,
  placeholder = 'Select',
  className,
  disabled,
}: {
  value: T | null | undefined;
  options: ReadonlyArray<SelectOption<T>>;
  onChange: (value: T) => void;
  /** Sheet title. */
  title: string;
  label?: string;
  placeholder?: string;
  className?: string;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const current = options.find((option) => option.value === value);

  return (
    <View className={className}>
      {label ? (
        <Text className="mb-1.5 text-[11px] font-medium uppercase tracking-wider text-content-400">{label}</Text>
      ) : null}
      <Pressable
        disabled={disabled}
        onPress={() => setOpen(true)}
        accessibilityRole="button"
        accessibilityLabel={(label ?? title) + ': ' + (current?.label ?? placeholder)}
        className={cn(
          'min-h-[44px] flex-row items-center rounded-lg border border-surface-700 bg-surface-850 px-3',
          disabled && 'opacity-50',
        )}
      >
        <Text numberOfLines={1} className={cn('flex-1 text-sm', current ? 'text-content-100' : 'text-content-400')}>
          {current?.label ?? placeholder}
        </Text>
        <Icon as={ChevronDown} size={16} className="text-content-400" />
      </Pressable>

      <Sheet open={open} onClose={() => setOpen(false)} title={title}>
        <View className="px-2 pb-2">
          {options.map((option) => {
            const selected = option.value === value;
            return (
              <Pressable
                key={String(option.value)}
                accessibilityRole="radio"
                accessibilityState={{ checked: selected }}
                onPress={() => {
                  onChange(option.value);
                  setOpen(false);
                }}
                className={cn(
                  'flex-row items-center gap-3 rounded-lg px-3 py-3',
                  selected ? 'bg-accent/10' : 'active:bg-surface-850',
                )}
              >
                <View className="min-w-0 flex-1">
                  <Text className={cn('text-sm', selected ? 'font-semibold text-content-100' : 'text-content-200')}>
                    {option.label}
                  </Text>
                  {option.description ? (
                    <Text className="mt-0.5 text-xs text-content-400">{option.description}</Text>
                  ) : null}
                </View>
                {selected ? <Icon as={Check} size={16} className="text-accent" /> : null}
              </Pressable>
            );
          })}
        </View>
      </Sheet>
    </View>
  );
}

/** A tappable list row with an optional icon, value and chevron - settings and account menus. */
export function ListRow({
  icon,
  label,
  description,
  value,
  right,
  onPress,
  destructive,
  className,
}: {
  icon?: ComponentType<IconGlyphProps>;
  label: string;
  description?: string;
  value?: ReactNode;
  right?: ReactNode;
  onPress?: () => void;
  destructive?: boolean;
  className?: string;
}) {
  return (
    <Pressable
      onPress={onPress}
      disabled={!onPress}
      accessibilityRole={onPress ? 'button' : undefined}
      className={cn('flex-row items-center gap-3 px-4 py-3 active:bg-surface-850', className)}
    >
      {icon ? (
        <View className="h-8 w-8 items-center justify-center rounded-lg bg-surface-850">
          <Icon as={icon} size={16} className={destructive ? 'text-down' : 'text-content-300'} />
        </View>
      ) : null}
      <View className="min-w-0 flex-1">
        <Text className={cn('text-sm font-medium', destructive ? 'text-down' : 'text-content-100')}>{label}</Text>
        {description ? <Text className="mt-0.5 text-xs text-content-400">{description}</Text> : null}
      </View>
      {value != null ? (
        typeof value === 'string' || typeof value === 'number' ? (
          <Text className="text-sm text-content-300 tnum">{value}</Text>
        ) : (
          value
        )
      ) : null}
      {right}
    </Pressable>
  );
}
