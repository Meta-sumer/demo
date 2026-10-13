import type { ComponentType } from 'react';
import type { StyleProp, TextStyle } from 'react-native';
import { cssInterop } from 'nativewind';

/**
 * A lucide icon coloured by class name.
 *
 *   <Icon as={Bell} size={18} className="text-content-300" />
 *
 * Icons draw with a `color` prop, not a style; this maps the text colour a
 * class resolves to onto that prop, so icons follow the theme like text does.
 */
export interface IconGlyphProps {
  size?: number;
  color?: string;
  strokeWidth?: number;
  fill?: string;
}

export interface IconProps {
  as: ComponentType<IconGlyphProps>;
  size?: number;
  strokeWidth?: number;
  className?: string;
  /** Explicit colour; wins over the class. */
  color?: string;
  fill?: string;
  style?: StyleProp<TextStyle>;
}

function IconImpl({ as: Glyph, size = 16, strokeWidth = 2, color, fill, style }: IconProps) {
  const fromClass = (style as TextStyle | undefined)?.color;
  return (
    <Glyph
      size={size}
      strokeWidth={strokeWidth}
      color={color ?? (typeof fromClass === 'string' ? fromClass : undefined) ?? '#888'}
      fill={fill}
    />
  );
}

cssInterop(IconImpl, { className: { target: 'style' } });

export const Icon = IconImpl;
