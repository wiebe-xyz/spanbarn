/// <reference types="vite/client" />

import type React from 'react'

declare module 'react' {
  namespace JSX {
    interface IntrinsicElements {
      'iambarn-profile': React.DetailedHTMLProps<
        React.HTMLAttributes<HTMLElement> & {
          'server-url'?: string
          sections?: string
        },
        HTMLElement
      >
      'iambarn-user-badge': React.DetailedHTMLProps<
        React.HTMLAttributes<HTMLElement> & {
          'server-url'?: string
          // Empty string = attribute present (widget uses hasAttribute).
          'show-email'?: string
        },
        HTMLElement
      >
      'iambarn-avatar': React.DetailedHTMLProps<
        React.HTMLAttributes<HTMLElement> & {
          'server-url'?: string
          size?: number | string
        },
        HTMLElement
      >
    }
  }
}
