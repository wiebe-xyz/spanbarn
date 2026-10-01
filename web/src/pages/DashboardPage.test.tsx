import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { DashboardPage } from './DashboardPage'

vi.mock('../api/client', () => ({
  api: {
    getDashboardCounts: vi.fn(),
    getDashboardPercentiles: vi.fn(),
    getDashboardHeatmap: vi.fn(),
    getServices: vi.fn().mockResolvedValue([{ service: 'web' }, { service: 'worker' }]),
    listProjects: vi.fn().mockResolvedValue([{ id: 3, slug: 'shop', name: 'Shop', status: 'active' }]),
  },
}))

// jsdom cannot lay out recharts, so draw each line as a marker we can count.
vi.mock('recharts', () => ({
  LineChart: ({ children }: { children: React.ReactNode }) => <div data-testid="line-chart">{children}</div>,
  Line: ({ name }: { name: string }) => <span data-testid="line">{name}</span>,
  XAxis: () => null,
  YAxis: () => null,
  Tooltip: () => null,
  CartesianGrid: () => null,
  ResponsiveContainer: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}))

import { api } from '../api/client'

const counts = vi.mocked(api.getDashboardCounts)
const percentiles = vi.mocked(api.getDashboardPercentiles)
const heatmap = vi.mocked(api.getDashboardHeatmap)

const T0 = '2026-10-01T10:00:00.000Z'

function renderPage(url = '/') {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <DashboardPage />
    </MemoryRouter>,
  )
}

function card(title: string) {
  return screen.getByRole('region', { name: title })
}

