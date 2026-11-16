import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App'
import { initInstrumentation } from './instrumentation'
import { initFunnelBarn } from './funnelbarn'
import { initBrandTrace } from './brandtrace'

initInstrumentation()
void initFunnelBarn()
void initBrandTrace()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
