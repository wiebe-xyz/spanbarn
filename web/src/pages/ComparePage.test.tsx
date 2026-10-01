import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { ComparePage } from './ComparePage'

vi.mock('../api/client', () => ({
  api: {
    listProjects: vi.fn(),
    getAttributes: vi.fn(),
    compareAttributes: vi.fn(),
  },
}))

import { api } from '../api/client'

const compare = vi.mocked(api.compareAttributes)

const SELECTION = JSON.stringify({ match: 'and', filters: [{ key: 'name', op: '=', value: 'POST /orphan' }] })

function renderPage(url = '/compare') {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <ComparePage />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listProjects).mockResolvedValue([{ id: 7, slug: 'profotograaf', name: 'Profotograaf', status: 'active' }])
  vi.mocked(api.getAttributes).mockResolvedValue({ scanned: 0, sample: 1, truncated: false, maxSpans: 0, keys: [] })
  compare.mockResolvedValue({
    selection: { scanned: 40, truncated: false },
    baseline: { scanned: 400, truncated: true },
    sample: 20,
    maxSpans: 400,
    attributes: [
      {
        key: 'user_agent.original',
        score: 0.82,
        selectionCoverage: 1,
        baselineCoverage: 0.9,
        values: [
          { value: 'Mozilla/5.0', selectionCount: 38, selectionShare: 0.95, baselineCount: 20, baselineShare: 0.05 },
          { value: '', missing: true, selectionCount: 0, selectionShare: 0, baselineCount: 40, baselineShare: 0.1 },
        ],
      },
    ],
  })
})

describe('ComparePage', () => {
  it('asks for a selection before it compares', async () => {
    renderPage()
    expect(await screen.findByText('Add at least one selection filter.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Compare' })).toBeDisabled()
    expect(compare).not.toHaveBeenCalled()
  })

  it('builds a selection, compares and renders the ranked attributes', async () => {
    renderPage('/compare?project=7&range=1h')
    const selection = screen.getByRole('group', { name: 'Selection' })
    fireEvent.click(within(selection).getByText('+ Add filter'))
    fireEvent.change(within(selection).getByLabelText('Filter key'), { target: { value: 'name' } })
    fireEvent.change(within(selection).getByLabelText('Filter value'), { target: { value: 'POST /orphan' } })

    fireEvent.click(screen.getByRole('button', { name: 'Compare' }))

    await waitFor(() => expect(compare).toHaveBeenCalledTimes(1))
    const scope = compare.mock.calls[0][0]
    expect(scope.projectId).toBe(7)
    expect(JSON.parse(scope.selection)).toEqual(JSON.parse(SELECTION))
    expect(scope.baseline).toBeUndefined()
    expect(new Date(scope.to).getTime() - new Date(scope.from).getTime()).toBe(3600_000)

    const card = await screen.findByRole('region', { name: 'Attribute user_agent.original' })
    expect(within(card).getByText('0.82')).toBeInTheDocument()
    expect(within(card).getByText('Mozilla/5.0')).toBeInTheDocument()
    expect(within(card).getByText('(not set)')).toBeInTheDocument()
    expect(within(card).getByText('95%')).toBeInTheDocument()
    expect(screen.getByTestId('compare-note')).toHaveTextContent('Selection: 40 spans')
    expect(screen.getByTestId('compare-note')).toHaveTextContent('Baseline: 400 spans (stopped at 400')
    expect(screen.getByTestId('compare-note')).toHaveTextContent('1 in 20 sample')
  })

  it('runs straight from a shared URL and sends the baseline filter', async () => {
    const baseline = JSON.stringify({ match: 'and', filters: [{ key: 'kind', op: '=', value: 'server' }] })
    renderPage(`/compare?project=7&selection=${encodeURIComponent(SELECTION)}&baseline=${encodeURIComponent(baseline)}&sample=5`)

    await waitFor(() => expect(compare).toHaveBeenCalled())
    const scope = compare.mock.calls[0][0]
    expect(scope.selection).toBe(SELECTION)
    expect(scope.baseline).toBe(baseline)
    expect(scope.sample).toBe(5)
    expect(screen.getByRole('button', { name: 'Compare' })).toBeEnabled()
  })

  it('says so when nothing differs or nothing matches', async () => {
    compare.mockResolvedValueOnce({
      selection: { scanned: 5, truncated: false },
      baseline: { scanned: 5, truncated: false },
      sample: 1,
      maxSpans: 10000,
      attributes: [],
    })
    renderPage(`/compare?project=7&selection=${encodeURIComponent(SELECTION)}`)
    expect(await screen.findByText('No attribute differs between the selection and the baseline.')).toBeInTheDocument()
  })

  it('shows the server error', async () => {
    compare.mockRejectedValueOnce(new Error('invalid selection filter'))
    renderPage(`/compare?project=7&selection=${encodeURIComponent(SELECTION)}`)
    expect(await screen.findByRole('alert')).toHaveTextContent('invalid selection filter')
  })

  it('reports an empty selection', async () => {
    compare.mockResolvedValueOnce({
      selection: { scanned: 0, truncated: false },
      baseline: { scanned: 9, truncated: false },
      sample: 1,
      maxSpans: 10000,
      attributes: [],
    })
    renderPage(`/compare?project=7&selection=${encodeURIComponent(SELECTION)}`)
    expect(await screen.findByText('No spans match the selection in this range.')).toBeInTheDocument()
  })
})
