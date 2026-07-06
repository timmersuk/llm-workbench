import { defineConfig, mergeConfig } from 'vitest/config'
import viteConfig from './vite.config'

// A sibling config, not a `test` block merged into vite.config.ts: that
// file is part of tsconfig.node.json's tsc -b pass (its "include" is
// exactly ["vite.config.ts"]), which also drives the production build —
// adding a `test` field would require importing vitest/config there,
// putting a devDependency on the production build's type-check path.
// mergeConfig still gives test files the same @vitejs/plugin-react JSX
// transform used in production.
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: 'jsdom',
      setupFiles: ['./src/setupTests.ts'],
    },
  }),
)
