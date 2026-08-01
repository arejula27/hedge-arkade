import { useState } from 'react'
import { useNavigate } from 'react-router'
import { api } from '../api'
import { PriceBar } from '../components/PriceBar'
import { Button, Field, Notice, Panel } from '../components/ui'
import { sats, usd } from '../format'
import { usePoll } from '../hooks'

// The form works in dollars and BTC because that is what a person thinks in.
// Everything below it is cents per BTC and sats.
export function CreatePage() {
  const navigate = useNavigate()
  const { value: price } = usePoll(() => api.price(), 2000)

  const [side, setSide] = useState<'short' | 'long'>('short')
  const [hedgeUsd, setHedgeUsd] = useState(10_000)
  const [payoutBtc, setPayoutBtc] = useState(0.2)
  const [lowUsd, setLowUsd] = useState(50_000)
  const [highUsd, setHighUsd] = useState(200_000)
  const [hours, setHours] = useState(24)

  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const now = price?.price ?? 0
  const outside = now > 0 && (now <= lowUsd * 100 || now >= highUsd * 100)

  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      const contract = await api.propose({
        side,
        hedge_value_cents: Math.round(hedgeUsd * 100),
        payout_sats: Math.round(payoutBtc * 100_000_000),
        low_liquidation_cents: Math.round(lowUsd * 100),
        high_liquidation_cents: Math.round(highUsd * 100),
        maturity_in_seconds: Math.round(hours * 3600),
        enable_mutual_redemption: true,
      })
      void navigate(`/contracts/${contract.id}`)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Panel title="Open a position">
        <div className="mb-6 flex gap-2">
          <Side
            side="short"
            chosen={side}
            onPick={setSide}
            blurb="Lock in a dollar value. A crash pays you back the hedge."
          />
          <Side
            side="long"
            chosen={side}
            onPick={setSide}
            blurb="Post the collateral behind it and take the other side of the move."
          />
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <Number label="Hedge value" unit="$" value={hedgeUsd} onChange={setHedgeUsd} step={500} />
          <Number label="Total in the contract" unit="BTC" value={payoutBtc} onChange={setPayoutBtc} step={0.05} />
          <Number label="Low liquidation" unit="$" value={lowUsd} onChange={setLowUsd} step={5000} />
          <Number label="High liquidation" unit="$" value={highUsd} onChange={setHighUsd} step={5000} />
          <Number label="Matures in" unit="hours" value={hours} onChange={setHours} step={1} />
        </div>

        <div className="mt-6">
          <Notice>{error}</Notice>
        </div>

        <div className="mt-6 flex items-center gap-4">
          <Button tone="primary" onClick={submit} disabled={busy || outside}>
            {busy ? 'opening…' : `Offer the ${side}`}
          </Button>
          <span className="text-xs text-muted">
            The other side arrives when someone takes it. The address is a function of both payout
            scripts, so there is nothing to fund until then.
          </span>
        </div>
      </Panel>

      <Panel title="Where it sits">
        {price && (
          <>
            <PriceBar price={price.price} low={lowUsd * 100} high={highUsd * 100} />
            {outside && (
              <p className="mt-3 text-sm text-bad">
                The price is already outside these boundaries, so the contract would liquidate the
                instant it was funded. That is a way to lose money to a typo rather than to the
                market, so it is refused.
              </p>
            )}
          </>
        )}

        <dl className="mt-6 grid gap-4 text-sm sm:grid-cols-3">
          <Field label="Each side puts in">
            <span className="mono">about {sats(Math.round((payoutBtc * 100_000_000) / 2))}</span>
          </Field>
          <Field label="Decided at">{price ? usd(price.price) : '—'}</Field>
          <Field label="Split at that price">
            what the covenant would pay back, so settling at once moves nothing
          </Field>
        </dl>
      </Panel>
    </>
  )
}

function Side({
  side,
  chosen,
  onPick,
  blurb,
}: {
  side: 'short' | 'long'
  chosen: string
  onPick: (s: 'short' | 'long') => void
  blurb: string
}) {
  const active = chosen === side
  const tone = side === 'short' ? 'text-short' : 'text-long'

  return (
    <button
      type="button"
      onClick={() => onPick(side)}
      className={`flex-1 rounded-lg border p-4 text-left transition-colors ${
        active ? 'border-slate-500 bg-edge/60' : 'border-edge hover:border-slate-600'
      }`}
    >
      <div className={`text-sm font-medium ${tone}`}>{side}</div>
      <div className="mt-1 text-xs text-muted">{blurb}</div>
    </button>
  )
}

function Number({
  label,
  unit,
  value,
  onChange,
  step,
}: {
  label: string
  unit: string
  value: number
  onChange: (v: number) => void
  step: number
}) {
  return (
    <label className="block">
      <span className="text-xs uppercase tracking-wide text-muted">{label}</span>
      <span className="mt-1 flex items-center rounded-md border border-edge bg-ink">
        <input
          type="number"
          value={value}
          step={step}
          min={0}
          onChange={(e) => onChange(e.target.valueAsNumber || 0)}
          className="mono w-full bg-transparent px-3 py-1.5 outline-none"
        />
        <span className="px-3 text-xs text-muted">{unit}</span>
      </span>
    </label>
  )
}
