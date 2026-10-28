import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { SourceLabel } from './SourceLabel'

describe('SourceLabel', () => {
  it('says aggregate counts are estimated spans', () => {
    render(<SourceLabel source="aggregates" />)
    expect(screen.getByTestId('source-label')).toHaveTextContent('estimated spans')
    expect(screen.getByTestId('source-label').getAttribute('title')).toContain('sample rate')
  })
})
