import { useState } from 'react'
import { api } from '../api'
import { sats, shortHex, usd } from '../format'
import type { Contract } from '../types'
import { Split } from './Split'
import { Button, Field, Notice, Panel } from './ui'

// Exit is the path that assumes nothing about the operator.
//
// Everything else on this page needs it to answer. This does not: the whole
// chain of transactions goes onto Bitcoin, and then the exit both parties
// signed at funding — before either of them needed it — sweeps the money into a
// 2-of-3 the operator has no key to.
export function Exit({
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

  if (contract.state === 'active' && side && !contract.redemption) {
    return (
      <Panel title="Leave without the operator">
        <p className="text-sm text-muted">
          If the operator stops answering, neither of you is stuck. The contract's whole chain of
          transactions goes onto Bitcoin, and then the exit you both signed at funding sweeps the
          money into a 2-of-3 between the two of you and this service.
        </p>
        <p className="mt-2 text-xs text-muted">
          This takes minutes — one block per transaction in the chain, then a relative timelock of{' '}
          {contract.terms.exit_delay}{' '}
          {contract.terms.exit_delay_in_blocks ? 'blocks' : 'seconds'} to wait out.
        </p>

        <div className="mt-4">
          <Button
            tone="danger"
            disabled={busy !== null || !contract.exit_ready}
            onClick={() => run('exit', () => api.exit(contract.id))}
          >
            {busy === 'exit' ? 'leaving…' : 'Exit unilaterally'}
          </Button>
        </div>

        <div className="mt-3">
          <Notice>{error}</Notice>
        </div>
      </Panel>
    )
  }

  if (contract.state === 'exiting') {
    return (
      <Panel title="Leaving Arkade">
        <p className="text-sm text-muted">
          Unrolling the chain onto Bitcoin, one transaction per block, and then waiting out the
          timelock. Minutes, and it survives a restart.
        </p>
      </Panel>
    )
  }

  if (contract.state !== 'exited' && !contract.arbitration) return null

  const proposal = contract.arbitration

  if (!proposal) {
    return (
      <Panel title="Out of Arkade">
        <p className="text-sm text-muted">
          The money is on Bitcoin now, in a 2-of-3 between the two of you and this service. The
          covenant is gone with it, so the split has to come from somewhere else.
        </p>
        <p className="mt-2 text-xs text-muted">
          The service works it out from an oracle-signed price. It has no discretion in that —
          without a valid signature it cannot produce a proposal at all — and it cannot move the
          money either, because two of the three keys are needed and it holds one.
        </p>

        <div className="mt-4">
          <Button
            disabled={busy !== null}
            onClick={() => run('arbitrate', () => api.arbitrate(contract.id))}
          >
            {busy === 'arbitrate' ? 'working it out…' : 'Ask the service to arbitrate'}
          </Button>
        </div>

        <div className="mt-3">
          <Notice>{error}</Notice>
        </div>
      </Panel>
    )
  }

  return (
    <Panel title={proposal.txid ? 'Paid on chain' : 'The service has proposed a split'}>
      <Split
        short={proposal.short_sats}
        long={proposal.long_sats}
        shortName={contract.short?.name}
        longName={contract.long?.name}
        shortStake={contract.short_stake}
        longStake={contract.long_stake}
      />

      <dl className="mt-4 grid gap-4 text-sm sm:grid-cols-3">
        <Field label="At">{usd(proposal.price)}</Field>
        <Field label="In the sweep">
          <span className="mono">{sats(proposal.available)}</span>
        </Field>
        <Field label="Signatures">
          {proposal.signatures} of the 2 the sweep needs
        </Field>
      </dl>

      <div className="mt-4 rounded-md border border-edge bg-ink/60 p-3 text-xs text-muted">
        <p>The oracle's signed message this came from:</p>
        <p className="mono mt-1 break-all">{shortHex(proposal.message, 12)}</p>
        <p className="mt-2">
          The service holds one of the three keys and cannot spend alone. Whichever of you signs
          next makes two, and the money moves. Before your signature is added the numbers are
          rebuilt from those bytes and compared — the service decided them, and taking that on
          trust is the thing this design refuses to require.
        </p>
      </div>

      {proposal.txid ? (
        <p className="mt-4 text-sm">
          <span className="text-short">Paid.</span>{' '}
          <span className="mono text-muted">{shortHex(proposal.txid)}</span>
        </p>
      ) : (
        side && (
          <div className="mt-4">
            <Button
              tone="primary"
              disabled={busy !== null}
              onClick={() => run('sign', () => api.signArbitration(contract.id))}
            >
              {busy === 'sign' ? 'signing…' : 'Check it and sign'}
            </Button>
          </div>
        )
      )}

      <div className="mt-3">
        <Notice>{error}</Notice>
      </div>
    </Panel>
  )
}
