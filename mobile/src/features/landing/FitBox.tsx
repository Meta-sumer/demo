import { useState, type ReactNode } from 'react';
import { View, type StyleProp, type ViewStyle } from 'react-native';

/**
 * Renders fixed-size content scaled to whatever width it is given.
 *
 * The workspace mock is built at one size, in points, because that is the only
 * way its type, its rails and its panels keep the proportions of the real app.
 * Laid out at the width of a phone it would reflow into a different, wrong
 * layout instead of getting smaller.
 *
 * Scaling sidesteps that. The content is laid out at `width` x `height` and
 * then transformed to fit, so the box reports only the size it was given and
 * the picture shrinks as one piece - type, borders and spacing together.
 *
 * The box's own measured width decides the scale, not the screen's: what
 * matters is the room this box was given by the layout around it. Nothing is
 * drawn until that is known, so the picture never flashes at full size.
 */
export function FitBox({
  width,
  height,
  children,
  className,
  style,
}: {
  width: number;
  height: number;
  children: ReactNode;
  className?: string;
  style?: StyleProp<ViewStyle>;
}) {
  const [scale, setScale] = useState(0);

  return (
    <View
      className={className}
      style={[{ aspectRatio: width / height, overflow: 'hidden' }, style]}
      onLayout={(event) => {
        const available = event.nativeEvent.layout.width;
        if (available > 0) setScale(available / width);
      }}
    >
      {scale > 0 ? (
        <View style={{ width, height, transformOrigin: 'top left', transform: [{ scale }] }}>{children}</View>
      ) : null}
    </View>
  );
}
