// refractor@3.x ships no type declarations of its own, and the DefinitelyTyped
// `@types/refractor` package would be a fourth new dependency purely to type
// a handful of calls — this narrow ambient shim covers exactly what DiffView.tsx
// (and, via a type-only import, react-diff-view's own .d.ts) needs instead.
//
// react-diff-view@3.3.3's bundled types (types/tokenize/toTokenTrees.d.ts) do
// `import type { highlight } from 'refractor'`, so the named export below has
// to exist even though DiffView.tsx itself only ever uses the default export.
declare module 'refractor' {
  export interface RefractorSyntax {
    displayName: string
    aliases: string[]
    (prism: unknown): void
  }

  export interface RefractorNode {
    type: string
    tagName?: string
    properties?: { className?: string[] }
    value?: string
    children?: RefractorNode[]
  }

  export function highlight(value: string, name: string): RefractorNode[]
  export function register(syntax: RefractorSyntax): void
  export function registered(name: string): boolean

  interface Refractor {
    highlight: typeof highlight
    register: typeof register
    registered: typeof registered
  }

  const refractor: Refractor
  export default refractor
}

declare module 'refractor/lang/*' {
  import type { RefractorSyntax } from 'refractor'

  const syntax: RefractorSyntax
  export default syntax
}
