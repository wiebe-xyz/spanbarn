import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { HeatmapPage } from './HeatmapPage'

vi.mock('../api/client', () => ({
  api: {
    listProjects: vi.fn(),
    getAttributes: vi.fn(),
    getHeatmap: vi.fn(),
  },
}))

import { api } from '../api/client'

const heatmap = vi.mocked(api.getHeatmap)

function Where() {
  const loc = useLocation()
  return <div data-testid="where">{loc.pathname + loc.search}</div>
}

function renderPage(url = '/heatmap?project=7&range=1h') {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="/heatmap" element={<HeatmapPage />} />
        <Route path="/compare" element={<Where />} />
      </Routes>
    </MemoryRouter>,
  )
}

const RESULT = {
  from: '2026-10-03T10:00:00Z',
  to: '2026-10-03T11:00:00Z',
  timeBuckets: 4,
  durationBuckets: 3,
  bucketMicros: 900_000_000,
  durationEdgesUs: [10, 100, 1000, 10000],
  cells: [
    { time: 0, duration: 0, count: 50 },
    { time: 3, duration: 2, count: 4 },
  ],
  scanned: 54,
  capped: false,
  maxSpans: 20000,
}

function stubGeometry() {
  const grid = screen.getByTestId('heatmap-grid')
  grid.getBoundingClientRect = () => ({ left: 0, top: 0, width: 400, height: 300, right: 400, bottom: 300, x: 0, y: 0, toJSON: () => ({}) })
  return grid
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listProjects).mockResolvedValue([{ id: 7, slug: 'profotograaf', name: 'Profotograaf', status: 'active' }])
  vi.mocked(api.getAttributes).mockResolvedValue({ scanned: 0, sample: 1, truncated: false, maxSpans: 0, keys: [] })
  heatmap.mockResolvedValue(RESULT)
})

describe('HeatmapPage', () => {
  it('loads the heatmap for the project and range in the URL', async () => {
    renderPage()
    await screen.findByTestId('heatmap-grid')
    const scope = heatmap.mock.calls[0][0]
    expect(scope.projectId).toBe(7)
    expect(scope.filter).toBeUndefined()
    expect(new Date(scope.to).getTime() - new Date(scope.from).getTime()).toBe(3600_000)
    expect(screen.queryByTestId('heatmap-capped')).toBeNull()
  })

  it('says when the scan was capped', async () => {
    heatmap.mockResolvedValue({ ...RESULT, capped: true })
    renderPage()
    expect(await screen.findByTestId('heatmap-capped')).toHaveTextContent('newest 20.0K spans')
  })

  it('shows an empty state without spans', async () => {
    heatmap.mockResolvedValue({ ...RESULT, cells: [], durationEdgesUs: [], scanned: 0 })
    renderPage()
    expect(await screen.findByText('No spans in this range.')).toBeInTheDocument()
    expect(screen.queryByTestId('heatmap-grid')).toBeNull()
  })

  it('shows the error of a failed request', async () => {
    heatmap.mockRejectedValue(new Error('range is limited'))
    renderPage()
    expect(await screen.findByRole('alert')).toHaveTextContent('range is limited')
  })

  it('opens the comparison with the dragged rectangle as selection', async () => {
    renderPage()
    await screen.findByTestId('heatmap-grid')
    const grid = stubGeometry()

    // 400x300 px, 4 columns of 100px and 3 rows of 100px. Top row is the slowest duration bucket.
    fireEvent.mouseDown(grid, { clientX: 150, clientY: 20 })
    fireEvent.mouseMove(grid, { clientX: 390, clientY: 40 })
    expect(screen.getByTestId('heatmap-selection')).toBeInTheDocument()
    fireEvent.mouseUp(grid)

    const where = await screen.findByTestId('where')
    const u = new URL(where.textContent ?? '', 'http://x')
    expect(u.pathname).toBe('/compare')
    expect(u.searchParams.get('project')).toBe('7')
    expect(u.searchParams.get('range')).toBe('1h')
    const selection = JSON.parse(u.searchParams.get('selection') ?? '')
    const keys = selection.filters.map((f: { key: string; op: string }) => `${f.key} ${f.op}`)
    expect(keys).toEqual(['duration_us >=', 'start_time_us >='])
    expect(selection.filters[0].value).toBe('1000')
    expect(u.searchParams.has('baseline')).toBe(false)
  })

  it('refuses a rectangle that covers the whole grid', async () => {
    renderPage()
    await screen.findByTestId('heatmap-grid')
    const grid = stubGeometry()
    fireEvent.mouseDown(grid, { clientX: 1, clientY: 1 })
    fireEvent.mouseMove(grid, { clientX: 399, clientY: 299 })
    fireEvent.mouseUp(grid)
    expect(await screen.findByTestId('heatmap-note')).toBeInTheDocument()
    expect(screen.queryByTestId('where')).toBeNull()
  })

  it('cancels the drag when the pointer leaves the grid', async () => {
    renderPage()
    await screen.findByTestId('heatmap-grid')
    const grid = stubGeometry()
    fireEvent.mouseDown(grid, { clientX: 150, clientY: 150 })
    await waitFor(() => expect(screen.getByTestId('heatmap-selection')).toBeInTheDocument())
    fireEvent.mouseLeave(grid)
    expect(screen.queryByTestId('heatmap-selection')).toBeNull()
  })
})
