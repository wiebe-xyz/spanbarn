import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { SaveToBoard } from './SaveToBoard'
import { defaultState } from '../../analyze/model'
import { parseFilter } from '../../filters/model'

vi.mock('../../api/client', () => ({
  api: { listBoards: vi.fn(), createBoard: vi.fn(), addPanel: vi.fn() },
}))

import { api } from '../../api/client'

const board = (id: number, name: string) => ({
  id, projectId: 7, name, timeRange: '24h', refreshSeconds: 0, panels: [], createdAt: '', updatedAt: '',
})

const state = () => ({
  ...defaultState(),
  projectId: 7,
  range: '7d' as const,
  groupBy: ['url.path'],
  calcs: ['count', 'p95'],
  view: 'chart' as const,
  chartCalc: 'p95',
  filter: parseFilter({ match: 'and', filters: [{ key: 'kind', op: '=', value: 'server' }] }),
})

const renderIt = () =>
  render(
    <MemoryRouter>
      <SaveToBoard projectId={7} state={state()} />
    </MemoryRouter>,
  )

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listBoards).mockResolvedValue([board(3, 'Overview')])
  vi.mocked(api.addPanel).mockResolvedValue({ id: 1 })
  vi.mocked(api.createBoard).mockResolvedValue({ id: 4 })
})

describe('SaveToBoard', () => {
  it('adds the query as a panel on an existing board', async () => {
    renderIt()
    fireEvent.click(screen.getByRole('button', { name: 'Save to board' }))
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'Board' })).toHaveValue('3'))
    expect(screen.getByLabelText('Panel title')).toHaveValue('Count by url.path')

    fireEvent.change(screen.getByLabelText('Panel title'), { target: { value: 'Paths' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add panel' }))

    await waitFor(() => expect(api.addPanel).toHaveBeenCalledTimes(1))
    expect(api.createBoard).not.toHaveBeenCalled()
    expect(api.addPanel).toHaveBeenCalledWith(3, {
      title: 'Paths',
      view: 'chart',
      filters: { match: 'and', filters: [{ key: 'kind', op: '=', value: 'server' }] },
      definition: { groupBy: ['url.path'], calcs: ['count', 'p95'], limit: 20, chartCalc: 'p95' },
    })
    expect(await screen.findByRole('status')).toHaveTextContent('Saved to Overview')
    expect(screen.getByRole('link', { name: 'Overview' })).toHaveAttribute('href', '/boards/3')
  })

  it('creates a board first when none exists, with the range of the query', async () => {
    vi.mocked(api.listBoards).mockResolvedValue([])
    renderIt()
    fireEvent.click(screen.getByRole('button', { name: 'Save to board' }))
    const add = await screen.findByRole('button', { name: 'Add panel' })
    expect(add).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Board name'), { target: { value: 'Errors' } })
    fireEvent.click(add)
    await waitFor(() => expect(api.createBoard).toHaveBeenCalledWith(7, 'Errors', '7d'))
    await waitFor(() => expect(api.addPanel).toHaveBeenCalledWith(4, expect.objectContaining({ view: 'chart' })))
  })

  it('shows the error and keeps the form when saving fails', async () => {
    vi.mocked(api.addPanel).mockRejectedValue(new Error('a board holds at most 50 panels'))
    renderIt()
    fireEvent.click(screen.getByRole('button', { name: 'Save to board' }))
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'Board' })).toHaveValue('3'))
    fireEvent.click(screen.getByRole('button', { name: 'Add panel' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('at most 50 panels')
    expect(screen.getByRole('form', { name: 'Save to board' })).toBeInTheDocument()
  })
})
