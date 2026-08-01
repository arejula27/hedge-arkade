import { useState } from 'react'
import { api } from '../api'
import { sats, shortHex, usd } from '../format'
import type { Contract } from '../types'
import { Split } from './Split'
import { Button, Notice, Panel } from './ui'

// EarlyClose is leaf 2: both parties agree to end the contract at a split they
// choose, with no oracle and no covenant involved.
//
// It is the only leaf whose authority is the two signatures alone, which is why
// the panel is mostly about who has signed and what they are signing.
export function EarlyClose({
  contract,
  me,
  onChanged,
}: {
  contract: Contract
  me: string | null
  onChanged: () => void
}) {
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  const side = contract.short?.id === me ? 'short' : contract.long?.id === me ? 'long' : null

  const run = async (what: string, call: () => Promise<unknown>) => {
    setBusy(what)
    setError(null)
    try {
      await call()
      onChanged()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }

  const proposal = contract.redemption

  if (!proposal) {
    if (contract.state !== 'active' || !side) return null

    return (
      <Panel title="Close it early">
        <p className="text-sm text-muted">
          Both of you agree to end it now, at the price the oracle is publishing. No covenant runs
          — leaf 2 asks for two signatures and nothing else, so the split is whatever the two of
          you say it is.
        </p>

        <div className="mt-4">
          <Button
            disabled={busy !== null}
            onClick={() => run('propose', () => api.proposeRedemption(contract.id))}
          >
            {busy === 'propose' ? 'proposing…' : 'Propose closing at the current price'}
          </Button>
        </div>

        <div className="mt-3">
          <Notice>{error}</Notice>
        </div>
      </Panel>
    )
  }

  const mySignature = side === 'short' ? proposal.short_signed : proposal.long_signed
  const both = proposal.short_signed && proposal.long_signed

  return (
    <Panel title="An early close is on the table">
      <Split
        short={proposal.short_sats}
        long={proposal.long_sats}
        shortName={contract.short?.name}
        longName={contract.long?.name}
        shortStake={contract.short_stake}
        longStake={contract.long_stake}
      />

      {proposal.price ? (
        <div className="mt-4 rounded-md border border-edge bg-ink/60 p-3 text-xs text-muted">
          <p>
            Proposed at <span className="text-slate-200">{usd(proposal.price)}</span>, and the
            oracle's signed message says so:
          </p>
          <p className="mono mt-1 break-all">{shortHex(proposal.message ?? '', 12)}</p>
          <p className="mt-2">
            The numbers were re-derived from those bytes before your signature was added. Signing
            without that check is trusting whoever proposed, which is the thing this design refuses
            to require — though for now it is the server doing the checking on your behalf, not
            your own wallet.
          </p>
        </div>
      ) : (
        <p className="mt-4 text-xs text-muted">
          A split the two of you simply agreed on. There is nothing to check it against, and that
          is the point of the leaf.
        </p>
      )}

      <div className="mt-4 flex flex-wrap items-center gap-3 text-sm">
        <Signature name={contract.short?.name ?? 'short'} signed={proposal.short_signed} />
        <Signature name={contract.long?.name ?? 'long'} signed={proposal.long_signed} />

        {side && !mySignature && !both && (
          <Button
            tone="primary"
            disabled={busy !== null}
            onClick={() => run('sign', () => api.signRedemption(contract.id))}
          >
            {busy === 'sign' ? 'signing…' : `Sign it — ${sats(side === 'short' ? proposal.short_sats : proposal.long_sats)} to you`}
          </Button>
        )}

        {side && !both && (
          <Button
            tone="danger"
            disabled={busy !== null}
            onClick={() => run('reject', () => api.rejectRedemption(contract.id))}
          >
            Reject
          </Button>
        )}

        {both && <span className="text-xs text-muted">Both signed. Submitting to the operator.</span>}
      </div>

      <div className="mt-3">
        <Notice>{error}</Notice>
      </div>
    </Panel>
  )
}

function Signature({ name, signed }: { name: string; signed: boolean }) {
  return (
    <span
      className={`rounded border px-2 py-0.5 text-xs ${
        signed
          ? 'border-emerald-500/30 bg-emerald-500/10 text-short'
          : 'border-edge bg-panel text-muted'
      }`}
    >
      {name} {signed ? 'signed' : 'has not signed'}
    </span>
  )
}
