import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { BoardsPage } from './BoardsPage'

vi.mock('../api/client', () => ({
  api: { listProjects: vi.fn(), listBoards: vi.fn(), createBoard: vi.fn() },
}))

import { api } from '../api/client'

const board = (id: number, name: string, timeRange = '24h') => ({
  id, projectId: 7, name, timeRange, refreshSeconds: 0, panels: [], createdAt: '', updatedAt: '',
})

function renderPage(url = '/boards') {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="/boards" element={<BoardsPage />} />
        <Route path="/boards/:id" element={<div>board page</div>} />
      </Routes>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listProjects).mockResolvedValue([
    { id: 7, slug: 'profotograaf', name: 'Profotograaf', status: 'active' },
    { id: 8, slug: 'other', name: 'Other', status: 'active' },
  ])
  vi.mocked(api.listBoards).mockResolvedValue([board(1, 'Overview'), board(2, 'Latency', '7d')])
})

describe('BoardsPage', () => {
  it('lists the boards of the first project with their range', async () => {
    renderPage()
    expect(await screen.findByRole('link', { name: /Overview/ })).toHaveAttribute('href', '/boards/1')
    expect(screen.getByRole('link', { name: /Latency/ })).toHaveTextContent('Last 7 days')
    expect(api.listBoards).toHaveBeenCalledWith(7)
  })

  it('reads the project from the URL', async () => {
    renderPage('/boards?project=8')
    await waitFor(() => expect(api.listBoards).toHaveBeenCalledWith(8))
  })

  it('says so when a project has no boards', async () => {
    vi.mocked(api.listBoards).mockResolvedValue([])
    renderPage()
    expect(await screen.findByText('No boards in this project yet.')).toBeInTheDocument()
  })

  it('creates a board and opens it', async () => {
    vi.mocked(api.createBoard).mockResolvedValue({ id: 9 })
    renderPage()
    await screen.findByRole('link', { name: /Overview/ })
    const create = screen.getByRole('button', { name: 'Create board' })
    expect(create).toBeDisabled()
    fireEvent.change(screen.getByLabelText('New board name'), { target: { value: ' Errors ' } })
    fireEvent.click(create)
    await waitFor(() => expect(api.createBoard).toHaveBeenCalledWith(7, 'Errors'))
    expect(await screen.findByText('board page')).toBeInTheDocument()
  })

  it('shows a create error', async () => {
    vi.mocked(api.createBoard).mockRejectedValue(new Error('name is required'))
    renderPage()
    await screen.findByRole('link', { name: /Overview/ })
    fireEvent.change(screen.getByLabelText('New board name'), { target: { value: 'x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create board' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('name is required')
  })
})
