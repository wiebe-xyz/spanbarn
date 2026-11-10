import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { navManifest, mobileTabs, mobileMoreItems } from './navManifest'
import { MobileTabBar } from './components/MobileTabBar'
import { DashboardLayout } from './components/DashboardLayout'

vi.mock('./api/iambarnWidget', () => ({
  useIambarnSession: () => null,
  useIambarnProfile: () => null,
  iambarnLogout: vi.fn(),
}))
vi.mock('./api/clientConfig', () => ({ isOIDCSession: () => false }))

describe('nav manifest', () => {
  it('gives every entry one route and a mobile placement', () => {
    const routes = navManifest.map((e) => e.to)
    expect(new Set(routes).size).toBe(routes.length)
    for (const e of navManifest) {
      expect(['tab', 'more', 'hidden']).toContain(e.mobile)
    }
  })

  it('keeps the bottom bar at four tabs', () => {
    expect(mobileTabs).toHaveLength(4)
  })

  it('shows every entry in the desktop sidebar', () => {
    render(
      <MemoryRouter>
        <DashboardLayout />
      </MemoryRouter>,
    )
    // The sidebar <nav> comes before the mobile tab bar in the layout.
    const nav = screen.getAllByRole('navigation')[0]
    for (const e of navManifest) {
      expect(within(nav).getByRole('link', { name: e.label })).toHaveAttribute('href', e.to)
    }
  })

  it('reaches every entry that is not hidden from a phone', () => {
    render(
      <MemoryRouter>
        <MobileTabBar />
      </MemoryRouter>,
    )
    fireEvent.click(screen.getByRole('button', { name: /more/i }))
    for (const e of mobileMoreItems) {
      expect(screen.getByRole('link', { name: e.label })).toHaveAttribute('href', e.to)
    }
    for (const e of mobileTabs) {
      expect(screen.getByRole('link', { name: e.tabLabel ?? e.label })).toHaveAttribute('href', e.to)
    }
    const placed = mobileTabs.length + mobileMoreItems.length
    expect(placed).toBe(navManifest.filter((e) => e.mobile !== 'hidden').length)
  })
})
