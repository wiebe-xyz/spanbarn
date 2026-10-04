import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { SlosPage } from './SlosPage'

vi.mock('../api/client', () => ({ api: { listProjects: vi.fn() } }))
vi.mock('../api/slos', async (orig) => ({
  ...(await orig<typeof import('../api/slos')>()),
  sloApi: {
    list: vi.fn(), status: vi.fn(), create: vi.fn(), update: vi.fn(), remove: vi.fn(),
    listAlerts: vi.fn(), createAlert: vi.fn(), updateAlert: vi.fn(), removeAlert: vi.fn(),
  },
}))
// The builder is covered by its own tests: here it sets a fixed condition per label.
vi.mock('../components/filter/FilterBuilder', () => ({
  FilterBuilder: ({ label, onChange }: { label: string; onChange: (e: unknown) => void }) => (
    <button type="button" onClick={() => onChange({ match: 'and', filters: [{ key: label, op: 'exists' }] })}>
      set {label}
    </button>
  ),
}))

import { api } from '../api/client'
import { sloApi } from '../api/slos'

const slo = { id: 3, projectId: 7, name: 'Checkout', goodFilter: {}, totalFilter: {}, target: 0.995, windowDays: 30, createdAt: '' }
const alert = { id: 5, sloId: 3, windowMinutes: 60, burnRate: 14.4, webhookUrl: '', email: '', cooldownMinutes: 30, enabled: true, firing: false }
const status = (over = {}) => ({
  id: 3, name: 'Checkout', target: 0.995, windowDays: 30, good: 997, total: 1000, budgetRemaining: 0.4,
  alerts: [{ id: 5, windowMinutes: 60, burnRate: 14.4, enabled: true, currentBurn: 2.5, good: 9, total: 10, firing: false }],
  ...over,
})

function renderPage(url = '/slos') {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <Routes><Route path="/slos" element={<SlosPage />} /></Routes>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.listProjects).mockResolvedValue([
    { id: 7, slug: 'p', name: 'Profotograaf', status: 'active' },
    { id: 8, slug: 'o', name: 'Other', status: 'active' },
  ])
  vi.mocked(sloApi.list).mockResolvedValue([slo])
  vi.mocked(sloApi.status).mockResolvedValue(status())
  vi.mocked(sloApi.listAlerts).mockResolvedValue([alert])
})

describe('SlosPage status', () => {
  it('shows budget remaining, good over total and the burn per alert window', async () => {
    renderPage()
    const card = await screen.findByRole('region', { name: 'Checkout' })
    await within(card).findByText('40.0% of the budget remaining')
    expect(within(card).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '40')
    expect(card).toHaveTextContent('997 good of 1,000 total')
    expect(card).toHaveTextContent('Target 99.50% over 30 days')
    expect(card).toHaveTextContent('1h window, fires at 14.4x, burning 2.50x')
    expect(card).not.toHaveTextContent('FIRING')
    expect(sloApi.list).toHaveBeenCalledWith(7)
  })

  it('names a negative budget as overspent and flags a firing alert', async () => {
    vi.mocked(sloApi.status).mockResolvedValue(status({
      budgetRemaining: -3, good: 980,
      alerts: [{ id: 5, windowMinutes: 60, burnRate: 14.4, enabled: true, currentBurn: 20, good: 1, total: 10, firing: true }],
    }))
    renderPage()
    expect(await screen.findByText('Overspent by 300.0%')).toBeInTheDocument()
    expect(screen.getByText('FIRING')).toBeInTheDocument()
  })

  it('reads the project from the URL', async () => {
    renderPage('/slos?project=8')
    await waitFor(() => expect(sloApi.list).toHaveBeenCalledWith(8))
  })

  it('says so when there are no SLOs', async () => {
    vi.mocked(sloApi.list).mockResolvedValue([])
    renderPage()
    expect(await screen.findByText('No SLOs in this project yet.')).toBeInTheDocument()
  })

  it('shows a loading state, then a load error', async () => {
    vi.mocked(sloApi.list).mockRejectedValue(new Error('boom'))
    renderPage()
    expect(screen.getByText('Loading SLOs...')).toBeInTheDocument()
    expect(await screen.findByRole('alert')).toHaveTextContent('boom')
  })
})

