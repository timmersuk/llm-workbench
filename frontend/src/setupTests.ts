import { afterEach, beforeEach, vi } from 'vitest'
import { cleanup } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

// test.globals stays false (vitest.config.ts) — every test file explicitly
// imports from 'vitest', matching this codebase's verbatimModuleSyntax/
// explicit-imports style. Since globals is off, @testing-library/react's
// auto-cleanup (which only registers when it detects a global afterEach)
// doesn't fire on its own, hence the manual call here.
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

// vi.restoreAllMocks() (above) restores vi.spyOn-created mocks to their
// original implementation, but doesn't reliably clear call history on
// vi.mock()-based auto-mocked module exports — without this, call counts
// on api.ts's mocked functions accumulate across every test in a file
// instead of resetting per-test, which silently inflates
// toHaveBeenCalledTimes assertions.
beforeEach(() => {
  vi.clearAllMocks()
})
