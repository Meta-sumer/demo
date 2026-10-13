/**
 * Text colour for a label drawn on a coloured tag.
 *
 * lightweight-charts picks black or white for its axis labels from the
 * background's brightness; this is the same rule, so a last-price tag in the
 * light theme's dark green gets white text and the dark theme's pale green
 * gets black.
 */
export function contrastText(background: string): string {
  const rgb = parseColor(background);
  if (!rgb) return '#ffffff';
  const brightness = 0.199 * rgb[0] + 0.687 * rgb[1] + 0.114 * rgb[2];
  return brightness > 160 ? '#000000' : '#ffffff';
}

function parseColor(color: string): [number, number, number] | null {
  if (color.startsWith('#')) {
    const hex = color.length === 4 ? color[1]! + color[1]! + color[2]! + color[2]! + color[3]! + color[3]! : color.slice(1, 7);
    if (hex.length !== 6) return null;
    return [parseInt(hex.slice(0, 2), 16), parseInt(hex.slice(2, 4), 16), parseInt(hex.slice(4, 6), 16)];
  }
  const match = /rgba?\(([^)]+)\)/.exec(color);
  if (!match) return null;
  const parts = match[1]!.split(',').map((part) => Number(part.trim()));
  if (parts.length < 3 || parts.slice(0, 3).some((part) => !Number.isFinite(part))) return null;
  return [parts[0]!, parts[1]!, parts[2]!];
}
