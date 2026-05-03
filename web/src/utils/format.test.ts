import { describe, it, expect } from 'vitest'
import { formatDuration, formatErrorRate, errorRateColor, formatCount } from './format'

describe('formatDuration', () => {
  it('formats microseconds', () => {
    expect(formatDuration(0)).toBe('0us')
    expect(formatDuration(500)).toBe('500us')
    expect(formatDuration(999)).toBe('999us')
  })

  it('formats milliseconds', () => {
    expect(formatDuration(1000)).toBe('1.0ms')
    expect(formatDuration(1200)).toBe('1.2ms')
    expect(formatDuration(9500)).toBe('9.5ms')
    expect(formatDuration(42000)).toBe('42ms')
    expect(formatDuration(999999)).toBe('1000ms')
  })

  it('formats seconds', () => {
    expect(formatDuration(1_000_000)).toBe('1.0s')
    expect(formatDuration(1_200_000)).toBe('1.2s')
    expect(formatDuration(5_300_000)).toBe('5.3s')
    expect(formatDuration(15_000_000)).toBe('15s')
  })

  it('handles negative values', () => {
    expect(formatDuration(-100)).toBe('0us')
  })
})

describe('formatErrorRate', () => {
  it('formats zero', () => {
    expect(formatErrorRate(0)).toBe('0%')
  })

  it('formats very small rates', () => {
    expect(formatErrorRate(0.0005)).toBe('<0.1%')
  })

  it('formats normal rates', () => {
    expect(formatErrorRate(0.05)).toBe('5.0%')
    expect(formatErrorRate(0.123)).toBe('12.3%')
  })
})

describe('errorRateColor', () => {
  it('returns green for low error rates', () => {
    expect(errorRateColor(0)).toBe('#22c55e')
    expect(errorRateColor(0.005)).toBe('#22c55e')
  })

  it('returns yellow for medium error rates', () => {
    expect(errorRateColor(0.02)).toBe('#eab308')
    expect(errorRateColor(0.04)).toBe('#eab308')
  })

  it('returns red for high error rates', () => {
    expect(errorRateColor(0.05)).toBe('#ef4444')
    expect(errorRateColor(0.5)).toBe('#ef4444')
  })
})

describe('formatCount', () => {
  it('formats small numbers as-is', () => {
    expect(formatCount(0)).toBe('0')
    expect(formatCount(999)).toBe('999')
  })

  it('formats thousands with K suffix', () => {
    expect(formatCount(1000)).toBe('1.0K')
    expect(formatCount(5432)).toBe('5.4K')
  })

  it('formats millions with M suffix', () => {
    expect(formatCount(1_000_000)).toBe('1.0M')
    expect(formatCount(2_500_000)).toBe('2.5M')
  })
})