describe('SlosPage form', () => {
  const openForm = async () => {
    renderPage()
    await screen.findByRole('region', { name: 'Checkout' })
    fireEvent.click(screen.getByRole('button', { name: 'New SLO' }))
  }
  const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } })

  it('shows validation errors without calling the API', async () => {
    await openForm()
    fireEvent.click(screen.getByRole('button', { name: 'Create SLO' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Name is required')
    fill('Name', 'API')
    fill('Target (%)', '100')
    fireEvent.click(screen.getByRole('button', { name: 'Create SLO' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Target must be above 0 and below 100 percent')
    fill('Target (%)', '99')
    fill('Window (days)', '91')
    fireEvent.click(screen.getByRole('button', { name: 'Create SLO' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Window must be a whole number of days from 1 to 90')
    expect(sloApi.create).not.toHaveBeenCalled()
  })

  it('creates an SLO with the target as a fraction and both filters', async () => {
    vi.mocked(sloApi.create).mockResolvedValue({ id: 9 })
    await openForm()
    fill('Name', ' API ')
    fill('Target (%)', '99.9')
    fill('Window (days)', '7')
    fireEvent.click(screen.getByRole('button', { name: 'set Good events' }))
    fireEvent.click(screen.getByRole('button', { name: 'set Total events' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create SLO' }))
    await waitFor(() => expect(sloApi.create).toHaveBeenCalledWith(7, {
      name: 'API',
      target: 0.999,
      windowDays: 7,
      goodFilter: { match: 'and', filters: [{ key: 'Good events', op: 'exists' }] },
      totalFilter: { match: 'and', filters: [{ key: 'Total events', op: 'exists' }] },
    }))
    await waitFor(() => expect(screen.queryByRole('form', { name: 'New SLO' })).not.toBeInTheDocument())
  })

  it('shows a server error such as a duplicate name and keeps the form', async () => {
    vi.mocked(sloApi.create).mockRejectedValue(new Error('an SLO with this name already exists'))
    await openForm()
    fill('Name', 'Checkout')
    fireEvent.click(screen.getByRole('button', { name: 'Create SLO' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('already exists')
    expect(screen.getByRole('form', { name: 'New SLO' })).toBeInTheDocument()
  })

  it('edits an SLO with the current values filled in', async () => {
    vi.mocked(sloApi.update).mockResolvedValue({ status: 'ok' })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Edit Checkout' }))
    expect(screen.getByLabelText('Target (%)')).toHaveValue('99.5')
    fill('Name', 'Checkout v2')
    fireEvent.click(screen.getByRole('button', { name: 'Save SLO' }))
    await waitFor(() => expect(sloApi.update).toHaveBeenCalledWith(7, 3, expect.objectContaining({ name: 'Checkout v2', target: 0.995, windowDays: 30 })))
  })

  it('deletes an SLO after confirmation', async () => {
    vi.mocked(sloApi.remove).mockResolvedValue({ status: 'ok' })
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Delete Checkout' }))
    await waitFor(() => expect(sloApi.remove).toHaveBeenCalledWith(7, 3))
  })
})

describe('SlosPage burn alerts', () => {
  const open = async () => {
    renderPage()
    await screen.findByRole('region', { name: 'Checkout' })
  }

  it('rejects an alert window as long as the SLO window', async () => {
    await open()
    fireEvent.click(screen.getByRole('button', { name: 'Add burn alert to Checkout' }))
    fireEvent.change(screen.getByLabelText('Alert window (minutes)'), { target: { value: '43200' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add alert' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('shorter than the SLO window of 43200 minutes')
    expect(sloApi.createAlert).not.toHaveBeenCalled()
  })

  it('adds, edits and deletes a burn alert', async () => {
    vi.mocked(sloApi.createAlert).mockResolvedValue({ id: 6 })
    vi.mocked(sloApi.updateAlert).mockResolvedValue({ status: 'ok' })
    vi.mocked(sloApi.removeAlert).mockResolvedValue({ status: 'ok' })
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    await open()

    fireEvent.click(screen.getByRole('button', { name: 'Add burn alert to Checkout' }))
    fireEvent.change(screen.getByLabelText('Alert window (minutes)'), { target: { value: '360' } })
    fireEvent.change(screen.getByLabelText('Burn rate'), { target: { value: '6' } })
    fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'ops@example.com' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add alert' }))
    await waitFor(() => expect(sloApi.createAlert).toHaveBeenCalledWith(7, 3, {
      windowMinutes: 360, burnRate: 6, email: 'ops@example.com', webhookUrl: '', cooldownMinutes: 0, enabled: true,
    }))

    fireEvent.click(await screen.findByRole('button', { name: 'Edit 1h burn alert' }))
    expect(screen.getByLabelText('Burn rate')).toHaveValue('14.4')
    fireEvent.change(screen.getByLabelText('Burn rate'), { target: { value: '10' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save alert' }))
    await waitFor(() => expect(sloApi.updateAlert).toHaveBeenCalledWith(7, 3, 5, expect.objectContaining({ burnRate: 10, windowMinutes: 60 })))

    fireEvent.click(await screen.findByRole('button', { name: 'Delete 1h burn alert' }))
    await waitFor(() => expect(sloApi.removeAlert).toHaveBeenCalledWith(7, 3, 5))
  })
})
