import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within, act } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { BoardPage } from './BoardPage'
import type { Board, BoardPanel } from '../api/types'

vi.mock('../api/client', () => ({
  api: {
    getBoard: vi.fn(),
    updateBoard: vi.fn(),
    deleteBoard: vi.fn(),
    updatePanel: vi.fn(),
    deletePanel: vi.fn(),
    reorderPanels: vi.fn(),
    listReleases: vi.fn(),
    createRelease: vi.fn(),
    analyze: vi.fn(),
    analyzeSeries: vi.fn(),
    getMetricSeries: vi.fn(),
  },
}))

vi.mock('../components/metrics/MetricLines', () => ({
  MetricLines: ({ resp }: { resp: { series: { labels: Record<string, string> }[] } }) => (
    <div data-testid="metric-chart">{resp.series.map((s) => Object.values(s.labels).join('/')).join(',')}</div>
  ),
}))

// Recharts needs layout the test DOM lacks, so the chart is replaced by a probe.
vi.mock('../components/dashboard/SeriesChart', () => ({
  SeriesChart: ({ label, markers }: { label: string; markers?: { label: string }[] }) => (
    <div data-testid="chart">{label}|{(markers ?? []).map((m) => m.label).join(',')}</div>
  ),
}))

import { api } from '../api/client'

const mocked = {
  getBoard: vi.mocked(api.getBoard),
  updateBoard: vi.mocked(api.updateBoard),
  updatePanel: vi.mocked(api.updatePanel),
  deletePanel: vi.mocked(api.deletePanel),
  reorderPanels: vi.mocked(api.reorderPanels),
  listReleases: vi.mocked(api.listReleases),
  createRelease: vi.mocked(api.createRelease),
  analyze: vi.mocked(api.analyze),
  analyzeSeries: vi.mocked(api.analyzeSeries),
  getMetricSeries: vi.mocked(api.getMetricSeries),
}

const query = (id: number, def: Record<string, unknown>, filters: unknown = null) => ({
  id, projectId: 7, name: `q${id}`, service: '', operation: '', status: '', minDurationUs: 0, createdAt: '',
  filters, definition: def,
})

const panels: BoardPanel[] = [
  {
    id: 11, boardId: 5, savedQueryId: 1, title: 'Errors by path', view: 'table', position: 0,
    query: query(1, { groupBy: ['url.path'], calcs: ['count'] }, { match: 'and', filters: [{ key: 'status', op: '=', value: 'error' }] }) as BoardPanel['query'],
  },
  {
    id: 12, boardId: 5, savedQueryId: 2, title: 'P95 by method', view: 'chart', position: 1,
    query: query(2, { groupBy: ['http.request.method'], calcs: ['p95'], chartCalc: 'p95' }) as BoardPanel['query'],
  },
  {
    id: 13, boardId: 5, savedQueryId: 3, title: 'Count by agent', view: 'table', position: 2,
    query: query(3, { groupBy: ['user_agent.original'], calcs: ['count'] }) as BoardPanel['query'],
  },
]

const board = (over: Partial<Board> = {}): Board => ({
  id: 5, projectId: 7, name: 'Overview', timeRange: '24h', refreshSeconds: 0, panels, createdAt: '', updatedAt: '', ...over,
})

const table = (group: string) => ({
  groupBy: ['g'], calcs: ['count'], rows: [{ group: [group], count: 3, values: [3], drill: { match: 'and', filters: [] } }],
  scanned: 3, sampleEvery: 1, truncated: false, maxSpans: 200000,
})

function renderBoard() {
  return render(
    <MemoryRouter initialEntries={['/boards/5']}>
      <Routes>
        <Route path="/boards/:id" element={<BoardPage />} />
        <Route path="/boards" element={<div>boards list</div>} />
        <Route path="/analyze" element={<div>query page</div>} />
      </Routes>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  mocked.getBoard.mockResolvedValue(board())
  mocked.updateBoard.mockResolvedValue({ status: 'ok' })
  mocked.updatePanel.mockResolvedValue({ status: 'ok' })
  mocked.deletePanel.mockResolvedValue({ status: 'ok' })
  mocked.reorderPanels.mockResolvedValue({ status: 'ok' })
  mocked.createRelease.mockResolvedValue({ id: 1 })
  mocked.listReleases.mockResolvedValue([])
  mocked.analyze.mockImplementation(async (p) => table(p.groupBy[0]))
  mocked.analyzeSeries.mockResolvedValue({
    groupBy: ['http.request.method'], calc: 'p95', bucketSeconds: 3600, buckets: [3600], series: [{ group: ['GET'], values: [5] }],
    scanned: 1, sampleEvery: 1, truncated: false, maxSpans: 1,
  })
})

