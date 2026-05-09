import { BrowserRouter, Routes, Route } from 'react-router-dom'
import { LoginPage } from './pages/LoginPage'
import { ServicesPage } from './pages/ServicesPage'
import { OperationsPage } from './pages/OperationsPage'
import { OperationDetailPage } from './pages/OperationDetailPage'
import { TracesPage } from './pages/TracesPage'
import { TraceDetailPage } from './pages/TraceDetailPage'
import { DependenciesPage } from './pages/DependenciesPage'
import { ServiceMapPage } from './pages/ServiceMapPage'
import { LiveTailPage } from './pages/LiveTailPage'
import { DatabasePage } from './pages/DatabasePage'
import { DatabaseQueryDetailPage } from './pages/DatabaseQueryDetailPage'
import { PromptsPage } from './pages/PromptsPage'
import { PromptDetailPage } from './pages/PromptDetailPage'
import { PagesPage } from './pages/PagesPage'
import { SettingsPage } from './pages/SettingsPage'
import { DashboardLayout } from './components/DashboardLayout'
import { TimeRangeProvider } from './contexts/TimeRangeContext'

function App() {
  return (
    <BrowserRouter>
      <TimeRangeProvider>
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
            <Route path="live" element={<LiveTailPage />} />
            <Route path="database" element={<DatabasePage />} />
            <Route path="database/detail" element={<DatabaseQueryDetailPage />} />
            <Route path="prompts" element={<PromptsPage />} />
            <Route path="prompts/:name" element={<PromptDetailPage />} />
            <Route path="pages" element={<PagesPage />} />
            <Route path="settings" element={<SettingsPage />} />
          </Route>
        </Routes>
      </TimeRangeProvider>
    </BrowserRouter>
  )
}

export default App
