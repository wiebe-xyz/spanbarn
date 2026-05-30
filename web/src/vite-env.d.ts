/// <reference types="vite/client" />

import type React from 'react'

declare module 'react' {
  // eslint-disable-next-line @typescript-eslint/no-namespace
  namespace JSX {
    interface IntrinsicElements {
      'iambarn-profile': React.DetailedHTMLProps<
        React.HTMLAttributes<HTMLElement> & {
          'server-url'?: string
          sections?: string
        },
        HTMLElement
      >
    }
  }
}
