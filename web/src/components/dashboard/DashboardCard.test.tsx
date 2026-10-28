import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { DashboardCard } from './DashboardCard'

function card(source?: 'rollups' | 'spans') {
  render(
    <DashboardCard title="Counts" info="info" loading={false} error={null} empty={false} source={source}>
      <div />
    </DashboardCard>,
  )
}

describe('DashboardCard source label', () => {
  it('says rollup counts are received spans', () => {
    card('rollups')
    expect(screen.getByTestId('source-label')).toHaveTextContent('received spans')
  })

  it('says raw counts are stored spans', () => {
    card('spans')
    expect(screen.getByTestId('source-label')).toHaveTextContent('stored spans')
  })

  it('shows no label when the server names no source', () => {
    card()
    expect(screen.queryByTestId('source-label')).toBeNull()
  })
})
