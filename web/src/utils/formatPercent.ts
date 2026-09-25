// A share of a total as text. Whole numbers from 10% up, one decimal below it,
// so a small slice reads "0.9%" rather than rounding away to a misleading "1%"
// or "0%". Only a true zero prints "0%".
export const formatPercent = (part: number, total: number): string => {
  if (total <= 0 || part <= 0) return "0%";
  const p = (part / total) * 100;
  if (p < 0.1) return "<0.1%";
  return p < 10 ? `${p.toFixed(1)}%` : `${Math.round(p)}%`;
};
