import { useEffect, useRef, useState } from 'react'

// charsPerTokenEstimate is a rough English-text heuristic (~4 chars/token),
// used only while a turn is still streaming — neither backend integration
// exposes a live running token count (OpenAI-SDK streaming's usage chunk
// and the claude/codex CLI's usage report both only arrive once, at the
// very end of a run), so this is deliberately approximate and always
// labeled with "~" by callers, snapping to the real total the instant one
// arrives (docs/adr/0018).
const charsPerTokenEstimate = 4

export interface LiveTurnStatus {
  // elapsedSeconds counts up from 0 for as long as active stays true, reset
  // to 0 the next time it turns true again.
  elapsedSeconds: number
  // tokens is the best number available right now: the real total once
  // finalTotalTokens arrives, otherwise a live estimate from streamedChars.
  tokens: number
  // isEstimate is false once tokens reflects finalTotalTokens rather than
  // the character-count heuristic — callers use this to decide whether to
  // show the "~" prefix.
  isEstimate: boolean
}

// useLiveTurnStatus tracks how long a turn has been in flight and a token
// count for it, so a chat/stage-conversation panel can show something
// visibly moving during the gap before an agent's first reply rather than a
// silent pause (docs/adr/0018) — the same motivation as the animated
// "working" indicator callers render alongside this. finalTotalTokens
// should be left undefined until the stream's real usage total (if any)
// arrives; once set, tokens/isEstimate snap to it instead of the running
// estimate.
export function useLiveTurnStatus(active: boolean, streamedChars: number, finalTotalTokens?: number): LiveTurnStatus {
  const [elapsedSeconds, setElapsedSeconds] = useState(0)
  const startRef = useRef<number | null>(null)

  useEffect(() => {
    if (!active) {
      startRef.current = null
      return
    }
    startRef.current = Date.now()
    setElapsedSeconds(0)
    const id = setInterval(() => {
      if (startRef.current !== null) {
        setElapsedSeconds(Math.floor((Date.now() - startRef.current) / 1000))
      }
    }, 1000)
    return () => clearInterval(id)
  }, [active])

  if (finalTotalTokens !== undefined) {
    return { elapsedSeconds, tokens: finalTotalTokens, isEstimate: false }
  }
  return { elapsedSeconds, tokens: Math.round(streamedChars / charsPerTokenEstimate), isEstimate: true }
}
