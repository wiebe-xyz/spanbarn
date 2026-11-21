import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { ProjectSwitcher } from './ProjectSwitcher'

const projects = [
  { id: 1, name: 'zeta', slug: 'zeta' },
  { id: 2, name: 'Alpha', slug: 'alpha-site' },
  { id: 3, name: 'beta', slug: 'bravo' },
]

function mount(onChange = vi.fn(), value = '2', allLabel?: string) {
  render(<ProjectSwitcher projects={projects} value={value} onChange={onChange} allLabel={allLabel} />)
  fireEvent.click(screen.getByRole('button', { name: 'Project' }))
  return onChange
}

const rowNames = () =>
  screen.getAllByRole('button').filter((b) => !b.getAttribute('aria-label')).map((b) => b.textContent?.replace('✓ ', ''))

describe('ProjectSwitcher', () => {
  beforeEach(() => localStorage.clear())
  afterEach(cleanup)

  it('lists projects alphabetically, marks the current one and focuses search', () => {
    mount()
    expect(rowNames()).toEqual(['Alpha', 'beta', 'zeta'])
    expect(screen.getByText('✓ Alpha')).toHaveAttribute('aria-current', 'true')
    expect(screen.getByLabelText('Search projects')).toHaveFocus()
  })

  it('filters on name and slug and shows an empty message', () => {
    mount()
    const box = screen.getByLabelText('Search projects')
    fireEvent.change(box, { target: { value: 'bravo' } })
    expect(rowNames()).toEqual(['beta'])
    fireEvent.change(box, { target: { value: 'nothing' } })
    expect(screen.getByText('No projects match')).toBeInTheDocument()
  })

  it('picks the first match on Enter and closes on Escape', () => {
    const onChange = mount()
    fireEvent.change(screen.getByLabelText('Search projects'), { target: { value: 'e' } })
    fireEvent.keyDown(screen.getByLabelText('Search projects'), { key: 'Enter' })
    expect(onChange).toHaveBeenCalledWith('3')
    expect(screen.queryByLabelText('Search projects')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Project' }))
    fireEvent.keyDown(screen.getByLabelText('Search projects'), { key: 'Escape' })
    expect(screen.queryByLabelText('Search projects')).toBeNull()
  })

  it('shows starred projects first in their own group and remembers them', () => {
    mount()
    fireEvent.click(screen.getByRole('button', { name: 'Favourite zeta' }))
    expect(screen.getByRole('button', { name: 'Favourite zeta' })).toHaveAttribute('aria-pressed', 'true')
    expect(rowNames()).toEqual(['zeta', 'Alpha', 'beta'])
    expect(screen.getByText('Favourites')).toBeInTheDocument()
    expect(JSON.parse(localStorage.getItem('spanbarn.favourite-projects') ?? '[]')).toEqual([1])

    cleanup()
    mount()
    expect(rowNames()).toEqual(['zeta', 'Alpha', 'beta'])
  })

  it('offers an all-projects row that selects 0', () => {
    const onChange = mount(vi.fn(), '3', 'All projects')
    fireEvent.click(screen.getByText('All projects'))
    expect(onChange).toHaveBeenCalledWith('0')
  })

  it('scrolls inside the menu and locks the page behind it while open', () => {
    mount()
    const list = screen.getByRole('group', { name: 'Projects' })
    expect(list.style.maxHeight).toBe('60vh')
    expect(document.body.style.overflow).toBe('hidden')
    fireEvent.keyDown(screen.getByLabelText('Search projects'), { key: 'Escape' })
    expect(document.body.style.overflow).toBe('')
  })
})
