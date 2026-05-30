/// <reference types="vite/client" />

declare global {
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
