import { usd } from '../format'

// PriceBar puts the price between the two liquidation boundaries.
//
// It is the one picture the demo needs: how far the position is from the point
// where the covenant stops caring about the market and pays the clamped price.
export function PriceBar({
  price,
  low,
  high,
}: {
  price: number
  low: number
  high: number
}) {
  const span = high - low
  const clamped = Math.min(Math.max(price, low), high)
  const at = span > 0 ? ((clamped - low) / span) * 100 : 50

  const liquidated = price <= low || price >= high

  return (
    <div>
      <div className="relative h-8">
        <div className="absolute inset-x-0 top-3.5 h-1 rounded-full bg-gradient-to-r from-short/40 via-edge to-long/40" />
        <div
          className={`absolute top-1 h-6 w-0.5 -translate-x-1/2 rounded ${
            liquidated ? 'bg-bad' : 'bg-slate-100'
          }`}
          style={{ left: `${at}%` }}
        />
        <div
          className="absolute -top-0.5 -translate-x-1/2 whitespace-nowrap text-xs"
          style={{ left: `${at}%` }}
        >
          <span className={liquidated ? 'text-bad' : 'text-slate-100'}>{usd(price)}</span>
        </div>
      </div>

      <div className="flex justify-between text-xs text-muted">
        <span>{usd(low)} — the short is made whole</span>
        <span>{usd(high)} — the long takes it all</span>
      </div>

      {liquidated && (
        <p className="mt-2 text-xs text-bad">
          Past the boundary. The covenant clamps to it and settles, so this pays the same as any
          price further out — touching it <em>is</em> the event.
        </p>
      )}
    </div>
  )
}
