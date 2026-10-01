import { useEffect } from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within, cleanup } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { AnalyzePage } from './AnalyzePage'

vi.mock('../api/client', () => ({
  api: {
    listProjects: vi.fn(),
    getAttributes: vi.fn(),
    analyze: vi.fn(),
    analyzeSeries: vi.fn(),
  },
}))

// Recharts needs layout the test DOM lacks, so the chart is replaced by a probe.
vi.mock('../components/dashboard/SeriesChart', () => ({
  SeriesChart: ({ series, label }: { series: { groups: string[] }; label: string }) => (
    <div data-testid="chart">{label}: {series.groups.join(',')}</div>
  ),
}))

import { api } from '../api/client'

const analyze = vi.mocked(api.analyze)
const analyzeSeries = vi.mocked(api.analyzeSeries)

const drill = { match: 'and', filters: [{ key: 'url.path', op: '=', value: '/a' }] }

const table = (over: Record<string, unknown> = {}) => ({
  groupBy: ['url.path'],
  calcs: ['count', 'p95'],
  rows: [
    { group: ['/a'], count: 10, values: [10, 5000], drill },
    { group: [''], count: 4, values: [4, 100], drill: { match: 'and', filters: [{ key: 'url.path', op: 'does-not-exist' }] } },
  ],
  other: { group: [], other: true, count: 6, values: [6, 80] },
  scanned: 20,
  sampleEvery: 1,
  truncated: false,
  maxSpans: 200000,
  ...over,
})

const seen = { loc: '' }

function Where(): null {
  const loc = useLocation()
  useEffect(() => {
    seen.loc = loc.pathname + loc.search
  }, [loc])
  return null
}

function renderPage(url = '/analyze') {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <Where />
      <Routes>
        <Route path="/analyze" element={<AnalyzePage />} />
        <Route path="/traces" element={<div>traces page</div>} />
      </Routes>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listProjects).mockResolvedValue([{ id: 7, slug: 'profotograaf', name: 'Profotograaf', status: 'active' }])
  vi.mocked(api.getAttributes).mockResolvedValue({
    scanned: 1, sample: 1, truncated: false, maxSpans: 1,
    keys: [{ key: 'url.path', spans: 1, coverage: 1, distinct: 1, distinctCapped: false, top: [] }],
  })
  analyze.mockResolvedValue(table())
  analyzeSeries.mockResolvedValue({
    groupBy: ['url.path'], calc: 'count', bucketSeconds: 3600, buckets: [3600, 7200],
    series: [{ group: ['/a'], values: [1, 2] }, { group: [], other: true, values: [0, 1] }],
    scanned: 20, sampleEvery: 1, truncated: false, maxSpans: 200000,
  })
})

