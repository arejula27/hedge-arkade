import { sats } from '../format'

// Split shows what each side would be paid, as a bar and as numbers.
export function Split({
  short,
  long,
  shortName,
  longName,
  shortStake,
  longStake,
}: {
  short: number
  long: number
  shortName?: string
  longName?: string
  shortStake?: number
  longStake?: number
}) {
  const total = short + long
  const shortShare = total > 0 ? (short / total) * 100 : 50

  return (
    <div>
      <div className="flex h-2 overflow-hidden rounded-full bg-edge">
        <div className="bg-short" style={{ width: `${shortShare}%` }} />
        <div className="bg-long" style={{ width: `${100 - shortShare}%` }} />
      </div>

      <div className="mt-3 grid grid-cols-2 gap-4 text-sm">
        <Side
          tone="short"
          label={`short${shortName ? ` · ${shortName}` : ''}`}
          paid={short}
          stake={shortStake}
        />
        <Side
          tone="long"
          label={`long${longName ? ` · ${longName}` : ''}`}
          paid={long}
          stake={longStake}
          align="right"
        />
      </div>
    </div>
  )
}

function Side({
  tone,
  label,
  paid,
  stake,
  align = 'left',
}: {
  tone: 'short' | 'long'
  label: string
  paid: number
  stake?: number
  align?: 'left' | 'right'
}) {
  const change = stake === undefined ? null : paid - stake

  return (
    <div className={align === 'right' ? 'text-right' : ''}>
      <div className={`text-xs uppercase tracking-wide ${tone === 'short' ? 'text-short' : 'text-long'}`}>
        {label}
      </div>
      <div className="mono mt-0.5">{sats(paid)}</div>
      {change !== null && (
        <div className={`text-xs ${change >= 0 ? 'text-short' : 'text-bad'}`}>
          {change >= 0 ? '+' : ''}
          {change.toLocaleString('en-US')} against a stake of {stake?.toLocaleString('en-US')}
        </div>
      )}
    </div>
  )
}
