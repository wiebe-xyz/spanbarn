import { describe, it, expect } from 'vitest'
import {
  calcLabel,
  defaultState,
  distinctWire,
  drillHref,
  formatCalc,
  groupLabel,
  hasQuery,
  normalize,
  pivotAnalyzeSeries,
  stateFromParams,
  stateToParams,
} from './model'

describe('calculations', () => {
  it('labels and formats each kind of calculation', () => {
    expect(calcLabel('p95')).toBe('P95')
    expect(calcLabel(distinctWire('user.id'))).toBe('Distinct user.id')
    expect(formatCalc('count', 1500)).toBe('1.5K')
    expect(formatCalc('error_rate', 0.25)).toMatch(/25/)
    expect(formatCalc('p99', 2500)).toBe('2.5ms')
    expect(formatCalc(distinctWire('x'), 12)).toBe('12')
  })
})

describe('query state in the URL', () => {
  it('round-trips group by, calculations, order and filter', () => {
    const state = {
      ...defaultState(),
      projectId: 7,
      range: '7d' as const,
      groupBy: ['url.path', 'http.response.status_code'],
      calcs: ['count', 'p95', distinctWire('user')],
      orderBy: 'p95',
      asc: true,
      limit: 50,
      sample: '10',
      view: 'chart' as const,
      chartCalc: 'p95',
      filter: { match: 'and' as const, filters: [{ key: 'kind', op: '=' as const, value: 'server' }] },
    }
    const params = stateToParams(state)
    expect(hasQuery(params)).toBe(true)
    expect(params.getAll('group_by')).toEqual(['url.path', 'http.response.status_code'])
    expect(stateFromParams(new URLSearchParams(params.toString()))).toEqual(state)
  })

  it('falls back to defaults for an empty or invalid URL', () => {
    const s = stateFromParams(new URLSearchParams('range=9y&limit=7&view=x&calc='))
    expect(s).toEqual(defaultState())
    expect(hasQuery(new URLSearchParams())).toBe(false)
  })

  it('normalize drops blank and repeated keys and a stale order', () => {
    const s = normalize({ ...defaultState(), groupBy: ['a', ' ', 'a', 'b'], calcs: ['count'], orderBy: 'p95', chartCalc: 'p99' })
    expect(s.groupBy).toEqual(['a', 'b'])
    expect(s.orderBy).toBe('')
    expect(s.chartCalc).toBe('')
    expect(normalize({ ...defaultState(), calcs: [] }).calcs).toEqual(['count'])
  })
})

describe('group labels and series', () => {
  it('labels groups, a missing value and the other row', () => {
    expect(groupLabel(['/a', 'GET'])).toBe('/a / GET')
    expect(groupLabel(['/a', ''])).toBe('/a / (none)')
    expect(groupLabel([], true)).toBe('other')
    expect(groupLabel([])).toBe('all spans')
  })

  it('pivots a series response into chart rows with gaps for null', () => {
    const pivot = pivotAnalyzeSeries({
      groupBy: ['p'],
      calc: 'p95',
      bucketSeconds: 60,
      buckets: [60, 120],
      series: [
        { group: ['/a'], values: [1, null] },
        { group: [''], other: true, values: [null, 3] },
      ],
      scanned: 4,
      sampleEvery: 1,
      truncated: false,
      maxSpans: 100,
    })
    expect(pivot.groups).toEqual(['/a', 'other'])
    expect(pivot.rows).toEqual([
      { time: 60_000, values: { '/a': 1 } },
      { time: 120_000, values: { other: 3 } },
    ])
  })
})

describe('drillHref', () => {
  it('links to the traces page with the drill filter, project and window', () => {
    const href = drillHref(7, { match: 'and', filters: [{ key: 'a', op: '=', value: 'b' }] }, Date.UTC(2026, 9, 1), Date.UTC(2026, 9, 2))
    const url = new URL(href, 'http://x')
    expect(url.pathname).toBe('/traces')
    expect(url.searchParams.get('project')).toBe('7')
    expect(JSON.parse(url.searchParams.get('filter') ?? '').filters[0].key).toBe('a')
    expect(url.searchParams.get('from')).toMatch(/^\d{4}-\d\d-\d\dT\d\d:\d\d$/)
  })
})
