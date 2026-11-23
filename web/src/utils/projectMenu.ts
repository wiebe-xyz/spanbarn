import { sortAlpha } from './sortAlpha'

export type MenuProject = { id: number; name: string; slug: string }

export type ProjectMenu = { favourites: MenuProject[]; others: MenuProject[] }

/**
 * Arranges the switcher's rows: projects matching the query on name or slug,
 * starred ones first in their own group, each group sorted alphabetically.
 */
export function arrangeProjects(
  projects: readonly MenuProject[],
  query: string,
  favouriteIds: ReadonlySet<number>,
): ProjectMenu {
  const q = query.trim().toLowerCase()
  const hits = q
    ? projects.filter((p) => p.name.toLowerCase().includes(q) || p.slug.toLowerCase().includes(q))
    : projects
  const byName = (list: MenuProject[]) => sortAlpha(list, (p) => p.name)
  return {
    favourites: byName(hits.filter((p) => favouriteIds.has(p.id))),
    others: byName(hits.filter((p) => !favouriteIds.has(p.id))),
  }
}

/** The project Enter picks: the first row in display order. */
export function firstMatch(menu: ProjectMenu): MenuProject | undefined {
  return menu.favourites[0] ?? menu.others[0]
}
