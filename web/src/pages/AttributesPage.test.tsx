import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { AttributesPage } from './AttributesPage'

vi.mock('../api/client', () => ({
  api: {
    listProjects: vi.fn(),
    getSpanNames: vi.fn(),
    getAttributes: vi.fn(),
  },
}))

import { api } from '../api/client'

const attributes = vi.mocked(api.getAttributes)

function renderPage(url = '/attributes') {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <AttributesPage />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listProjects).mockResolvedValue([
    { id: 7, slug: 'profotograaf', name: 'Profotograaf', status: 'active' },
  ])
  vi.mocked(api.getSpanNames).mockResolvedValue([
    { name: 'POST /api/v1/library/presign', count: 1200, rootCount: 1200 },
  ])
  attributes.mockResolvedValue({
    scanned: 1200,
    sample: 1,
    truncated: false,
    maxSpans: 20000,
    keys: [
      {
        key: 'client.address',
        spans: 1200,
        coverage: 1,
        distinct: 1000,
        distinctCapped: true,
        top: [{ value: '10.0.0.1', count: 40 }],
      },
      {
        key: 'http.response.status_code',
        spans: 600,
        coverage: 0.5,
        distinct: 2,
        distinctCapped: false,
        top: [{ value: '200', count: 590 }, { value: '500', count: 10 }],
      },
    ],
  })
})

describe('AttributesPage', () => {
  it('queries the first project with a required, bounded range', async () => {
    renderPage()
    expect(await screen.findByText('client.address', { exact: false })).toBeInTheDocument()
    const scope = attributes.mock.calls[0][0]
    expect(scope.projectId).toBe(7)
    const hours = (Date.parse(scope.to) - Date.parse(scope.from)) / 3600_000
    expect(hours).toBeCloseTo(24, 1)
    expect(scope.spanName).toBeUndefined()
    expect(scope.sample).toBeUndefined()
  })

  it('shows coverage, capped cardinality and top values', async () => {
    renderPage()
    expect(await screen.findByText('50%')).toBeInTheDocument()
    expect(screen.getByText('1,000+')).toBeInTheDocument()
    expect(screen.getByText('10.0.0.1')).toBeInTheDocument()
    expect(screen.getByText('500')).toBeInTheDocument()
    expect(screen.getByTestId('scan-note')).toHaveTextContent('Scanned 1,200 spans (exact).')
  })

  it('reports sampling and truncation', async () => {
    attributes.mockResolvedValue({ scanned: 20000, sample: 20, truncated: true, maxSpans: 20000, keys: [] })
    renderPage('/attributes?range=7d')
    expect(await screen.findByText('No attributes in this window.')).toBeInTheDocument()
    const note = screen.getByTestId('scan-note')
    expect(note).toHaveTextContent('1 in 20 sample')
    expect(note).toHaveTextContent('stopped at 20,000 spans')
  })

  it('filters by span name and sampling', async () => {
    renderPage()
    await screen.findByText('client.address', { exact: false })
    await screen.findByRole('option', { name: /POST \/api\/v1\/library\/presign/ })

    fireEvent.change(screen.getByLabelText('Span name'), { target: { value: 'POST /api/v1/library/presign' } })
    await waitFor(() => expect(attributes.mock.calls.at(-1)?.[0].spanName).toBe('POST /api/v1/library/presign'))

    fireEvent.change(screen.getByLabelText('Sampling'), { target: { value: '5' } })
    await waitFor(() => expect(attributes.mock.calls.at(-1)?.[0].sample).toBe(5))
  })

  it('loads up to 50 values of a key when expanded', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Values of http.response.status_code' }))
    await waitFor(() => {
      const last = attributes.mock.calls.at(-1)?.[0]
      expect(last?.key).toBe('http.response.status_code')
      expect(last?.top).toBe(50)
    })
  })

  it('shows the server error', async () => {
    attributes.mockRejectedValue(new Error('range is limited to 168h0m0s'))
    renderPage()
    expect(await screen.findByRole('alert')).toHaveTextContent('range is limited')
  })
})
