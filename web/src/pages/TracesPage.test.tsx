import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { TracesPage } from './TracesPage'
import { api } from '../api/client'

vi.mock('../api/client', () => ({
  api: {
    getSavedQueries: vi.fn().mockResolvedValue([]),
    listProjects: vi.fn().mockResolvedValue([{ id: 1, name: 'p' }]),
    getAttributes: vi.fn().mockResolvedValue({ scanned: 0, sample: 1, truncated: false, maxSpans: 0, keys: [] }),
    createSavedQuery: vi.fn().mockResolvedValue({ id: 1 }),
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

const LIB = {
  match: 'and',
  filters: [
    { key: 'kind', op: '=', value: 'server' },
    { key: 'url.path', op: 'starts-with', value: '/api/v1/library' },
  ],
}

describe('TracesPage attribute filters', () => {
  it('reads the filter from the URL, shows the rows and sends it to the API', async () => {
    const filter = encodeURIComponent(JSON.stringify(LIB))
    render(
      <MemoryRouter initialEntries={[`/traces?filter=${filter}`]}>
        <TracesPage />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByText('GET /ok')).toBeInTheDocument())
    const keys = screen.getAllByLabelText('Filter key') as HTMLInputElement[]
    expect(keys.map((k) => k.value)).toEqual(['kind', 'url.path'])
    const sent = new URL(tracesUrls[0], 'http://x').searchParams.get('filter')
    expect(JSON.parse(sent ?? 'null')).toEqual(LIB)
  })

  it('sends a filter built in the UI and leaves unfinished rows out', async () => {
    render(
      <MemoryRouter initialEntries={['/traces?operation=GET%20/ok']}>
        <TracesPage />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByText('GET /ok', { selector: 'td, span, div' })).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: '+ Add filter' }))
    fireEvent.click(screen.getByRole('button', { name: '+ Add filter' }))
    const keys = screen.getAllByLabelText('Filter key')
    fireEvent.change(keys[0], { target: { value: 'http.response.status_code' } })
    fireEvent.change(screen.getAllByLabelText('Filter operator')[0], { target: { value: '>=' } })
    fireEvent.change(screen.getAllByLabelText('Filter value')[0], { target: { value: '500' } })
    // The second row has no key and is dropped.
    const before = tracesUrls.length
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    await waitFor(() => expect(tracesUrls.length).toBeGreaterThan(before))
    const last = tracesUrls[tracesUrls.length - 1]
    const sent = JSON.parse(new URL(last, 'http://x').searchParams.get('filter') ?? 'null')
    expect(sent).toEqual({ match: 'and', filters: [{ key: 'http.response.status_code', op: '>=', value: '500' }] })
  })

  it('saves the filter and loads a saved query back into the builder', async () => {
    vi.mocked(api.getSavedQueries).mockResolvedValue([
      { id: 7, projectId: 1, name: 'library', service: '', operation: '', status: '', minDurationUs: 0, filters: LIB, createdAt: '' },
    ])
    const filter = encodeURIComponent(JSON.stringify(LIB))
    render(
      <MemoryRouter initialEntries={[`/traces?filter=${filter}`]}>
        <TracesPage />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByText('library')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Save Query' }))
    await waitFor(() => expect(api.createSavedQuery).toHaveBeenCalled())
    expect(vi.mocked(api.createSavedQuery).mock.calls[0][0].filters).toEqual(LIB)

    fireEvent.click(screen.getAllByLabelText('Remove filter')[0])
    fireEvent.click(screen.getAllByLabelText('Remove filter')[0])
    expect(screen.queryAllByLabelText('Filter key')).toHaveLength(0)
    fireEvent.click(screen.getByText('library'))
    expect((screen.getAllByLabelText('Filter key') as HTMLInputElement[]).map((k) => k.value)).toEqual(['kind', 'url.path'])
  })
})

function Where() {
  const loc = useLocation()
  return <div data-testid="where">{loc.pathname + loc.search}</div>
}

describe('TracesPage compare', () => {
  it('opens the comparison with the current filters as the selection', async () => {
    const filter = encodeURIComponent(JSON.stringify(LIB))
    render(
      <MemoryRouter initialEntries={[`/traces?service=web&filter=${filter}`]}>
        <Routes>
          <Route path="/traces" element={<TracesPage />} />
          <Route path="/compare" element={<Where />} />
        </Routes>
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByRole('button', { name: 'Compare attributes' })).toBeEnabled())
    fireEvent.click(screen.getByRole('button', { name: 'Compare attributes' }))

    const where = (await screen.findByTestId('where')).textContent ?? ''
    expect(where.startsWith('/compare?')).toBe(true)
    const selection = JSON.parse(new URLSearchParams(where.split('?')[1]).get('selection') ?? 'null')
    expect(selection.filters).toEqual([{ key: 'service', op: '=', value: 'web' }, ...LIB.filters])
  })

  it('disables the comparison until a filter is set', async () => {
    render(
      <MemoryRouter initialEntries={['/traces']}>
        <TracesPage />
      </MemoryRouter>,
    )
    expect(await screen.findByRole('button', { name: 'Compare attributes' })).toBeDisabled()
  })
})

describe('TracesPage project scope', () => {
  it('scopes the list to the project of a link from the query page', async () => {
    render(
      <MemoryRouter initialEntries={['/traces?project=7&filter=' + encodeURIComponent(JSON.stringify(LIB))]}>
        <TracesPage />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByText('GET /ok')).toBeInTheDocument())
    expect(tracesUrls[0]).toContain('project_id=7')
  })
})
