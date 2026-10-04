import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { CalculatedFieldsPage } from './CalculatedFieldsPage'

vi.mock('../api/client', () => ({
  api: { listProjects: vi.fn() },
}))
vi.mock('../api/calculatedFields', () => ({
  calculatedFieldsApi: { list: vi.fn(), create: vi.fn(), update: vi.fn(), remove: vi.fn(), preview: vi.fn() },
}))

import { api } from '../api/client'
import { calculatedFieldsApi as fields } from '../api/calculatedFields'

const field = (id: number, name: string, expression: string) => ({
  id, projectId: 7, name, expression, createdAt: '', updatedAt: '',
})

function renderPage(url = '/calculated-fields') {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <CalculatedFieldsPage />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listProjects).mockResolvedValue([
    { id: 7, slug: 'p', name: 'Profotograaf', status: 'active' },
    { id: 8, slug: 'o', name: 'Other', status: 'active' },
  ])
  vi.mocked(fields.list).mockResolvedValue([field(1, 'duration_ms', 'duration_us / 1000')])
  vi.mocked(fields.preview).mockResolvedValue({ samples: [{ spanId: 's1', name: 'GET /a', value: 2.5 }] })
})

describe('CalculatedFieldsPage', () => {
  it('lists the fields of the first project', async () => {
    renderPage()
    expect(await screen.findByText('duration_ms')).toBeInTheDocument()
    expect(screen.getByText('duration_us / 1000')).toBeInTheDocument()
    expect(fields.list).toHaveBeenCalledWith(7)
  })

  it('reads the project from the URL', async () => {
    renderPage('/calculated-fields?project=8')
    await waitFor(() => expect(fields.list).toHaveBeenCalledWith(8))
  })

  it('says so when a project has no fields', async () => {
    vi.mocked(fields.list).mockResolvedValue([])
    renderPage()
    expect(await screen.findByText('No calculated fields in this project yet.')).toBeInTheDocument()
  })

  it('previews the expression on a sample span while typing', async () => {
    renderPage()
    await screen.findByText('duration_ms')
    fireEvent.click(screen.getByRole('button', { name: 'New field' }))
    fireEvent.change(screen.getByLabelText('Expression'), { target: { value: 'duration_us / 1000' } })
    expect(await screen.findByText('2.5')).toBeInTheDocument()
    expect(screen.getByText('GET /a')).toBeInTheDocument()
    expect(fields.preview).toHaveBeenLastCalledWith(7, '', 'duration_us / 1000')
  })

  it('shows why an expression does not preview', async () => {
    vi.mocked(fields.preview).mockRejectedValue(new Error('unknown function "sum"'))
    renderPage()
    await screen.findByText('duration_ms')
    fireEvent.click(screen.getByRole('button', { name: 'New field' }))
    fireEvent.change(screen.getByLabelText('Expression'), { target: { value: 'sum(1)' } })
    expect(await screen.findByText('unknown function "sum"')).toBeInTheDocument()
  })

  it('creates a field and reloads the list', async () => {
    vi.mocked(fields.create).mockResolvedValue({ id: 2 })
    renderPage()
    await screen.findByText('duration_ms')
    fireEvent.click(screen.getByRole('button', { name: 'New field' }))
    const save = screen.getByRole('button', { name: 'Save field' })
    expect(save).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Field name'), { target: { value: 'route' } })
    fireEvent.change(screen.getByLabelText('Expression'), { target: { value: 'coalesce(http.route, name)' } })
    fireEvent.click(save)
    await waitFor(() => expect(fields.create).toHaveBeenCalledWith(7, 'route', 'coalesce(http.route, name)'))
    await waitFor(() => expect(fields.list).toHaveBeenCalledTimes(2))
    expect(screen.queryByLabelText('Field name')).not.toBeInTheDocument()
  })

  it('keeps the editor open and shows the server message when a save is rejected', async () => {
    vi.mocked(fields.create).mockRejectedValue(new Error('name "duration_us" is a span column'))
    renderPage()
    await screen.findByText('duration_ms')
    fireEvent.click(screen.getByRole('button', { name: 'New field' }))
    fireEvent.change(screen.getByLabelText('Field name'), { target: { value: 'duration_us' } })
    fireEvent.change(screen.getByLabelText('Expression'), { target: { value: '1' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save field' }))
    expect(await screen.findByText('name "duration_us" is a span column')).toBeInTheDocument()
    expect(screen.getByLabelText('Field name')).toBeInTheDocument()
  })

  it('edits an existing field', async () => {
    vi.mocked(fields.update).mockResolvedValue({ status: 'ok' })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Edit duration_ms' }))
    expect(screen.getByLabelText('Field name')).toHaveValue('duration_ms')
    fireEvent.change(screen.getByLabelText('Expression'), { target: { value: 'duration_us / 500' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save field' }))
    await waitFor(() => expect(fields.update).toHaveBeenCalledWith(1, 'duration_ms', 'duration_us / 500'))
  })

  it('deletes a field', async () => {
    vi.mocked(fields.remove).mockResolvedValue({ status: 'ok' })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Delete duration_ms' }))
    await waitFor(() => expect(fields.remove).toHaveBeenCalledWith(1))
    await waitFor(() => expect(fields.list).toHaveBeenCalledTimes(2))
  })
})
