import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { ProjectAPIKeys } from './ProjectAPIKeys'

const mockFetch = vi.fn()
vi.stubGlobal('fetch', mockFetch)

function reply(status: number, body?: unknown) {
  const text = body === undefined ? '' : JSON.stringify(body)
  return Promise.resolve({ ok: status < 400, status, text: () => Promise.resolve(text), json: () => Promise.resolve(body) })
}

const existing = { id: 7, projectId: 3, name: 'setup', scope: 'ingest', lastUsedAt: null, createdAt: '2026-10-01T00:00:00Z' }

describe('ProjectAPIKeys', () => {
  beforeEach(() => mockFetch.mockReset())

  it('creates a key and shows it once', async () => {
    const created = { ...existing, id: 8, name: 'ingest-2026-10', key: 'a'.repeat(64) }
    mockFetch
      .mockImplementationOnce(() => reply(200, [existing]))
      .mockImplementationOnce(() => reply(201, created))
      .mockImplementationOnce(() => reply(200, [existing, created]))
    render(<ProjectAPIKeys projectId={3} />)
    fireEvent.click(screen.getByText('API Keys'))
    await screen.findByText('setup')

    fireEvent.change(screen.getByLabelText('New key name'), { target: { value: ' ingest-2026-10 ' } })
    fireEvent.click(screen.getByText('Create key'))

    const shown = await screen.findByLabelText('New API key')
    expect((shown as HTMLInputElement).value).toBe('a'.repeat(64))
    const [url, init] = mockFetch.mock.calls[1]
    expect(url).toBe('/api/v1/projects/3/apikeys')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body)).toEqual({ name: 'ingest-2026-10', scope: 'ingest' })

    fireEvent.click(screen.getByText('Done'))
    expect(screen.queryByLabelText('New API key')).toBeNull()
  })

  it('revokes a key only after confirmation', async () => {
    mockFetch
      .mockImplementationOnce(() => reply(200, [existing]))
      .mockImplementationOnce(() => reply(204))
      .mockImplementationOnce(() => reply(200, []))
    render(<ProjectAPIKeys projectId={3} />)
    fireEvent.click(screen.getByText('API Keys'))
    fireEvent.click(await screen.findByText('Revoke'))
    expect(mockFetch).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByText('Confirm revoke'))
    await screen.findByText('No API keys')
    const [url, init] = mockFetch.mock.calls[1]
    expect(url).toBe('/api/v1/projects/3/apikeys/7')
    expect(init.method).toBe('DELETE')
  })

  it('shows a refused request', async () => {
    mockFetch
      .mockImplementationOnce(() => reply(200, []))
      .mockImplementationOnce(() => reply(400, { error: 'invalid api key request' }))
      .mockImplementationOnce(() => reply(200, []))
    render(<ProjectAPIKeys projectId={3} />)
    fireEvent.click(screen.getByText('API Keys'))
    await screen.findByText('No API keys')
    fireEvent.change(screen.getByLabelText('New key name'), { target: { value: 'x' } })
    fireEvent.click(screen.getByText('Create key'))
    await waitFor(() => expect(screen.getByText(/invalid api key request/)).toBeDefined())
  })
})
