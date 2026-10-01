import { describe, it, expect } from 'vitest'
import { heatOpacity, layoutHeatmap, pivotSeries, seriesColor, statusColor, statusDash } from './dashboardData'

const t = (min: number) => new Date(Date.UTC(2026, 9, 1, 12, min)).toISOString()

describe('pivotSeries', () => {
  it('builds one row per bucket with a value per group', () => {
    const { rows, groups } = pivotSeries(
      [
        { time: t(5), group: 'web', n: 3 },
        { time: t(0), group: 'web', n: 1 },
        { time: t(0), group: 'db', n: 2 },
      ],
      (p) => p.n,
    )
    expect(groups).toEqual(['db', 'web'])
    expect(rows.map((r) => r.values)).toEqual([{ web: 1, db: 2 }, { web: 3 }])
    expect(rows[0].time).toBeLessThan(rows[1].time)
  })

  it('sorts "other" last', () => {
    const { groups } = pivotSeries(
      [
        { time: t(0), group: 'other', n: 1 },
        { time: t(0), group: 'zeta', n: 1 },
        { time: t(0), group: 'alpha', n: 1 },
      ],
      (p) => p.n,
    )
    expect(groups).toEqual(['alpha', 'zeta', 'other'])
  })

  it('keeps group names with dots and slashes intact', () => {
    const { rows } = pivotSeries([{ time: t(0), group: 'GET /api/v1.0', n: 7 }], (p) => p.n)
    expect(rows[0].values['GET /api/v1.0']).toBe(7)
  })

  it('skips points with an unparseable time and handles empty input', () => {
    expect(pivotSeries([{ time: 'nope', group: 'a', n: 1 }], (p) => p.n).rows).toEqual([])
    expect(pivotSeries([], () => 0)).toEqual({ rows: [], groups: [] })
  })
})

describe('colors', () => {
  it('colors status codes by class', () => {
    expect(statusColor('200')).toBe(statusColor('204'))
    expect(statusColor('200')).not.toBe(statusColor('500'))
    expect(statusColor('404')).not.toBe(statusColor('500'))
  })

  it('tells apart codes of one class by dash pattern', () => {
    const groups = ['200', '201', '500']
    expect(statusDash('200', groups)).toBeUndefined()
    expect(statusDash('201', groups)).toBeDefined()
    expect(statusDash('500', groups)).toBeUndefined()
  })

  it('draws "other" in grey whatever its position', () => {
    expect(seriesColor('other', 0)).toBe(seriesColor('other', 7))
    expect(seriesColor('web', 0)).not.toBe(seriesColor('other', 0))
  })

  it('wraps the palette for more than ten series', () => {
    expect(seriesColor('a', 0)).toBe(seriesColor('k', 10))
  })
})

describe('layoutHeatmap', () => {
  const from = Date.UTC(2026, 9, 1, 12, 0)
  const to = Date.UTC(2026, 9, 1, 13, 0)
  const cell = (min: number, bucket: number, count: number) => ({
    time: t(min), bucket, lowerUs: bucket * 10, upperUs: bucket * 10 + 10, count,
  })

  it('places cells in columns by time bucket and tracks the row range', () => {
    const layout = layoutHeatmap([cell(0, 4, 2), cell(10, 9, 50), cell(59, 6, 1)], from, to, 600)!
    expect(layout.cols).toBe(6)
    expect(layout.cells.map((c) => c.col)).toEqual([0, 1, 5])
    expect(layout.minBucket).toBe(4)
    expect(layout.maxBucket).toBe(9)
    expect(layout.maxCount).toBe(50)
  })

  it('drops cells outside the window', () => {
    const layout = layoutHeatmap([cell(0, 3, 1), cell(-30, 3, 5), cell(90, 3, 5)], from, to, 600)!
    expect(layout.cells).toHaveLength(1)
    expect(layout.maxCount).toBe(1)
  })

  it('returns null when nothing is drawable', () => {
    expect(layoutHeatmap([], from, to, 600)).toBeNull()
    expect(layoutHeatmap([cell(120, 1, 1)], from, to, 600)).toBeNull()
  })
})

describe('heatOpacity', () => {
  it('rises with count and stays within bounds', () => {
    const low = heatOpacity(1, 1000)
    const mid = heatOpacity(30, 1000)
    const high = heatOpacity(1000, 1000)
    expect(low).toBeLessThan(mid)
    expect(mid).toBeLessThan(high)
    expect(low).toBeGreaterThanOrEqual(0.15)
    expect(high).toBe(1)
  })

  it('is fully opaque when every cell has one span', () => {
    expect(heatOpacity(1, 1)).toBe(1)
  })
})
