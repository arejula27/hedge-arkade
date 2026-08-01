// The shapes the API answers with.

export type User = {
  id: string
  name: string
  pubkey: string
}

export type Wallet = {
  offchain_address: string
  boarding_address: string
  spendable_sats: number
  // Money the wallet holds and cannot spend offchain until it has been back
  // through a batch, because its own batch was swept.
  recoverable_sats: number
}

export type Party = {
  id: string
  name: string
}

// Everything the taproot address is a function of. A client is supposed to
// recognise the contract it funds rather than take the service's word for it,
// which is why these travel structured and not only as an address.
export type Terms = {
  hedge_value_cents: number
  payout_sats: number
  low_liquidation_cents: number
  high_liquidation_cents: number
  start_timestamp: number
  maturity_timestamp: number

  oracle_pubkey: string
  short_lock_script?: string
  long_lock_script?: string

  short_key?: string
  long_key?: string
  arkd_signer: string
  emulator_signer: string

  exit_delay: number
  exit_delay_in_blocks: boolean
  enable_mutual_redemption: boolean
}

export type Outpoint = {
  txid: string
  vout: number
}

// What the contract would pay right now, straight from the covenant's formula.
export type Projection = {
  price: number
  short_sats: number
  long_sats: number
  liquidated: boolean
  matured: boolean
}

export type ContractEvent = {
  from: string
  to: string
  detail: string
}

export type State =
  | 'proposed'
  | 'accepted'
  | 'funding'
  | 'active'
  | 'settling'
  | 'settled'
  | 'redemption_proposed'
  | 'redeeming'
  | 'redeemed'
  | 'exiting'
  | 'exited'
  | 'arbitrating'
  | 'arbitrated'
  | 'cancelled'
  | 'failed'

// An early close through leaf 2. The evidence is there when the split came from
// an oracle price, so the other party can check the numbers against the same
// bytes rather than against a promise.
export type Redemption = {
  id: string
  proposed_by: string
  short_sats: number
  long_sats: number
  price?: number
  message?: string
  signature?: string
  short_signed: boolean
  long_signed: boolean
}

// The split after a unilateral exit, when the covenant is gone and the money is
// in a 2-of-3 on plain Bitcoin.
export type Arbitration = {
  id: string
  short_sats: number
  long_sats: number
  price: number
  message: string
  signature: string
  available: number
  signatures: number
  signed: boolean
  txid?: string
}

export type Contract = {
  id: string
  state: State
  creator: 'short' | 'long'

  address?: string
  pk_script?: string

  short: Party | null
  long: Party | null

  terms: Terms

  short_stake: number
  long_stake: number

  funding?: Outpoint
  exit_ready: boolean

  projection?: Projection
  redemption?: Redemption
  arbitration?: Arbitration
  events?: ContractEvent[]
}

// Price is in cents per BTC: 10_000_000 is $100,000.
export type Price = {
  sequence: number
  timestamp: number
  price: number
}

// What the operator and the emulator said about themselves. None of it is a
// number we chose, which is the point of showing it.
export type Stack = {
  arkd_signer: string
  emulator_signer: string
  exit_delay: number
  exit_delay_in_blocks: boolean
  dust: number
}

export type StreamEvent = {
  contract: string
  state: State
  detail: string
}
