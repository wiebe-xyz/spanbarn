/**
 * Format microseconds into a human-readable duration string.
 * Examples: "1.2ms", "42ms", "1.2s", "5.3s"
 */
export function formatDuration(us: number): string {
  if (us < 0) return '0us'
  if (us < 1000) return `${Math.round(us)}us`
  if (us < 1_000_000) {
    const ms = us / 1000
    return ms < 10 ? `${ms.toFixed(1)}ms` : `${Math.round(ms)}ms`
  }
  const s = us / 1_000_000
  return s < 10 ? `${s.toFixed(1)}s` : `${Math.round(s)}s`
}

/**
 * Format an error rate (0-1 float) as a percentage string.
 */
export function formatErrorRate(rate: number): string {
  if (rate === 0) return '0%'
  if (rate < 0.001) return '<0.1%'
  if (rate < 0.01) return `${(rate * 100).toFixed(1)}%`
  return `${(rate * 100).toFixed(1)}%`
}

/**
 * Return a CSS color for an error rate.
 */
export function errorRateColor(rate: number): string {
  if (rate < 0.01) return '#22c55e' // green
  if (rate < 0.05) return '#eab308' // yellow
  return '#ef4444' // red
}

/**
 * Format a number with K/M suffixes.
 */
export function formatCount(n: number): string {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}K`
  return `${(n / 1_000_000).toFixed(1)}M`
}
