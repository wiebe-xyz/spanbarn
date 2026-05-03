import { BrowserRouter, Routes, Route } from 'react-router-dom'
import { LoginPage } from './pages/LoginPage'
import { ServicesPage } from './pages/ServicesPage'
import { OperationsPage } from './pages/OperationsPage'
import { OperationDetailPage } from './pages/OperationDetailPage'
import { TracesPage } from './pages/TracesPage'
import { TraceDetailPage } from './pages/TraceDetailPage'
import { DependenciesPage } from './pages/DependenciesPage'
import { DashboardLayout } from './components/DashboardLayout'

function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/" element={<DashboardLayout />}>
          <Route index element={<ServicesPage />} />
          <Route path="services/:service" element={<OperationsPage />} />
          <Route path="services/:service/operations/:operation" element={<OperationDetailPage />} />
          <Route path="traces" element={<TracesPage />} />
          <Route path="traces/:traceId" element={<TraceDetailPage />} />
          <Route path="dependencies" element={<DependenciesPage />} />
        </Route>
      </Routes>
    </BrowserRouter>
  )
}

export default App
