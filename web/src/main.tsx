import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App'
import { initInstrumentation } from './instrumentation'
import { initFunnelBarn } from './funnelbarn'

initInstrumentation()
void initFunnelBarn()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
