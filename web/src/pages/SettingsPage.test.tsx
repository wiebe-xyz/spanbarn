import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { SettingsPage } from './SettingsPage'

vi.mock('../components/ProjectAPIKeys', () => ({ ProjectAPIKeys: () => null }))

beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.startsWith('/api/v1/stats')) return Promise.resolve(new Response('{}', { status: 503 }))
      if (url === '/api/v1/projects') {
        return Promise.resolve(new Response(JSON.stringify([
          { id: 7, slug: 'my app/x', name: 'Odd slug', status: 'active', createdAt: '2026-10-01T10:00:00Z' },
        ])))
      }
      if (url.startsWith('/api/v1/settings') && init?.method === 'PUT') {
        return Promise.resolve(new Response(JSON.stringify({ error: 'metrics_retention_days must be positive' }), { status: 400 }))
      }
      if (url.startsWith('/api/v1/settings')) return Promise.resolve(new Response('{}'))
      return Promise.resolve(new Response('[]'))
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// A rejected PUT used to reset the button to Save with no message, so an
// operator could not tell a refused value from a saved one.
describe('SettingsPage retention save', () => {
  it('shows why a rejected save failed', async () => {
    render(
      <MemoryRouter>
        <SettingsPage />
      </MemoryRouter>,
    )
    const saveButtons = await screen.findAllByRole('button', { name: 'Save' })
    fireEvent.click(saveButtons[0])
    const alert = await screen.findByRole('alert')
    await waitFor(() => expect(alert).toHaveTextContent('metrics_retention_days must be positive'))
  })
})

describe('SettingsPage setup links', () => {
  it('encodes the slug in the setup link', async () => {
    const { container } = render(
      <MemoryRouter>
        <SettingsPage />
      </MemoryRouter>,
    )
    await waitFor(() => {
      expect(container.querySelector('a[href^="/api/v1/setup/"]')).not.toBeNull()
    })
    expect(container.querySelector('a[href^="/api/v1/setup/"]')).toHaveAttribute('href', '/api/v1/setup/my%20app%2Fx')
  })
})
