import { describe, expect, it } from 'vitest'
import { arrangeProjects, firstMatch, type MenuProject } from './projectMenu'

const projects: MenuProject[] = [
  { id: 1, name: 'zeta', slug: 'zeta' },
  { id: 2, name: 'Alpha', slug: 'alpha-site' },
  { id: 3, name: 'beta', slug: 'bravo' },
  { id: 4, name: 'Gamma', slug: 'gamma' },
]

const names = (list: MenuProject[]) => list.map((p) => p.name)

describe('arrangeProjects', () => {
  it('sorts alphabetically ignoring case', () => {
    const menu = arrangeProjects(projects, '', new Set())
    expect(names(menu.others)).toEqual(['Alpha', 'beta', 'Gamma', 'zeta'])
    expect(menu.favourites).toEqual([])
  })

  it('filters on name and on slug, ignoring case', () => {
    expect(names(arrangeProjects(projects, 'GAM', new Set()).others)).toEqual(['Gamma'])
    expect(names(arrangeProjects(projects, 'bravo', new Set()).others)).toEqual(['beta'])
    expect(arrangeProjects(projects, 'nothing', new Set())).toEqual({ favourites: [], others: [] })
  })

  it('puts starred projects in their own alphabetical group first', () => {
    const menu = arrangeProjects(projects, '', new Set([1, 4]))
    expect(names(menu.favourites)).toEqual(['Gamma', 'zeta'])
    expect(names(menu.others)).toEqual(['Alpha', 'beta'])
  })

  it('applies the filter to both groups', () => {
    const menu = arrangeProjects(projects, 'a', new Set([1]))
    expect(names(menu.favourites)).toEqual(['zeta'])
    expect(names(menu.others)).toEqual(['Alpha', 'beta', 'Gamma'])
  })
})

describe('firstMatch', () => {
  it('prefers the first favourite, then the first other', () => {
    expect(firstMatch(arrangeProjects(projects, '', new Set([4])))?.name).toBe('Gamma')
    expect(firstMatch(arrangeProjects(projects, '', new Set()))?.name).toBe('Alpha')
    expect(firstMatch(arrangeProjects(projects, 'zzz', new Set()))).toBeUndefined()
  })
})
