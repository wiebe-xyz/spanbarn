import { describe, it, expect } from 'vitest'
import {
  DASHBOARD_RANGES,
  customWindow,
  findDashboardRange,
  getDashboardWindow,
  parseCustomWindow,
  zoomOutWindow,
} from './dashboardWindow'

const NOW = new Date('2026-10-01T12:00:00Z')

describe('getDashboardWindow', () => {
  it('ends at now for offset 0', () => {
    const w = getDashboardWindow('24h', 0, NOW)
    expect(w.to).toBe('2026-10-01T12:00:00.000Z')
    expect(w.from).toBe('2026-09-30T12:00:00.000Z')
    expect(w.toMs - w.fromMs).toBe(24 * 3600_000)
  })

  it('moves back by one window width per offset step', () => {
    const w = getDashboardWindow('4h', 2, NOW)
    expect(w.to).toBe('2026-10-01T04:00:00.000Z')
    expect(w.from).toBe('2026-10-01T00:00:00.000Z')
  })

  it('windows at consecutive offsets touch without overlapping', () => {
    const a = getDashboardWindow('1h', 1, NOW)
    const b = getDashboardWindow('1h', 0, NOW)
    expect(a.toMs).toBe(b.fromMs)
  })

  it('clamps negative and fractional offsets', () => {
    expect(getDashboardWindow('1h', -3, NOW).toMs).toBe(NOW.getTime())
    expect(getDashboardWindow('1h', 1.9, NOW).toMs).toBe(NOW.getTime() - 3600_000)
  })

  it('never produces a window wider than 48 hours', () => {
    for (const r of DASHBOARD_RANGES) {
      const w = getDashboardWindow(r.value, 0, NOW)
      expect(w.toMs - w.fromMs).toBeLessThanOrEqual(48 * 3600_000)
    }
  })
})

describe('findDashboardRange', () => {
  it('falls back to the default for unknown values', () => {
    expect(findDashboardRange('7d').value).toBe('24h')
  })

  it('labels the default range as in the reference dashboard', () => {
    expect(findDashboardRange('24h').label).toBe('Last 1 day')
  })
})

describe('customWindow', () => {
  const at = (iso: string) => Date.parse(iso)

  it('keeps a window inside the bounds untouched', () => {
    const w = customWindow(at('2026-10-01T10:00:00Z'), at('2026-10-01T10:20:00Z'), NOW)
    expect(w.fromMs).toBe(at('2026-10-01T10:00:00Z'))
    expect(w.toMs).toBe(at('2026-10-01T10:20:00Z'))
  })

  it('grows a window under a minute around its centre', () => {
    const w = customWindow(at('2026-10-01T10:00:00Z'), at('2026-10-01T10:00:10Z'), NOW)
    expect(w.toMs - w.fromMs).toBe(60_000)
    expect((w.fromMs + w.toMs) / 2).toBe(at('2026-10-01T10:00:05Z'))
  })

  it('caps the width at 48 hours', () => {
    const w = customWindow(at('2026-09-20T00:00:00Z'), at('2026-10-01T00:00:00Z'), NOW)
    expect(w.toMs - w.fromMs).toBe(48 * 3600_000)
  })

  it('shifts a window that ends in the future back to now', () => {
    const w = customWindow(at('2026-10-01T11:30:00Z'), at('2026-10-01T12:30:00Z'), NOW)
    expect(w.toMs).toBe(NOW.getTime())
    expect(w.toMs - w.fromMs).toBe(3600_000)
  })
})

describe('zoomOutWindow', () => {
  it('doubles the width around the same centre', () => {
    const w = zoomOutWindow(Date.parse('2026-10-01T10:00:00Z'), Date.parse('2026-10-01T10:10:00Z'), NOW)
    expect(w.fromMs).toBe(Date.parse('2026-10-01T09:55:00Z'))
    expect(w.toMs).toBe(Date.parse('2026-10-01T10:15:00Z'))
  })
})

describe('parseCustomWindow', () => {
  it('reads epoch-millisecond params', () => {
    const w = parseCustomWindow('1790856000000', '1790859600000', new Date(1790900000000))
    expect(w?.toMs).toBe(1790859600000)
  })

  it('returns null unless both params are valid and ordered', () => {
    expect(parseCustomWindow(null, '5')).toBeNull()
    expect(parseCustomWindow('abc', '5')).toBeNull()
    expect(parseCustomWindow('10', '5')).toBeNull()
  })
})
