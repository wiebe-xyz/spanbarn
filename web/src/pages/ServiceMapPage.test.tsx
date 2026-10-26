import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { ServiceMapPage } from './ServiceMapPage'
import type { ServiceMap } from '../api/types'

vi.mock('../api/client', () => ({
  api: { getServiceMap: vi.fn() },
}))

import { api } from '../api/client'

const mockedGetServiceMap = vi.mocked(api.getServiceMap)

const map: ServiceMap = {
  nodes: [{ id: 'web', spanCount: 10, errorCount: 0, errorRate: 0 }],
  edges: [],
}

function renderPage() {
  return render(
    <MemoryRouter>
      <ServiceMapPage />
    </MemoryRouter>,
  )
}

describe('ServiceMapPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.stubGlobal(
      'ResizeObserver',
      class {
        observe() {}
        disconnect() {}
      },
    )
    // The force layout animates for up to 200 frames; the tests only need the first render.
    vi.stubGlobal('requestAnimationFrame', () => 0)
    vi.stubGlobal('cancelAnimationFrame', () => {})
  })

  it('states the window a truncated map covers', async () => {
    const coveredFrom = new Date(Date.now() - 8 * 60_000).toISOString()
    mockedGetServiceMap.mockResolvedValueOnce({ ...map, truncated: true, coveredFrom })

    renderPage()

    const note = await screen.findByRole('note')
    expect(note.textContent).toContain('covers the last 8 min of the selected range')
  })

  it('shows no note when the map read the whole range', async () => {
    mockedGetServiceMap.mockResolvedValueOnce({ ...map, truncated: false })

    renderPage()

    await waitFor(() => expect(mockedGetServiceMap).toHaveBeenCalled())
    await waitFor(() => expect(screen.queryByText('Loading service map...')).toBeNull())
    expect(screen.queryByRole('note')).toBeNull()
  })
})
