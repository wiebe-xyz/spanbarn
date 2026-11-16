import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { EnvironmentSelect } from './EnvironmentSelect'
import { api } from '../api/client'
import { ALL_ENVIRONMENTS, setEnvironment, withEnvironment } from '../environment'

describe('EnvironmentSelect', () => {
  beforeEach(() => {
    setEnvironment('production')
    vi.spyOn(api, 'getEnvironments').mockResolvedValue([
      { environment: 'production', spans: 30 },
      { environment: 'staging', spans: 4 },
    ])
  })
  afterEach(() => vi.restoreAllMocks())

  it('offers all and the environments the data holds, production selected', async () => {
    render(<EnvironmentSelect />)
    await waitFor(() => expect(screen.getByRole('option', { name: 'staging' })).toBeDefined())
    expect((screen.getByLabelText('Environment') as HTMLSelectElement).value).toBe('production')
    expect(screen.getByRole('option', { name: 'all' })).toBeDefined()
  })

  it('changes what the reads send', async () => {
    render(<EnvironmentSelect />)
    await waitFor(() => expect(screen.getByRole('option', { name: 'staging' })).toBeDefined())
    fireEvent.change(screen.getByLabelText('Environment'), { target: { value: 'staging' } })
    expect(withEnvironment('/api/v1/traces')).toBe('/api/v1/traces?environment=staging')
    fireEvent.change(screen.getByLabelText('Environment'), { target: { value: ALL_ENVIRONMENTS } })
    expect(withEnvironment('/api/v1/traces')).toBe('/api/v1/traces')
  })

  it('still lists the current choice when the listing fails', async () => {
    vi.spyOn(api, 'getEnvironments').mockRejectedValue(new Error('down'))
    render(<EnvironmentSelect />)
    await waitFor(() => expect(screen.getByRole('option', { name: 'production' })).toBeDefined())
  })
})
