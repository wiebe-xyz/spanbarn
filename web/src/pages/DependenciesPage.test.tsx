import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { DependenciesPage } from './DependenciesPage'
import type { DependencySummary } from '../api/types'

const mockDependencies: DependencySummary[] = [
  {
    target: 'PostgreSQL',
    targetType: 'database',
    callCount: 5000,
    errorCount: 10,
    errorRate: 0.002,
    p50Us: 1200,
    p95Us: 5400,
    p99Us: 12000,
  },
  {
    target: 'stripe.com',
    targetType: 'http',
    callCount: 800,
    errorCount: 40,
    errorRate: 0.05,
    p50Us: 95000,
    p95Us: 250000,
    p99Us: 500000,
  },
  {
    target: 'S3',
    targetType: 'aws',
    callCount: 3000,
    errorCount: 3,
    errorRate: 0.001,
    p50Us: 15000,
    p95Us: 45000,
    p99Us: 90000,
  },
]

// Mock the API module
vi.mock('../api/client', () => ({
  api: {
    getDependencies: vi.fn(),
    searchTraces: vi.fn(),
  },
}))

// Mock recharts to avoid rendering issues in jsdom
vi.mock('recharts', () => ({
  LineChart: ({ children }: { children: React.ReactNode }) => <div data-testid="line-chart">{children}</div>,
  Line: () => null,
  XAxis: () => null,
  YAxis: () => null,
  Tooltip: () => null,
  ResponsiveContainer: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  CartesianGrid: () => null,
}))

import { api } from '../api/client'

const mockedGetDependencies = vi.mocked(api.getDependencies)

function renderPage() {
  return render(
    <MemoryRouter>
      <DependenciesPage />
    </MemoryRouter>,
  )
}

describe('DependenciesPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders table with dependency data', async () => {
    mockedGetDependencies.mockResolvedValueOnce(mockDependencies)

    renderPage()

    await waitFor(() => {
      expect(screen.getByText('PostgreSQL')).toBeDefined()
    })

    expect(screen.getByText('stripe.com')).toBeDefined()
    expect(screen.getByText('S3')).toBeDefined()
    expect(screen.getByText('database')).toBeDefined()
    expect(screen.getByText('http')).toBeDefined()
    expect(screen.getByText('aws')).toBeDefined()
  })

  it('sorts by column when header is clicked', async () => {
    mockedGetDependencies.mockResolvedValueOnce(mockDependencies)

    renderPage()

    // Wait for data to load (default sort is callCount desc)
    await waitFor(() => {
      expect(screen.getByText('PostgreSQL')).toBeDefined()
    })

    // Get all target cells - default sort by callCount desc should be: PostgreSQL (5000), S3 (3000), stripe.com (800)
    const rows = screen.getAllByRole('row')
    // rows[0] is thead, rows[1..3] are data rows
    const firstRowCells = rows[1].querySelectorAll('td')
    expect(firstRowCells[0].textContent).toBe('PostgreSQL')

    const lastRowCells = rows[3].querySelectorAll('td')
    expect(lastRowCells[0].textContent).toBe('stripe.com')

    // Click "Target" header to sort alphabetically ascending
    fireEvent.click(screen.getByText(/^Target/))

    const rowsAfterSort = screen.getAllByRole('row')
    const firstAfter = rowsAfterSort[1].querySelectorAll('td')
    expect(firstAfter[0].textContent).toBe('PostgreSQL')

    const lastAfter = rowsAfterSort[3].querySelectorAll('td')
    expect(lastAfter[0].textContent).toBe('stripe.com')

    // Click "Errors" header to sort by errors descending
    fireEvent.click(screen.getByText(/^Errors/))

    const rowsAfterErrors = screen.getAllByRole('row')
    const firstErrors = rowsAfterErrors[1].querySelectorAll('td')
    expect(firstErrors[0].textContent).toBe('stripe.com')
  })

  it('shows empty state when no dependencies found', async () => {
    mockedGetDependencies.mockResolvedValueOnce([])

    renderPage()

    await waitFor(() => {
      expect(screen.getByText('No dependencies found')).toBeDefined()
    })
  })
})
