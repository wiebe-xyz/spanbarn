import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { TraceStructureBadges } from './TraceStructureBadges'

describe('TraceStructureBadges', () => {
  it('renders nothing for a healthy trace', () => {
    const { container } = render(<TraceStructureBadges trace={{ hasRoot: true, orphanCount: 0 }} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('renders nothing while the structure is not computed', () => {
    const { container } = render(<TraceStructureBadges trace={{ hasRoot: null, orphanCount: 0 }} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('flags a trace without a root', () => {
    render(<TraceStructureBadges trace={{ hasRoot: false, orphanCount: 0 }} />)
    expect(screen.getByText('no root span')).toBeInTheDocument()
  })

  it('counts orphan spans', () => {
    render(<TraceStructureBadges trace={{ hasRoot: true, orphanCount: 3 }} />)
    expect(screen.getByText('3 orphan spans')).toBeInTheDocument()
    expect(screen.queryByText('no root span')).not.toBeInTheDocument()
  })
})
