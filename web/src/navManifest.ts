import {
  Activity, LayoutDashboard, Table2, LayoutGrid, BarChart2, Bell, GitBranch, Network, Search,
  Database, BrainCircuit, Radio, Settings, Globe, ScrollText, Target, Terminal,
  type LucideIcon,
} from 'lucide-react'

// Where an entry shows up on a phone: one of the four bottom tabs, the More
// menu, or nowhere. Every entry names one, so a page cannot be added to the
// desktop sidebar and forgotten on mobile.
type MobilePlacement = 'tab' | 'more' | 'hidden'

export interface NavEntry {
  to: string
  icon: LucideIcon
  label: string
  mobile: MobilePlacement
  // Shorter label for the bottom tab, where width is tight.
  tabLabel?: string
}

// Order is the desktop sidebar order. The More menu follows it too.
export const navManifest: readonly NavEntry[] = [
  { to: '/', icon: LayoutDashboard, label: 'Dashboard', mobile: 'tab' },
  { to: '/services', icon: Activity, label: 'Services', mobile: 'more' },
  { to: '/traces', icon: Search, label: 'Traces', mobile: 'tab' },
  { to: '/analyze', icon: Table2, label: 'Query', mobile: 'more' },
  { to: '/boards', icon: LayoutGrid, label: 'Boards', mobile: 'more' },
  { to: '/dependencies', icon: GitBranch, label: 'Dependencies', mobile: 'tab', tabLabel: 'Deps' },
  { to: '/service-map', icon: Network, label: 'Service Map', mobile: 'more' },
  { to: '/logs', icon: ScrollText, label: 'Logs', mobile: 'more' },
  { to: '/live', icon: Radio, label: 'Live Tail', mobile: 'more' },
  { to: '/database', icon: Database, label: 'Database', mobile: 'more' },
  { to: '/prompts', icon: BrainCircuit, label: 'Prompts', mobile: 'tab' },
  { to: '/pages', icon: Globe, label: 'Pages', mobile: 'more' },
  { to: '/metrics', icon: BarChart2, label: 'Metrics', mobile: 'more' },
  { to: '/claude-code', icon: Terminal, label: 'Claude Code', mobile: 'more' },
  { to: '/alerts', icon: Bell, label: 'Alerts', mobile: 'more' },
  { to: '/slos', icon: Target, label: 'SLOs', mobile: 'more' },
  { to: '/settings', icon: Settings, label: 'Settings', mobile: 'more' },
]

export const mobileTabs = navManifest.filter((e) => e.mobile === 'tab')
export const mobileMoreItems = navManifest.filter((e) => e.mobile === 'more')
