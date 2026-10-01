import { describe, it, expect } from 'vitest'
import type { BoardPanel } from '../api/types'
import { defaultState } from '../analyze/model'
import { parseFilter } from '../filters/model'
import {
  chartCalcOf,
  definitionFromState,
  filtersFromState,
  markersFrom,
  panelParams,
  panelQueryHref,
  panelSeriesParams,
  panelToState,
  windowFor,
} from './model'

const filters = { match: 'and', filters: [{ key: 'kind', op: '=', value: 'server' }] }

const panel = (over: Partial<BoardPanel> = {}): BoardPanel => ({
  id: 1,
  boardId: 1,
  savedQueryId: 1,
  title: 'P95 by method',
  view: 'chart',
  position: 0,
  query: {
    id: 1, projectId: 7, name: 'x', service: '', operation: '', status: '', minDurationUs: 0, createdAt: '',
    filters,
    definition: { groupBy: ['http.request.method'], calcs: ['count', 'p95'], limit: 10, chartCalc: 'p95', orderBy: 'count', asc: true, sample: 10 },
  },
  ...over,
})

describe('boards model', () => {
  it('windows end now and span the board range', () => {
    expect(windowFor('1h', 10_000_000)).toEqual({ fromMs: 10_000_000 - 3600_000, toMs: 10_000_000 })
    expect(windowFor('7d', 10_000_000_000).toMs - windowFor('7d', 10_000_000_000).fromMs).toBe(168 * 3600_000)
    // An unknown range reads as the default day.
    expect(windowFor('bogus', 1_000_000_000).toMs - windowFor('bogus', 1_000_000_000).fromMs).toBe(24 * 3600_000)
  })

  it('stores a query as a definition and a filter', () => {
    const s = { ...defaultState(), groupBy: ['url.path'], calcs: ['count', 'p95'], orderBy: 'p95', asc: true, sample: '10', limit: 50, chartCalc: 'p95' }
    s.filter = parseFilter(filters)
    expect(definitionFromState(s)).toEqual({ groupBy: ['url.path'], calcs: ['count', 'p95'], limit: 50, orderBy: 'p95', asc: true, sample: 10, chartCalc: 'p95' })
    expect(filtersFromState(s)).toEqual(filters)
    expect(filtersFromState(defaultState())).toBeUndefined()
    // The chart calculation falls back to the first calculation.
    expect(definitionFromState({ ...defaultState(), calcs: ['p99'] }).chartCalc).toBe('p99')
  })

  it('turns a panel into table and series parameters', () => {
    const w = { fromMs: 0, toMs: 3600_000 }
    const table = panelParams(panel(), 7, w)
    expect(table).toMatchObject({
      projectId: 7, from: '1970-01-01T00:00:00.000Z', to: '1970-01-01T01:00:00.000Z',
      groupBy: ['http.request.method'], calcs: ['count', 'p95'], orderBy: 'count', asc: true, limit: 10, sample: 10,
    })
    expect(JSON.parse(table.filter!)).toEqual(filters)
    // A sort chosen on screen wins over the stored one.
    expect(panelParams(panel(), 7, w, { orderBy: 'p95', asc: false })).toMatchObject({ orderBy: 'p95', asc: false })

    const series = panelSeriesParams(panel(), 7, w)
    expect(series.calcs).toEqual(['p95'])
    expect(series.orderBy).toBeUndefined()
    expect(series.limit).toBe(8)
  })

  it('reads a panel without a definition or filter as a plain count', () => {
    const bare = panel()
    bare.query = { ...bare.query, filters: null, definition: undefined }
    const p = panelParams(bare, 7, { fromMs: 0, toMs: 1 })
    expect(p.groupBy).toEqual([])
    expect(p.calcs).toEqual(['count'])
    expect(p.filter).toBeUndefined()
    expect(chartCalcOf({ groupBy: [], calcs: ['p50', 'p95'] })).toBe('p50')
  })

  it('opens a panel on the query page with its range and view', () => {
    const state = panelToState(panel(), { projectId: 7, timeRange: '7d' })
    expect(state).toMatchObject({ projectId: 7, range: '7d', view: 'chart', chartCalc: 'p95', sample: '10', limit: 10, asc: true })
    const href = panelQueryHref(panel(), { projectId: 7, timeRange: '7d' })
    const url = new URL(href, 'http://x')
    expect(url.pathname).toBe('/analyze')
    expect(url.searchParams.get('run')).toBe('1')
    expect(url.searchParams.get('project')).toBe('7')
    expect(url.searchParams.get('range')).toBe('7d')
    expect(url.searchParams.getAll('calc')).toEqual(['count', 'p95'])
    expect(JSON.parse(url.searchParams.get('filter')!)).toEqual(filters)
  })

  it('keeps only the releases inside the window', () => {
    const w = { fromMs: Date.parse('2026-09-01T00:00:00Z'), toMs: Date.parse('2026-09-02T00:00:00Z') }
    const out = markersFrom(
      [
        { version: 'old', releasedAt: '2026-08-31T00:00:00Z' },
        { version: 'v1', releasedAt: '2026-09-01T12:00:00Z' },
        { version: 'bad', releasedAt: 'nope' },
      ],
      w,
    )
    expect(out).toEqual([{ time: Date.parse('2026-09-01T12:00:00Z'), label: 'v1' }])
  })
})
