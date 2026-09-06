/**
 * Builds an SVG path `d` attribute for a hand-rolled line sparkline across a
 * 100x30 viewBox, normalized against the series' own max - a quiet metric
 * still fills the available height instead of drawing a flat line pinned to
 * the bottom. No charting library involved (see rapido-ui/Monitoring.tsx):
 * one of these is drawn per host per metric, and a chart library per card
 * costs more than the picture is worth.
 *
 * Returns null for fewer than two points - a single point has no line to draw.
 */
export const buildSparklinePath = (points: number[]): string | null => {
  if (points.length < 2) return null;
  // Clamped to at least 1 so an all-zero series doesn't divide by zero.
  const max = Math.max(...points, 1);
  const step = 100 / (points.length - 1);
  return points
    .map((p, i) => `${i === 0 ? "M" : "L"}${(i * step).toFixed(2)},${(30 - (p / max) * 28).toFixed(2)}`)
    .join(" ");
};
