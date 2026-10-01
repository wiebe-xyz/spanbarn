import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { TraceHealthPage } from './TraceHealthPage'

vi.mock('../api/client', () => ({
  api: {
    listProjects: vi.fn(),
    getOrphanSpans: vi.fn(),
    getRootlessTraces: vi.fn(),
    getSingleSpanTraces: vi.fn(),
    getSpanNames: vi.fn(),
  },
}))

import { api } from '../api/client'

const orphans = vi.mocked(api.getOrphanSpans)
const rootless = vi.mocked(api.getRootlessTraces)
const single = vi.mocked(api.getSingleSpanTraces)
const names = vi.mocked(api.getSpanNames)

function renderPage(url = '/trace-health') {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <TraceHealthPage />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listProjects).mockResolvedValue([
    { id: 7, slug: 'profotograaf', name: 'Profotograaf', status: 'active' },
  ])
  orphans.mockResolvedValue([
    { name: 'POST /api/v1/library/presign', kind: 'server', service: 'web', count: 52, sampleTraceId: 'abcdef0123456789abcdef0123456789' },
  ])
  rootless.mockResolvedValue({
    total: 2,
    traces: [
      { traceId: 'r1'.padEnd(32, '0'), rootSpanName: '', rootService: '', durationUs: 0, spanCount: 3, status: 'ok', startTime: '2026-10-01T10:00:00Z', hasRoot: false, orphanCount: 1 },
    ],
  })
  single.mockResolvedValue([
    { name: 'outreach.leadgen.POST /api/campaigns/{id}/stop', service: 'outreach', count: 518, sampleTraceId: 's1'.padEnd(32, '0') },
  ])
  names.mockResolvedValue([
    { name: 'GET /health', count: 10, rootCount: 10 },
    { name: 'sql.conn.query', count: 36, rootCount: 0 },
    { name: 'mixed', count: 4, rootCount: 1 },
  ])
})

describe('TraceHealthPage', () => {
  it('queries the first project with a required, bounded range', async () => {
    renderPage()
    expect(await screen.findByText('POST /api/v1/library/presign')).toBeInTheDocument()
    expect(orphans).toHaveBeenCalledTimes(1)
    const scope = orphans.mock.calls[0][0]
    expect(scope.projectId).toBe(7)
    const widthMs = new Date(scope.to).getTime() - new Date(scope.from).getTime()
    expect(widthMs).toBe(24 * 3600_000)
    expect(screen.getByText('52')).toBeInTheDocument()
  })

  it('shows the rootless traces with the no-root badge', async () => {
    renderPage('/trace-health?tab=rootless&project=7')
    expect(await screen.findByText(/2 rootless traces in this window/)).toBeInTheDocument()
    expect(screen.getByText('no root span')).toBeInTheDocument()
    expect(screen.getByText('1 orphan span')).toBeInTheDocument()
    expect(rootless).toHaveBeenCalledWith(expect.objectContaining({ projectId: 7 }))
  })

  it('switches tabs and loads the single-span and span-name views', async () => {
    renderPage()
    await screen.findByText('POST /api/v1/library/presign')

    fireEvent.click(screen.getByRole('tab', { name: 'Single-span traces' }))
    expect(await screen.findByText('outreach.leadgen.POST /api/campaigns/{id}/stop')).toBeInTheDocument()
    expect(screen.getByText('518')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('tab', { name: 'Span names' }))
    expect(await screen.findByText('sql.conn.query')).toBeInTheDocument()
    expect(screen.getByText('entry point')).toBeInTheDocument()
    expect(screen.getByText('internal')).toBeInTheDocument()
    expect(screen.getByText('mixed', { selector: 'td:last-child' })).toBeInTheDocument()
  })

  it('requests the chosen range', async () => {
    renderPage()
    await screen.findByText('POST /api/v1/library/presign')
    fireEvent.change(screen.getByLabelText('Time range'), { target: { value: '7d' } })
    await waitFor(() => expect(orphans).toHaveBeenCalledTimes(2))
    const scope = orphans.mock.calls[1][0]
    expect(new Date(scope.to).getTime() - new Date(scope.from).getTime()).toBe(168 * 3600_000)
  })

  it('shows the server error', async () => {
    orphans.mockRejectedValueOnce(new Error('HTTP 400: range is limited'))
    renderPage()
    expect(await screen.findByRole('alert')).toHaveTextContent('HTTP 400: range is limited')
  })
})
