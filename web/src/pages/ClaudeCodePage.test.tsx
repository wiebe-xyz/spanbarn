import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { ClaudeCodePage } from './ClaudeCodePage'
import { api } from '../api/client'

vi.mock('../api/client', () => ({
  api: { listProjects: vi.fn(), getMetricTotals: vi.fn(), getOperations: vi.fn() },
}))

describe('ClaudeCodePage', () => {
  beforeEach(() => {
    vi.mocked(api.getOperations).mockResolvedValue([])
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

  it('shows tool call counts and blocked-on-user time derived from spans', async () => {
    vi.mocked(api.getOperations).mockResolvedValue([
      { operation: 'claude_code.tool', resource: 'Bash', kind: 'internal', spanCount: 12, errorCount: 0, errorRate: 0, p50Us: 0, p95Us: 0, p99Us: 0 },
      { operation: 'claude_code.tool.blocked_on_user', resource: 'Bash', kind: 'internal', spanCount: 3, errorCount: 0, errorRate: 0, p50Us: 4_000_000, p95Us: 9_000_000, p99Us: 9_000_000 },
    ])
    render(<MemoryRouter><ClaudeCodePage /></MemoryRouter>)
    const row = (await screen.findByText('Bash')).closest('tr') as HTMLElement
    expect(row.textContent).toContain('12')
    expect(row.textContent).toContain('4.0 s')
    expect(api.getOperations).toHaveBeenCalledWith('claude-code', expect.any(String), expect.any(String), '')
  })
})
