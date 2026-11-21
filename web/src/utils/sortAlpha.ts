const collator = new Intl.Collator('en', { sensitivity: 'base', numeric: true })

/**
 * Returns a copy of items ordered alphabetically by label, ignoring case.
 * Every select that lists data (services, projects, environments, operations)
 * runs its options through this, so a new select gets the same order.
 * Fixed vocabularies with a meaning of their own (time ranges, limits, status)
 * keep their order and do not use it.
 */
export function sortAlpha<T>(items: readonly T[], label: (item: T) => string = String): T[] {
  return [...items].sort((a, b) => collator.compare(label(a), label(b)))
}