afterEach(() => {
  vi.useRealTimers()
})

describe('BoardPage', () => {
  it('draws a metric panel from the metric series of the board project', async () => {
    const metricPanel: BoardPanel = {
      id: 21, boardId: 5, savedQueryId: 9, title: 'Rows deleted', view: 'metric', position: 0,
      query: query(9, { groupBy: [], calcs: [], metric: { name: 'spanbarn.retention.deleted', groupBy: ['table'] } }) as BoardPanel['query'],
    }
    mocked.getBoard.mockResolvedValue(board({ name: 'SpanBarn storage', panels: [metricPanel] }))
    mocked.getMetricSeries.mockResolvedValue({
      name: 'spanbarn.retention.deleted', type: 'sum', unit: '', render: 'line', step_seconds: 0,
      series: [{ labels: { table: 'logs' }, points: [{ t: 1, value: 4, count: 1 }] }],
    })

    renderBoard()
    const card = await screen.findByTestId('panel')
    expect(await within(card).findByTestId('metric-chart')).toHaveTextContent('logs')

    const [name, from, to, , , projectId, groupBy] = mocked.getMetricSeries.mock.calls[0]
    expect([name, projectId, groupBy]).toEqual(['spanbarn.retention.deleted', 7, ['table']])
    expect(Date.parse(to) - Date.parse(from)).toBe(24 * 3600_000)
    expect(mocked.analyze).not.toHaveBeenCalled()
    expect(mocked.analyzeSeries).not.toHaveBeenCalled()
    // Span-only actions do not apply to a metric.
    expect(within(card).queryByRole('button', { name: /Show (chart|table)/ })).toBeNull()
    expect(within(card).queryByRole('link', { name: 'Open in Query' })).toBeNull()
  })

  it('shows three panels that run their saved queries over one window', async () => {
    renderBoard()
    const cards = await screen.findAllByTestId('panel')
    expect(cards.map((c) => within(c).getByRole('heading').textContent)).toEqual(['Errors by path', 'P95 by method', 'Count by agent'])

    await waitFor(() => expect(mocked.analyze).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(mocked.analyzeSeries).toHaveBeenCalledTimes(1))

    const [errors, agents] = mocked.analyze.mock.calls.map((c) => c[0])
    expect(errors.groupBy).toEqual(['url.path'])
    expect(JSON.parse(errors.filter!)).toEqual({ match: 'and', filters: [{ key: 'status', op: '=', value: 'error' }] })
    expect(agents.groupBy).toEqual(['user_agent.original'])
    const series = mocked.analyzeSeries.mock.calls[0][0]
    expect(series.calcs).toEqual(['p95'])

    // One window for all of them.
    expect(new Set([errors.from, agents.from, series.from]).size).toBe(1)
    expect(new Set([errors.to, agents.to, series.to]).size).toBe(1)
    expect(Date.parse(errors.to) - Date.parse(errors.from)).toBe(24 * 3600_000)

    expect(await within(cards[0]).findByText('3')).toBeInTheDocument()
    expect(within(cards[1]).getByTestId('chart')).toHaveTextContent('P95')
  })

  it('changing the shared range reruns every panel and saves it', async () => {
    renderBoard()
    await screen.findAllByTestId('panel')
    await waitFor(() => expect(mocked.analyze).toHaveBeenCalledTimes(2))
    mocked.analyze.mockClear()
    mocked.analyzeSeries.mockClear()

    fireEvent.change(screen.getByRole('combobox', { name: 'Time range' }), { target: { value: '1h' } })

    await waitFor(() => expect(mocked.analyze).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(mocked.analyzeSeries).toHaveBeenCalledTimes(1))
    for (const call of [...mocked.analyze.mock.calls, ...mocked.analyzeSeries.mock.calls]) {
      expect(Date.parse(call[0].to) - Date.parse(call[0].from)).toBe(3600_000)
    }
    expect(mocked.updateBoard).toHaveBeenCalledWith(5, { name: 'Overview', timeRange: '1h', refreshSeconds: 0 })
  })

  it('refreshes every panel on the board interval', async () => {
    mocked.getBoard.mockResolvedValue(board({ refreshSeconds: 30 }))
    vi.useFakeTimers({ shouldAdvanceTime: true })
    renderBoard()
    await screen.findAllByTestId('panel')
    await waitFor(() => expect(mocked.analyze).toHaveBeenCalledTimes(2))
    const firstTo = mocked.analyze.mock.calls[0][0].to

    await act(async () => { await vi.advanceTimersByTimeAsync(30_000) })
    await waitFor(() => expect(mocked.analyze).toHaveBeenCalledTimes(4))
    expect(mocked.analyzeSeries).toHaveBeenCalledTimes(2)
    expect(mocked.analyze.mock.calls[2][0].to).not.toBe(firstTo)
  })

  it('draws release markers on chart panels', async () => {
    mocked.listReleases.mockImplementation(async () => [{ id: 1, projectId: 7, version: 'v0.4.0', releasedAt: new Date(Date.now() - 3600_000).toISOString() }])
    renderBoard()
    const chart = await screen.findByTestId('chart')
    await waitFor(() => expect(chart).toHaveTextContent('|v0.4.0'))
    expect(mocked.listReleases.mock.calls[0][0]).toBe(7)
  })

  it('marks a release and reloads the markers', async () => {
    renderBoard()
    await screen.findAllByTestId('panel')
    fireEvent.change(screen.getByLabelText('Release version'), { target: { value: ' v1.0.0 ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Mark release' }))
    await waitFor(() => expect(mocked.createRelease).toHaveBeenCalledWith(7, 'v1.0.0'))
    await waitFor(() => expect(mocked.listReleases.mock.calls.length).toBeGreaterThan(1))
  })

  it('moves, switches, renames and removes panels', async () => {
    renderBoard()
    await screen.findAllByTestId('panel')

    fireEvent.click(screen.getByRole('button', { name: 'Move Errors by path later' }))
    await waitFor(() => expect(mocked.reorderPanels).toHaveBeenCalledWith(5, [12, 11, 13]))
    expect(screen.getAllByTestId('panel').map((c) => within(c).getByRole('heading').textContent)).toEqual(['P95 by method', 'Errors by path', 'Count by agent'])

    fireEvent.click(within(screen.getByRole('region', { name: 'Count by agent' })).getByRole('button', { name: 'Show chart' }))
    await waitFor(() => expect(mocked.updatePanel).toHaveBeenCalledWith(5, 13, 'Count by agent', 'chart'))

    fireEvent.click(screen.getByRole('button', { name: 'Rename Errors by path' }))
    const input = screen.getByLabelText('Panel title')
    fireEvent.change(input, { target: { value: 'Errors' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(mocked.updatePanel).toHaveBeenCalledWith(5, 11, 'Errors', 'table'))

    fireEvent.click(screen.getByRole('button', { name: 'Remove Count by agent' }))
    await waitFor(() => expect(mocked.deletePanel).toHaveBeenCalledWith(5, 13))
  })

  it('offers the query page when the board is empty', async () => {
    mocked.getBoard.mockResolvedValue(board({ panels: [] }))
    renderBoard()
    expect(await screen.findByText(/This board has no panels/)).toBeInTheDocument()
    expect(mocked.analyze).not.toHaveBeenCalled()
  })

  it('shows a panel error without taking the board down', async () => {
    mocked.analyze.mockImplementation(async (p) => {
      if (p.groupBy[0] === 'url.path') throw new Error('query failed')
      return table(p.groupBy[0])
    })
    renderBoard()
    expect(await screen.findByRole('alert')).toHaveTextContent('query failed')
    expect(await screen.findByText('3')).toBeInTheDocument()
  })

  it('reports a board that cannot load', async () => {
    mocked.getBoard.mockRejectedValue(new Error('not found'))
    renderBoard()
    expect(await screen.findByRole('alert')).toHaveTextContent('not found')
  })
})
