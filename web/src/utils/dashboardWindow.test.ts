import { describe, it, expect } from 'vitest'
import { DASHBOARD_RANGES, findDashboardRange, getDashboardWindow } from './dashboardWindow'

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
