import { useState } from 'react'
import { api } from '../api'
import { Button, Field, Notice, Panel } from '../components/ui'
import { usd, when } from '../format'
import { usePoll } from '../hooks'
import type { Price } from '../types'

export function OraclePage() {
  const { value: price, reload: reloadPrice } = usePoll(() => api.price(), 2000)
  const { value: history, reload: reloadHistory } = usePoll(() => api.priceHistory(60), 2000)

  const [next, setNext] = useState(100_000)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const publish = async () => {
    setBusy(true)
    setError(null)
    try {
      await api.setPrice(Math.round(next * 100))
      void reloadPrice()
      void reloadHistory()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Panel title="The oracle">
        <dl className="grid gap-4 sm:grid-cols-3">
          <Field label="Price">
            <span className="mono text-2xl">{price ? usd(price.price) : '—'}</span>
          </Field>
          <Field label="Sequence">
            <span className="mono">{price?.sequence ?? '—'}</span>
          </Field>
          <Field label="Published">{price ? when(price.timestamp) : '—'}</Field>
        </dl>

        <p className="mt-4 text-sm text-muted">
          A separate process that knows about no contract. It signs a 24-byte message on a fixed
          cadence and stores it, and that is all it does.
        </p>
        <p className="mt-2 text-sm text-muted">
          The sequence has no gaps, and it cannot have any: settling needs a message{' '}
          <em>and the one immediately before it</em>, so a number that was never published makes
          every settlement that would have needed it impossible.
        </p>
      </Panel>

      <Panel title="Move it">
        <div className="flex flex-wrap items-end gap-3">
          <label className="block">
            <span className="text-xs uppercase tracking-wide text-muted">New price</span>
            <span className="mt-1 flex items-center rounded-md border border-edge bg-ink">
              <span className="pl-3 text-muted">$</span>
              <input
                type="number"
                value={next}
                step={1000}
                min={1}
                onChange={(e) => setNext(e.target.valueAsNumber || 0)}
                className="mono w-40 bg-transparent px-2 py-1.5 outline-none"
              />
            </span>
          </label>

          <Button tone="primary" onClick={publish} disabled={busy || next <= 0}>
            {busy ? 'publishing…' : 'Publish'}
          </Button>
        </div>

        <p className="mt-3 text-xs text-muted">
          Taking a price over HTTP is a demo control. A real feed reads a market, and an oracle that
          can be told what to say is one nobody should build a contract against — which is why the
          oracle refuses this unless it was started with manual publication on.
        </p>

        <div className="mt-3">
          <Notice>{error}</Notice>
        </div>
      </Panel>

      <Panel title="Published">
        <Chart history={history ?? []} />
      </Panel>
    </>
  )
}

function Chart({ history }: { history: Price[] }) {
  if (history.length < 2) return <p className="text-sm text-muted">Not enough published yet.</p>

  // Newest first from the API; a chart reads left to right.
  const points = [...history].reverse()
  const prices = points.map((p) => p.price)
  const low = Math.min(...prices)
  const high = Math.max(...prices)
  const span = high - low || 1

  const path = points
    .map((p, i) => {
      const x = (i / (points.length - 1)) * 100
      const y = 100 - ((p.price - low) / span) * 100
      return `${i === 0 ? 'M' : 'L'}${x.toFixed(2)},${y.toFixed(2)}`
    })
    .join(' ')

  return (
    <div>
      <svg viewBox="0 0 100 100" preserveAspectRatio="none" className="h-40 w-full">
        <path d={path} fill="none" stroke="currentColor" strokeWidth="0.6" className="text-short" />
      </svg>
      <div className="mt-1 flex justify-between text-xs text-muted">
        <span>{usd(low)}</span>
        <span>
          {points.length} publications, sequence {points[0].sequence} —{' '}
          {points[points.length - 1].sequence}
        </span>
        <span>{usd(high)}</span>
      </div>
    </div>
  )
}
