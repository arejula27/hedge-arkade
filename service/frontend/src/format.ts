// Prices are in cents per BTC and money is in sats. Mixing the two up is the
// easiest mistake to make here, so the conversions live in one place.

export function usd(cents: number): string {
  return (cents / 100).toLocaleString('en-US', {
    style: 'currency',
    currency: 'USD',
    maximumFractionDigits: 0,
  })
}

export function sats(value: number): string {
  return value.toLocaleString('en-US') + ' sats'
}

export function btc(value: number): string {
  return (value / 100_000_000).toFixed(8).replace(/0+$/, '').replace(/\.$/, '') + ' BTC'
}

export function shortId(id: string): string {
  return id.slice(0, 8)
}

export function shortHex(hex: string, keep = 8): string {
  if (hex.length <= keep * 2) return hex
  return `${hex.slice(0, keep)}…${hex.slice(-keep)}`
}

export function when(unix: number): string {
  return new Date(unix * 1000).toLocaleString()
}

export function until(unix: number): string {
  const seconds = unix - Math.floor(Date.now() / 1000)
  if (seconds <= 0) return 'matured'

  const days = Math.floor(seconds / 86400)
  if (days > 0) return `in ${days}d`
  const hours = Math.floor(seconds / 3600)
  if (hours > 0) return `in ${hours}h`
  const minutes = Math.floor(seconds / 60)
  if (minutes > 0) return `in ${minutes}m`
  return `in ${seconds}s`
}
