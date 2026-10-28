import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { SetupGuideNotice } from './SetupGuideNotice'

describe('SetupGuideNotice', () => {
  it('shows a new project guide with its key once', () => {
    const onDismiss = vi.fn()
    render(<SetupGuideNotice result={{ slug: 'acme', created: true, guide: '| API Key    | k1 |' }} onDismiss={onDismiss} />)
    expect(screen.getByText(/not shown again/)).toBeTruthy()
    expect((screen.getByLabelText('Setup guide') as HTMLTextAreaElement).value).toContain('k1')
    fireEvent.click(screen.getByText('Done'))
    expect(onDismiss).toHaveBeenCalledOnce()
  })

  it('says an existing project guide has no key', () => {
    render(<SetupGuideNotice result={{ slug: 'acme', created: false, guide: 'guide' }} onDismiss={() => {}} />)
    expect(screen.getByText(/already exists/)).toBeTruthy()
  })
})
