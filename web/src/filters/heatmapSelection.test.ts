import { describe, it, expect } from 'vitest'
import {
  cellIndex,
  heatmapCompareUrl,
  normalizeRect,
  rectConditions,
  selectionForRect,
  type HeatmapGrid,
} from './heatmapSelection'
import { emptyExpr, type FilterExpr } from './model'

const grid: HeatmapGrid = {
  timeBuckets: 4,
  durationBuckets: 3,
  bucketMicros: 1000,
  durationEdgesUs: [10, 100, 1000, 10000],
}
const FROM = 5_000_000

describe('normalizeRect', () => {
  it('orders the corners and clamps to the grid', () => {
    expect(normalizeRect({ t: 3, d: 2 }, { t: 1, d: 0 }, grid)).toEqual({ t0: 1, t1: 3, d0: 0, d1: 2 })
    expect(normalizeRect({ t: -5, d: 9 }, { t: 2, d: 1 }, grid)).toEqual({ t0: 0, t1: 2, d0: 1, d1: 2 })
  })
})

describe('rectConditions', () => {
  it('turns an inner rectangle into duration and start time bounds', () => {
    expect(rectConditions(grid, FROM, { t0: 1, t1: 2, d0: 1, d1: 1 })).toEqual([
      { key: 'duration_us', op: '>=', value: '100' },
      { key: 'duration_us', op: '<', value: '1000' },
      { key: 'start_time_us', op: '>=', value: String(FROM + 1000) },
      { key: 'start_time_us', op: '<', value: String(FROM + 3000) },
    ])
  })

  it('leaves the sides on the grid edge open', () => {
    expect(rectConditions(grid, FROM, { t0: 0, t1: 3, d0: 2, d1: 2 })).toEqual([
      { key: 'duration_us', op: '>=', value: '1000' },
    ])
    expect(rectConditions(grid, FROM, { t0: 0, t1: 3, d0: 0, d1: 2 })).toEqual([])
  })
})

describe('selectionForRect', () => {
  const rect = { t0: 0, t1: 3, d0: 2, d1: 2 }
  const slow = [{ key: 'duration_us', op: '>=', value: '1000' }]

  it('uses the rectangle alone without a heatmap filter', () => {
    expect(selectionForRect(emptyExpr(), grid, FROM, rect)).toEqual({ match: 'and', filters: slow })
  })

  it('appends the rectangle to an AND filter', () => {
    const base: FilterExpr = { match: 'and', filters: [{ key: 'service', op: '=', value: 'web' }] }
    expect(selectionForRect(base, grid, FROM, rect).filters).toEqual([{ key: 'service', op: '=', value: 'web' }, ...slow])
  })

  it('wraps an OR filter in a group so the rectangle still applies to all of it', () => {
    const base: FilterExpr = {
      match: 'or',
      filters: [{ key: 'service', op: '=', value: 'web' }, { key: 'service', op: '=', value: 'api' }],
    }
    expect(selectionForRect(base, grid, FROM, rect)).toEqual({
      match: 'and',
      filters: [{ match: 'or', filters: base.filters }, ...slow],
    })
  })

  it('falls back to the rectangle when an OR filter already holds a group', () => {
    const base: FilterExpr = {
      match: 'or',
      filters: [
        { key: 'a', op: '=', value: '1' },
        { match: 'and', filters: [{ key: 'b', op: '=', value: '2' }] },
      ],
    }
    expect(selectionForRect(base, grid, FROM, rect)).toEqual({ match: 'and', filters: slow })
  })
})

describe('heatmapCompareUrl', () => {
  it('serialises the selection and passes the heatmap filter as baseline', () => {
    const base: FilterExpr = { match: 'and', filters: [{ key: 'service', op: '=', value: 'web' }] }
    const url = heatmapCompareUrl({ projectId: 7, range: '24h', base, grid, fromUs: FROM, rect: { t0: 0, t1: 3, d0: 2, d1: 2 } })
    const u = new URL(url, 'http://x')
    expect(u.pathname).toBe('/compare')
    expect(u.searchParams.get('project')).toBe('7')
    expect(u.searchParams.get('range')).toBe('24h')
    expect(JSON.parse(u.searchParams.get('selection') ?? '')).toEqual({
      match: 'and',
      filters: [
        { key: 'service', op: '=', value: 'web' },
        { key: 'duration_us', op: '>=', value: '1000' },
      ],
    })
    expect(JSON.parse(u.searchParams.get('baseline') ?? '')).toEqual({ match: 'and', filters: base.filters })
  })

  it('omits the baseline without a heatmap filter', () => {
    const url = heatmapCompareUrl({ projectId: 7, range: '1h', base: emptyExpr(), grid, fromUs: FROM, rect: { t0: 0, t1: 3, d0: 2, d1: 2 } })
    expect(new URL(url, 'http://x').searchParams.has('baseline')).toBe(false)
  })
})

describe('cellIndex', () => {
  it('maps a pixel offset to a cell and clamps outside the axis', () => {
    expect(cellIndex(0, 100, 4)).toBe(0)
    expect(cellIndex(26, 100, 4)).toBe(1)
    expect(cellIndex(99, 100, 4)).toBe(3)
    expect(cellIndex(250, 100, 4)).toBe(3)
    expect(cellIndex(-10, 100, 4)).toBe(0)
    expect(cellIndex(10, 0, 4)).toBe(0)
  })
})
