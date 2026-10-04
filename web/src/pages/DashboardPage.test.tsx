import { describe, it, expect, vi, beforeEach, onTestFinished } from 'vitest'
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
  // A drag runs from three hours ago to two hours ago, whatever the window.
  LineChart: ({ children, onMouseDown, onMouseMove, onMouseUp }: {
    children: React.ReactNode
    onMouseDown?: (e: { activeLabel: number }) => void
    onMouseMove?: (e: { activeLabel: number }) => void
    onMouseUp?: () => void
  }) => (
    <div
      data-testid="line-chart"
      onMouseDown={() => onMouseDown?.({ activeLabel: Date.now() - 3 * 3600_000 })}
      onMouseMove={() => onMouseMove?.({ activeLabel: Date.now() - 2 * 3600_000 })}
      onMouseUp={() => onMouseUp?.()}
    >
      {children}
    </div>
  ),
  ReferenceArea: () => <span data-testid="brush-area" />,
  ReferenceLine: () => null,
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
    // Freeze Date only (waitFor keeps real timers): the live and the previous
    // window are computed from Date.now() at different renders, which a loaded
    // CI host can put seconds apart.
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-10-04T12:00:00Z'))
    onTestFinished(() => { vi.useRealTimers() })
    renderPage()
    await waitFor(() => expect(counts).toHaveBeenCalledTimes(2))
    const live = counts.mock.calls[0][0]

    fireEvent.click(screen.getByRole('button', { name: 'Previous window' }))
    await waitFor(() => expect(counts).toHaveBeenCalledTimes(4))
    const earlier = counts.mock.calls[2][0]
    expect(earlier.to).toBe(live.from)
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

describe('DashboardPage zoom and filters', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    counts.mockImplementation(async () => ({
      intervalSeconds: 900,
      points: [{ time: T0, group: 'web', count: 5 }, { time: T0, group: 'worker', count: 4 }],
    }))
    percentiles.mockResolvedValue({
      intervalSeconds: 900,
      points: [{ time: T0, group: 'web', count: 5, p90Us: 9000, p95Us: 12000, p99Us: 30000 }],
    })
    heatmap.mockResolvedValue({ intervalSeconds: 900, cells: [{ time: T0, bucket: 10, lowerUs: 31, upperUs: 45, count: 3 }] })
  })

  function drag(chart: HTMLElement) {
    fireEvent.mouseDown(chart)
    fireEvent.mouseMove(chart)
    fireEvent.mouseUp(chart)
  }

  it('zooms every card to the dragged range and shows the zoom controls', async () => {
    renderPage()
    await waitFor(() => expect(within(card('Trace Counts by Service')).getAllByTestId('line-chart')).toHaveLength(1))
    expect(screen.queryByRole('group', { name: 'Active zoom and filters' })).toBeNull()

    drag(within(card('Trace Counts by Service')).getByTestId('line-chart'))

    await waitFor(() => expect(screen.getByRole('group', { name: 'Active zoom and filters' })).toHaveTextContent('Zoomed'))
    const filter = counts.mock.calls.at(-1)![0]
    expect(Date.parse(filter.to) - Date.parse(filter.from)).toBeCloseTo(3600_000, -3)
    expect(heatmap.mock.calls.at(-1)![0].from).toBe(filter.from)
    expect(percentiles.mock.calls.at(-1)![0].to).toBe(filter.to)
    expect(screen.getByRole('combobox', { name: 'Time range' })).toHaveValue('custom')
    expect(screen.getByRole('button', { name: 'Previous window' })).toBeDisabled()
  })

  it('restores a zoomed window from the URL, zooms out and resets', async () => {
    const to = Date.now() - 3600_000
    const from = to - 600_000
    renderPage(`/?from=${from}&to=${to}`)
    await waitFor(() => expect(counts).toHaveBeenCalled())
    const first = counts.mock.calls[0][0]
    expect(Date.parse(first.from)).toBe(from)
    expect(Date.parse(first.to)).toBe(to)

    fireEvent.click(screen.getByRole('button', { name: 'Zoom out' }))
    await waitFor(() => {
      const f = counts.mock.calls.at(-1)![0]
      expect(Date.parse(f.to) - Date.parse(f.from)).toBe(1_200_000)
    })

    fireEvent.click(screen.getByRole('button', { name: 'Reset zoom' }))
    await waitFor(() => {
      const f = counts.mock.calls.at(-1)![0]
      expect(Date.parse(f.to) - Date.parse(f.from)).toBe(24 * 3600_000)
    })
    expect(screen.queryByRole('group', { name: 'Active zoom and filters' })).toBeNull()
  })

  it('filters to a series when its legend entry is clicked, and removes the chip again', async () => {
    renderPage()
    await waitFor(() => expect(within(card('Trace Counts by Service')).getByRole('button', { name: 'web' })).toBeInTheDocument())

    fireEvent.click(within(card('Trace Counts by Service')).getByRole('button', { name: 'web' }))
    await waitFor(() => expect(counts.mock.calls.at(-1)![0].service).toBe('web'))
    expect(screen.getByRole('button', { name: 'Remove service = web' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Remove service = web' }))
    await waitFor(() => expect(counts.mock.calls.at(-1)![0].service).toBe(''))
    expect(screen.queryByRole('group', { name: 'Active zoom and filters' })).toBeNull()
  })

  it('filters on span name from the duration card legend', async () => {
    renderPage()
    await waitFor(() => expect(within(card('Duration by Name')).getAllByRole('button', { name: 'web' }).length).toBeGreaterThan(0))
    fireEvent.click(within(card('Duration by Name')).getAllByRole('button', { name: 'web' })[0])
    await waitFor(() => expect(percentiles.mock.calls.at(-1)![0].name).toBe('web'))
  })

  it('regroups the counts card and filters on that dimension', async () => {
    renderPage()
    await waitFor(() => expect(counts).toHaveBeenCalled())
    fireEvent.change(within(card('Trace Counts by Service')).getByRole('combobox', { name: 'Group by' }), { target: { value: 'name' } })

    await waitFor(() => expect(card('Trace Counts by Span name')).toBeInTheDocument())
    await waitFor(() => expect(counts.mock.calls.some(([, g, root]) => g === 'name' && root)).toBe(true))

    fireEvent.click(within(card('Trace Counts by Span name')).getByRole('button', { name: 'web' }))
    await waitFor(() => expect(counts.mock.calls.at(-1)![0].name).toBe('web'))
  })

  it('sends the duration band from the URL and clears it from its chip', async () => {
    renderPage('/?min_us=1000&max_us=5000')
    await waitFor(() => expect(counts).toHaveBeenCalled())
    expect(counts.mock.calls[0][0]).toMatchObject({ minDurationUs: 1000, maxDurationUs: 5000 })
    expect(heatmap.mock.calls[0][0]).toMatchObject({ minDurationUs: 1000, maxDurationUs: 5000 })

    fireEvent.click(screen.getByRole('button', { name: /^Remove duration/ }))
    await waitFor(() => expect(counts.mock.calls.at(-1)![0].minDurationUs).toBe(0))
  })

  it('offers Clear all once several filters are active', async () => {
    renderPage('/?service=web&status=error')
    await waitFor(() => expect(counts).toHaveBeenCalled())
    fireEvent.click(screen.getByRole('button', { name: 'Clear all filters' }))
    await waitFor(() => expect(counts.mock.calls.at(-1)![0]).toMatchObject({ service: '', status: '' }))
  })
})
