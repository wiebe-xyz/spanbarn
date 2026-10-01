import { describe, it, expect } from 'vitest'
import { compareUrl, rangeCovering } from './compareLink'

const HOUR = 3600_000
const now = Date.UTC(2026, 9, 1, 12)

describe('rangeCovering', () => {
  it('picks the smallest range that reaches the start', () => {
    expect(rangeCovering(now - 30 * 60_000, now)).toBe('1h')
    expect(rangeCovering(now - 5 * HOUR, now)).toBe('24h')
    expect(rangeCovering(now - 30 * HOUR, now)).toBe('48h')
    expect(rangeCovering(now - 100 * HOUR, now)).toBe('7d')
  })
  it('falls back to the widest range', () => {
    expect(rangeCovering(now - 30 * 24 * HOUR, now)).toBe('7d')
  })
})

describe('compareUrl', () => {
  it('carries the project, range and selection', () => {
    const url = compareUrl({
      projectId: 7,
      selection: { match: 'and', filters: [{ key: 'service', op: '=', value: 'web' }] },
      fromMs: now - HOUR / 2,
      nowMs: now,
    })
    const params = new URLSearchParams(url.split('?')[1])
    expect(url.startsWith('/compare?')).toBe(true)
    expect(params.get('project')).toBe('7')
    expect(params.get('range')).toBe('1h')
    expect(JSON.parse(params.get('selection') ?? '')).toEqual({
      match: 'and',
      filters: [{ key: 'service', op: '=', value: 'web' }],
    })
  })
  it('omits an empty selection and an unknown project', () => {
    const params = new URLSearchParams(
      compareUrl({ projectId: 0, selection: { match: 'and', filters: [] }, fromMs: now, nowMs: now }).split('?')[1],
    )
    expect(params.has('selection')).toBe(false)
    expect(params.has('project')).toBe(false)
  })
})
