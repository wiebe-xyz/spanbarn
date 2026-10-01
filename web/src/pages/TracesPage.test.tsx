import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { TracesPage } from './TracesPage'

vi.mock('../api/client', () => ({
  api: {
    getSavedQueries: vi.fn().mockResolvedValue([]),
    listTraceExclusions: vi.fn().mockResolvedValue([]),
    getTraceGroups: vi.fn().mockResolvedValue([]),
    getExportUrl: vi.fn().mockReturnValue('/export'),
  },
}))

const trace = (over: Record<string, unknown>) => ({
  traceId: 'a'.repeat(32),
  rootSpanName: 'GET /ok',
  rootService: 'web',
  durationUs: 1000,
  spanCount: 2,
  status: 'ok',
  startTime: '2026-10-01T10:00:00Z',
  hasRoot: true,
  orphanCount: 0,
  ...over,
})

let tracesUrls: string[]

beforeEach(() => {
  tracesUrls = []
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.startsWith('/api/v1/traces?')) {
        tracesUrls.push(url)
        return Promise.resolve(new Response(JSON.stringify([
          trace({}),
          trace({ traceId: 'b'.repeat(32), rootSpanName: '', rootService: '', hasRoot: false, orphanCount: 2 }),
        ])))
      }
      return Promise.resolve(new Response('[]'))
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('TracesPage structure', () => {
  it('badges rootless traces and leaves healthy ones alone', async () => {
    render(
      <MemoryRouter initialEntries={['/traces?structure=orphans']}>
        <TracesPage />
      </MemoryRouter>,
    )
    // Loading the exclusions re-runs the search, so assert once the list settles.
    await waitFor(() => {
      expect(screen.getByText('no root span')).toBeInTheDocument()
      expect(screen.getByText('2 orphan spans')).toBeInTheDocument()
      expect(screen.getByText('GET /ok')).toBeInTheDocument()
    })
    expect(tracesUrls[0]).toContain('orphans=true')
  })

  it('sends has_root=false for the rootless filter', async () => {
    render(
      <MemoryRouter initialEntries={['/traces?structure=rootless']}>
        <TracesPage />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByText('no root span')).toBeInTheDocument())
    expect(tracesUrls[0]).toContain('has_root=false')
    expect(tracesUrls[0]).not.toContain('orphans=true')

    fireEvent.change(screen.getByLabelText('Structure'), { target: { value: '' } })
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    await waitFor(() => expect(tracesUrls.length).toBeGreaterThan(1))
    const last = tracesUrls[tracesUrls.length - 1]
    expect(last).not.toContain('has_root')
    expect(last).not.toContain('orphans')
  })
})
