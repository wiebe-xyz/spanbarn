import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { MobileTabBar } from './MobileTabBar'

vi.mock('../api/iambarnWidget', () => ({
  useIambarnSession: () => null,
  useIambarnProfile: () => null,
  iambarnLogout: vi.fn(),
}))
vi.mock('../api/clientConfig', () => ({ isOIDCSession: () => false }))

// The desktop sidebar lists Query, Boards and Claude Code; the phone menu had
// no link to them.
describe('MobileTabBar More menu', () => {
  it('links to Query, Boards and Claude Code', () => {
    render(
      <MemoryRouter>
        <MobileTabBar />
      </MemoryRouter>,
    )
    fireEvent.click(screen.getByRole('button', { name: /more/i }))
    expect(screen.getByRole('link', { name: 'Query' })).toHaveAttribute('href', '/analyze')
    expect(screen.getByRole('link', { name: 'Boards' })).toHaveAttribute('href', '/boards')
    expect(screen.getByRole('link', { name: 'Claude Code' })).toHaveAttribute('href', '/claude-code')
  })
})
