import { lazy, Suspense } from 'react'
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { DashboardLayout } from './components/DashboardLayout'
import { TimeRangeProvider } from './contexts/TimeRangeContext'

// Route-level code splitting: each page becomes its own chunk so initial
// load only ships React + shell + the page you actually opened, instead of
// the entire dashboard. Pages export named components, so unwrap them here.
const LoginPage = lazy(() => import('./pages/LoginPage').then(m => ({ default: m.LoginPage })))
const ServicesPage = lazy(() => import('./pages/ServicesPage').then(m => ({ default: m.ServicesPage })))
const OperationsPage = lazy(() => import('./pages/OperationsPage').then(m => ({ default: m.OperationsPage })))
const OperationDetailPage = lazy(() => import('./pages/OperationDetailPage').then(m => ({ default: m.OperationDetailPage })))
const TracesPage = lazy(() => import('./pages/TracesPage').then(m => ({ default: m.TracesPage })))
const TraceDetailPage = lazy(() => import('./pages/TraceDetailPage').then(m => ({ default: m.TraceDetailPage })))
const DependenciesPage = lazy(() => import('./pages/DependenciesPage').then(m => ({ default: m.DependenciesPage })))
const ServiceMapPage = lazy(() => import('./pages/ServiceMapPage').then(m => ({ default: m.ServiceMapPage })))
const LiveTailPage = lazy(() => import('./pages/LiveTailPage').then(m => ({ default: m.LiveTailPage })))
const DatabasePage = lazy(() => import('./pages/DatabasePage').then(m => ({ default: m.DatabasePage })))
const DatabaseQueryDetailPage = lazy(() => import('./pages/DatabaseQueryDetailPage').then(m => ({ default: m.DatabaseQueryDetailPage })))
const PromptsPage = lazy(() => import('./pages/PromptsPage').then(m => ({ default: m.PromptsPage })))
const PromptDetailPage = lazy(() => import('./pages/PromptDetailPage').then(m => ({ default: m.PromptDetailPage })))
const PagesPage = lazy(() => import('./pages/PagesPage').then(m => ({ default: m.PagesPage })))
const PageDetailPage = lazy(() => import('./pages/PageDetailPage').then(m => ({ default: m.PageDetailPage })))
const AlertsPage = lazy(() => import('./pages/AlertsPage').then(m => ({ default: m.AlertsPage })))
const MetricsPage = lazy(() => import('./pages/MetricsPage').then(m => ({ default: m.MetricsPage })))
const LogsPage = lazy(() => import('./pages/LogsPage').then(m => ({ default: m.LogsPage })))
const SettingsPage = lazy(() => import('./pages/SettingsPage').then(m => ({ default: m.SettingsPage })))
const ProfilePage = lazy(() => import('./pages/ProfilePage').then(m => ({ default: m.ProfilePage })))

function App() {
  return (
    <BrowserRouter>
      <TimeRangeProvider>
        <Suspense fallback={<div style={{ padding: 24, color: '#9ca3af' }}>Loading…</div>}>
          <Routes>
            <Route path="/login" element={<LoginPage />} />
            <Route path="/" element={<DashboardLayout />}>
              <Route index element={<ServicesPage />} />
              <Route path="services/:service" element={<OperationsPage />} />
              <Route path="services/:service/operations/:operation" element={<OperationDetailPage />} />
              <Route path="traces" element={<TracesPage />} />
              <Route path="traces/:traceId" element={<TraceDetailPage />} />
              <Route path="dependencies" element={<DependenciesPage />} />
              <Route path="service-map" element={<ServiceMapPage />} />
              <Route path="logs" element={<LogsPage />} />
              <Route path="live" element={<LiveTailPage />} />
              <Route path="database" element={<DatabasePage />} />
              <Route path="database/detail" element={<DatabaseQueryDetailPage />} />
              <Route path="prompts" element={<PromptsPage />} />
              <Route path="prompts/:name" element={<PromptDetailPage />} />
              <Route path="pages" element={<PagesPage />} />
              <Route path="pages/:page" element={<PageDetailPage />} />
              <Route path="alerts" element={<AlertsPage />} />
              <Route path="metrics" element={<MetricsPage />} />
              <Route path="settings" element={<SettingsPage />} />
              <Route path="account" element={<ProfilePage />} />
              {/* Back-compat: the account page used to live at /profile. */}
              <Route path="profile" element={<Navigate to="/account" replace />} />
            </Route>
          </Routes>
        </Suspense>
      </TimeRangeProvider>
    </BrowserRouter>
  )
}

export default App
