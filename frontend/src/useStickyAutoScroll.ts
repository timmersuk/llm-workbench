import { useEffect, useRef } from 'react'

// bottomThresholdPx is how close to the bottom "already at the bottom"
// counts as, given rounding/sub-pixel scroll positions.
const bottomThresholdPx = 48

// useStickyAutoScroll keeps a scrolling container pinned to its latest
// content as `dependency` changes, but only while the user is already
// scrolled near the bottom (docs/adr/0019) — new tool activity or message
// content shouldn't yank a human back down while they've scrolled up to
// read earlier history. Attach the returned ref to the scrolling element
// itself (the one with overflow-y: auto), not an outer wrapper.
export function useStickyAutoScroll<T>(dependency: T) {
  const ref = useRef<HTMLDivElement | null>(null)
  // stickyRef tracks "should the next content update scroll to bottom" as a
  // ref, not state — it's read/written on scroll and content-change effects
  // that must not themselves trigger a re-render.
  const stickyRef = useRef(true)

  useEffect(() => {
    const el = ref.current
    if (!el) {
      return
    }
    function handleScroll() {
      if (!el) {
        return
      }
      stickyRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < bottomThresholdPx
    }
    el.addEventListener('scroll', handleScroll)
    return () => el.removeEventListener('scroll', handleScroll)
  }, [])

  useEffect(() => {
    const el = ref.current
    if (el && stickyRef.current) {
      el.scrollTop = el.scrollHeight
    }
  }, [dependency])

  return ref
}
