import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { ClaudeCodePage } from './ClaudeCodePage'
import { api } from '../api/client'

vi.mock('../api/client', () => ({
  api: { listProjects: vi.fn(), getMetricTotals: vi.fn() },
}))

describe('ClaudeCodePage', () => {
  beforeEach(() => {
    vi.mocked(api.listProjects).mockResolvedValue([{ id: 7, slug: 'claude-code' }] as never)
    vi.mocked(api.getMetricTotals).mockImplementation((async (name: string) => ({
      groups: [{ labels: { 'session.id': 'abc' }, total: name.endsWith('cost.usage') ? 1.5 : 10 }],
    })) as never)
  })

  it('lists sessions with their cost and links to the drill-down', async () => {
    render(<MemoryRouter><ClaudeCodePage /></MemoryRouter>)
    const link = await screen.findByRole('link', { name: 'abc' })
    expect(link.getAttribute('href')).toContain('/claude-code/abc')
    await waitFor(() => expect(screen.getAllByText('$1.50').length).toBeGreaterThan(0))
    expect(api.getMetricTotals).toHaveBeenCalledWith(expect.any(String), expect.any(String), expect.any(String), ['session.id'], 7)
  })
})