describe('DashboardPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    counts.mockImplementation(async (_f, groupBy) => ({
      intervalSeconds: 900,
      points:
        groupBy === 'http_status'
          ? [{ time: T0, group: '200', count: 8 }, { time: T0, group: '500', count: 1 }]
          : [{ time: T0, group: 'web', count: 5 }, { time: T0, group: 'worker', count: 4 }],
    }))
    percentiles.mockResolvedValue({
      intervalSeconds: 900,
      points: [{ time: T0, group: 'web', count: 5, p90Us: 9000, p95Us: 12000, p99Us: 30000 }],
    })
    heatmap.mockResolvedValue({
      intervalSeconds: 900,
      cells: [{ time: T0, bucket: 10, lowerUs: 31, upperUs: 45, count: 3 }],
    })
  })

  it('renders every card of the default dashboard', async () => {
    renderPage()
    for (const title of [
      'Trace Counts by Service',
      'Trace Counts by HTTP Status Code',
      'Trace Duration Heatmap',
      'Duration Heatmap',
      'Duration by Service',
      'Duration by Name',
    ]) {
      expect(card(title)).toBeInTheDocument()
    }
    await waitFor(() => expect(within(card('Trace Counts by Service')).getAllByTestId('line')).toHaveLength(2))
  })

  it('draws P99, P95 and P90 charts in each duration card', async () => {
    renderPage()
    await waitFor(() => expect(within(card('Duration by Service')).getAllByTestId('line-chart')).toHaveLength(3))
    const labels = within(card('Duration by Service')).getAllByText(/^P\d\d\(duration\)$/).map((e) => e.textContent)
    expect(labels).toEqual(['P99(duration)', 'P95(duration)', 'P90(duration)'])
  })

  it('draws a line per HTTP status code', async () => {
    renderPage()
    await waitFor(() => {
      const lines = within(card('Trace Counts by HTTP Status Code')).getAllByTestId('line').map((e) => e.textContent)
      expect(lines).toEqual(['200', '500'])
    })
  })

  it('queries root spans for trace cards and all spans for the span heatmap', async () => {
    renderPage()
    await waitFor(() => expect(heatmap).toHaveBeenCalledTimes(2))
    expect(counts.mock.calls.map(([, g, root]) => [g, root])).toEqual(
      expect.arrayContaining([['service', true], ['http_status', true]]),
    )
    expect(heatmap.mock.calls.map(([, root]) => root).sort()).toEqual([false, true])
    expect(percentiles.mock.calls.map(([, g]) => g).sort()).toEqual(['name', 'service'])
  })

  it('defaults to the last day and sends an ISO window of that width', async () => {
    renderPage()
    await waitFor(() => expect(counts).toHaveBeenCalled())
    const filter = counts.mock.calls[0][0]
    expect(Date.parse(filter.to) - Date.parse(filter.from)).toBe(24 * 3600_000)
    expect(screen.getByRole('combobox', { name: 'Time range' })).toHaveValue('24h')
  })

  it('restores range, offset and filters from the URL', async () => {
    renderPage('/?range=4h&offset=2&service=web&status=error&name=GET%20%2F&project=3')
    await waitFor(() => expect(counts).toHaveBeenCalled())
    const filter = counts.mock.calls[0][0]
    expect(filter).toMatchObject({ service: 'web', status: 'error', name: 'GET /', projectId: 3 })
    expect(Date.parse(filter.to) - Date.parse(filter.from)).toBe(4 * 3600_000)
    expect(Date.now() - Date.parse(filter.to)).toBeGreaterThanOrEqual(8 * 3600_000 - 5000)
    expect(screen.getByRole('combobox', { name: 'Span status' })).toHaveValue('error')
    expect(screen.getByRole('textbox', { name: 'Span name' })).toHaveValue('GET /')
  })

  it('refetches every card when a filter changes', async () => {
    renderPage()
    await waitFor(() => expect(counts).toHaveBeenCalledTimes(2))
    fireEvent.change(screen.getByRole('combobox', { name: 'Span status' }), { target: { value: 'error' } })
    await waitFor(() => expect(counts).toHaveBeenCalledTimes(4))
    expect(counts.mock.calls[2][0].status).toBe('error')
    expect(heatmap.mock.calls.at(-1)?.[0].status).toBe('error')
    expect(percentiles.mock.calls.at(-1)?.[0].status).toBe('error')
  })

  it('applies the span name filter on Enter, not on every keystroke', async () => {
    renderPage()
    await waitFor(() => expect(counts).toHaveBeenCalledTimes(2))
    const input = screen.getByRole('textbox', { name: 'Span name' })
    fireEvent.change(input, { target: { value: 'GET /orders' } })
    expect(counts).toHaveBeenCalledTimes(2)
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(counts).toHaveBeenCalledTimes(4))
    expect(counts.mock.calls[2][0].name).toBe('GET /orders')
  })

  it('moves the window back and forward with the arrows', async () => {
    renderPage()
    await waitFor(() => expect(counts).toHaveBeenCalledTimes(2))
    const live = counts.mock.calls[0][0]

    fireEvent.click(screen.getByRole('button', { name: 'Previous window' }))
    await waitFor(() => expect(counts).toHaveBeenCalledTimes(4))
    const earlier = counts.mock.calls[2][0]
    expect(Date.parse(live.from) - Date.parse(earlier.to)).toBeLessThan(5000)
    expect(Date.parse(earlier.to)).toBeLessThan(Date.parse(live.from) + 5000)
    expect(screen.getByRole('button', { name: 'Next window' })).toBeEnabled()

    fireEvent.click(screen.getByRole('button', { name: 'Next window' }))
    await waitFor(() => expect(counts).toHaveBeenCalledTimes(6))
    expect(screen.getByRole('button', { name: 'Next window' })).toBeDisabled()
  })

  it('resets to the live window when the range changes', async () => {
    renderPage('/?offset=3')
    await waitFor(() => expect(counts).toHaveBeenCalled())
    fireEvent.change(screen.getByRole('combobox', { name: 'Time range' }), { target: { value: '1h' } })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Next window' })).toBeDisabled())
    const filter = counts.mock.calls.at(-1)![0]
    expect(Date.parse(filter.to) - Date.parse(filter.from)).toBe(3600_000)
    expect(Date.now() - Date.parse(filter.to)).toBeLessThan(5000)
  })

  it('shows an error on the failing card and keeps the others', async () => {
    percentiles.mockImplementation(async (_f, groupBy) => {
      if (groupBy === 'name') throw new Error('query failed')
      return { intervalSeconds: 900, points: [{ time: T0, group: 'web', count: 5, p90Us: 1, p95Us: 2, p99Us: 3 }] }
    })
    renderPage()
    await waitFor(() => expect(within(card('Duration by Name')).getByRole('alert')).toHaveTextContent('query failed'))
    expect(within(card('Duration by Service')).queryByRole('alert')).toBeNull()
    await waitFor(() => expect(within(card('Trace Counts by Service')).getAllByTestId('line').length).toBeGreaterThan(0))
  })

  it('shows an empty state when a window has no data', async () => {
    counts.mockResolvedValue({ intervalSeconds: 900, points: [] })
    heatmap.mockResolvedValue({ intervalSeconds: 900, cells: [] })
    percentiles.mockResolvedValue({ intervalSeconds: 900, points: [] })
    renderPage()
    await waitFor(() => expect(within(card('Trace Counts by Service')).getByText('No data in this range')).toBeInTheDocument())
    expect(within(card('Duration Heatmap')).getByText('No data in this range')).toBeInTheDocument()
    expect(within(card('Duration by Service')).getByText('No data in this range')).toBeInTheDocument()
  })

  it('shows skeletons while the first response is pending', () => {
    counts.mockReturnValue(new Promise(() => {}))
    renderPage()
    expect(within(card('Trace Counts by Service')).getByTestId('card-skeleton')).toBeInTheDocument()
  })

  it('does not draw a stale response over a newer one', async () => {
    let resolveSlow: (v: Awaited<ReturnType<typeof api.getDashboardCounts>>) => void = () => {}
    counts.mockReset()
    counts.mockImplementationOnce(() => new Promise((r) => { resolveSlow = r }))
    counts.mockImplementation(async () => ({ intervalSeconds: 900, points: [{ time: T0, group: 'fresh', count: 1 }] }))
    renderPage()
    fireEvent.change(screen.getByRole('combobox', { name: 'Span status' }), { target: { value: 'error' } })
    await waitFor(() => expect(within(card('Trace Counts by Service')).getByTestId('line')).toHaveTextContent('fresh'))
    resolveSlow({ intervalSeconds: 900, points: [{ time: T0, group: 'stale', count: 1 }] })
    await new Promise((r) => setTimeout(r, 20))
    expect(within(card('Trace Counts by Service')).getByTestId('line')).toHaveTextContent('fresh')
  })
})
