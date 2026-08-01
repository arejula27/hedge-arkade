import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from './api'
import type { StreamEvent } from './types'

// usePoll re-reads something on an interval and after any call to reload.
//
// Most of what the UI shows is cheap enough to poll. Only contract transitions
// are pushed, because they are the one thing that happens without the person
// looking at the screen having done anything.
export function usePoll<T>(load: () => Promise<T>, everyMs = 2000) {
  const [value, setValue] = useState<T | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  // The loader is captured in a ref so a caller can pass an inline closure
  // without restarting the timer on every render.
  const loader = useRef(load)
  loader.current = load

  const reload = useCallback(async () => {
    try {
      setValue(await loader.current())
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    let live = true
    const tick = () => {
      if (live) void reload()
    }

    tick()
    if (everyMs <= 0) return

    const timer = setInterval(tick, everyMs)
    return () => {
      live = false
      clearInterval(timer)
    }
  }, [reload, everyMs])

  return { value, error, loading, reload }
}

// useContractEvents watches the server's stream.
//
// This is what makes the demo readable: the tab that did not click sees the
// contract change state at the same moment as the one that did.
export function useContractEvents(contract: string | undefined, onEvent: () => void) {
  const handler = useRef(onEvent)
  handler.current = onEvent

  const [connected, setConnected] = useState(false)

  useEffect(() => {
    const source = new EventSource(api.eventsURL(contract))

    source.onopen = () => setConnected(true)
    source.onerror = () => setConnected(false)
    source.onmessage = (message) => {
      try {
        const event = JSON.parse(message.data) as StreamEvent
        if (!contract || event.contract === contract) handler.current()
      } catch {
        // A frame we cannot read is not worth taking the stream down for.
      }
    }

    return () => source.close()
  }, [contract])

  return connected
}