describe('AnalyzePage', () => {
  it('runs nothing until asked, then shows a table with an other row', async () => {
    renderPage()
    await screen.findByRole('option', { name: 'Profotograaf' })
    expect(analyze).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: '+ Add group by' }))
    fireEvent.change(screen.getByLabelText('Group by key 1'), { target: { value: 'url.path' } })
    fireEvent.click(screen.getByRole('button', { name: 'Run query' }))

    await waitFor(() => expect(analyze).toHaveBeenCalledTimes(1))
    const sent = analyze.mock.calls[0][0]
    expect(sent.projectId).toBe(7)
    expect(sent.groupBy).toEqual(['url.path'])
    expect(sent.calcs).toEqual(['count', 'p95'])
    expect(Date.parse(sent.to) - Date.parse(sent.from)).toBe(24 * 3600_000)

    const rows = await screen.findAllByTestId('group-row')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText('/a')).toBeInTheDocument()
    expect(within(rows[0]).getByText('5.0ms')).toBeInTheDocument()
    expect(within(rows[1]).getByText('(none)')).toBeInTheDocument()
    expect(screen.getByTestId('other-row')).toHaveTextContent('other')
  })

  it('runs a query given in the URL and sorts server side from a column header', async () => {
    renderPage('/analyze?run=1&project=7&range=1h&group_by=url.path&calc=count&calc=p95')
    await screen.findAllByTestId('group-row')
    expect(analyze).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByRole('button', { name: 'P95' }))
    await waitFor(() => expect(analyze).toHaveBeenCalledTimes(2))
    expect(analyze.mock.calls[1][0]).toMatchObject({ orderBy: 'p95', asc: false })

    fireEvent.click(screen.getByRole('button', { name: /P95/ }))
    await waitFor(() => expect(analyze).toHaveBeenCalledTimes(3))
    expect(analyze.mock.calls[2][0]).toMatchObject({ orderBy: 'p95', asc: true })
  })

  it('opens the matching traces when a group row is clicked', async () => {
    renderPage('/analyze?run=1&project=7&group_by=url.path')
    const rows = await screen.findAllByTestId('group-row')
    fireEvent.click(rows[0])

    await screen.findByText('traces page')
    const url = new URL(seen.loc, 'http://x')
    expect(url.pathname).toBe('/traces')
    expect(url.searchParams.get('project')).toBe('7')
    expect(JSON.parse(url.searchParams.get('filter') ?? '')).toEqual(drill)
  })

  it('does not open anything from the other row', async () => {
    renderPage('/analyze?run=1&project=7&group_by=url.path')
    await screen.findAllByTestId('group-row')
    fireEvent.click(screen.getByTestId('other-row'))
    expect(screen.queryByText('traces page')).not.toBeInTheDocument()
  })

  it('draws the chart view from the series endpoint', async () => {
    renderPage('/analyze?run=1&project=7&group_by=url.path&view=chart&chart=count&calc=count&calc=p95')
    expect(await screen.findByTestId('chart')).toHaveTextContent('Count per 1h: /a,other')
    expect(analyzeSeries.mock.calls[0][0]).toMatchObject({ calcs: ['count'], groupBy: ['url.path'] })

    fireEvent.change(screen.getByLabelText('Chart calculation'), { target: { value: 'p95' } })
    await waitFor(() => expect(analyzeSeries).toHaveBeenCalledTimes(2))
    expect(analyzeSeries.mock.calls[1][0]).toMatchObject({ calcs: ['p95'] })
    expect(analyze).toHaveBeenCalledTimes(1)
  })

  it('says when the scan was sampled or cut off', async () => {
    analyze.mockResolvedValue(table({ sampleEvery: 8, truncated: true, scanned: 200000 }))
    renderPage('/analyze?run=1&project=7&group_by=url.path')
    const note = await screen.findByTestId('sample-note')
    expect(note).toHaveTextContent('Sampled 1 in 8')
    expect(note).toHaveTextContent('stopped at')
  })

  it('shows the server error', async () => {
    analyze.mockRejectedValue(new Error('invalid filter'))
    renderPage('/analyze?run=1&project=7')
    expect(await screen.findByRole('alert')).toHaveTextContent('invalid filter')
  })

  it('offers to save a result to a board, and not before there is one', async () => {
    renderPage()
    await screen.findByRole('option', { name: 'Profotograaf' })
    expect(screen.queryByRole('button', { name: 'Save to board' })).not.toBeInTheDocument()
    cleanup()
    renderPage('/analyze?run=1&project=7&group_by=url.path&calc=count')
    expect(await screen.findByRole('button', { name: 'Save to board' })).toBeInTheDocument()
  })

  it('adds a distinct count calculation from the attribute box', async () => {
    renderPage()
    await screen.findByRole('option', { name: 'Profotograaf' })
    fireEvent.change(screen.getByLabelText('Distinct values of'), { target: { value: 'user.id' } })
    fireEvent.click(screen.getByRole('button', { name: 'Run query' }))
    await waitFor(() => expect(analyze).toHaveBeenCalled())
    expect(analyze.mock.calls[0][0].calcs).toContain('count_distinct:user.id')
  })
})
