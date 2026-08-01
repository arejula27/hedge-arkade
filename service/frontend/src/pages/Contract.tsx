import { useState } from 'react'
import { useParams } from 'react-router'
import { api, currentUser } from '../api'
import { EarlyClose } from '../components/EarlyClose'
import { PriceBar } from '../components/PriceBar'
import { Split } from '../components/Split'
import { Button, Field, Notice, Panel, StateBadge } from '../components/ui'
import { sats, shortHex, until, usd, when } from '../format'
import { useContractEvents, usePoll } from '../hooks'
import type { Contract } from '../types'

export function ContractPage() {
  const { id } = useParams<{ id: string }>()
  const me = currentUser()

  const { value: contract, error, reload } = usePoll(() => api.contract(id!), 3000)
  const live = useContractEvents(id, reload)

  const [busy, setBusy] = useState<string | null>(null)
  const [failure, setFailure] = useState<string | null>(null)

  const run = async (what: string, call: () => Promise<unknown>) => {
    setBusy(what)
    setFailure(null)
    try {
      await call()
    } catch (err) {
      setFailure(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
      void reload()
    }
  }

  if (error) return <Notice>{error}</Notice>
  if (!contract) return <p className="text-sm text-muted">Loading…</p>

  const side = contract.short?.id === me ? 'short' : contract.long?.id === me ? 'long' : null

  return (
    <>
      <Panel>
        <div className="flex flex-wrap items-center gap-3">
          <StateBadge state={contract.state} />
          <h1 className="text-lg">
            {usd(contract.terms.hedge_value_cents)} hedged against{' '}
            <span className="mono">{sats(contract.terms.payout_sats)}</span>
          </h1>
          <span className="ml-auto flex items-center gap-1.5 text-xs text-muted">
            <span className={`h-1.5 w-1.5 rounded-full ${live ? 'bg-short' : 'bg-muted'}`} />
            {live ? 'live' : 'polling'}
          </span>
        </div>

        {side && (
          <p className="mt-2 text-sm text-muted">
            You are the{' '}
            <span className={side === 'short' ? 'text-short' : 'text-long'}>{side}</span>.
          </p>
        )}

        <div className="mt-6">
          <Actions contract={contract} me={me} busy={busy} run={run} />
        </div>

        <div className="mt-4">
          <Notice>{failure}</Notice>
        </div>
      </Panel>

      <Panel title="Price">
        <PriceBar
          price={contract.projection?.price ?? 0}
          low={contract.terms.low_liquidation_cents}
          high={contract.terms.high_liquidation_cents}
        />
        <div className="mt-4">
          <Presets contract={contract} onMoved={reload} />
        </div>
      </Panel>

      {contract.projection && (
        <Panel title={contract.state === 'settled' ? 'Paid' : 'What it would pay right now'}>
          <Split
            short={contract.projection.short_sats}
            long={contract.projection.long_sats}
            shortName={contract.short?.name}
            longName={contract.long?.name}
            shortStake={contract.short_stake}
            longStake={contract.long_stake}
          />
        </Panel>
      )}

      <EarlyClose contract={contract} me={me} onChanged={reload} />

      <Panel title="The contract">
        <Details contract={contract} />
      </Panel>

      <Panel title="What happened">
        <Timeline contract={contract} />
      </Panel>
    </>
  )
}

function Actions({
  contract,
  me,
  busy,
  run,
}: {
  contract: Contract
  me: string | null
  busy: string | null
  run: (what: string, call: () => Promise<unknown>) => Promise<void>
}) {
  const party = contract.short?.id === me || contract.long?.id === me
  const canAccept = contract.state === 'proposed' && me !== null && !party

  return (
    <div className="flex flex-wrap items-center gap-3">
      {canAccept && (
        <Button
          tone="primary"
          disabled={busy !== null}
          onClick={() => run('accept', () => api.accept(contract.id))}
        >
          Take the {contract.creator === 'short' ? 'long' : 'short'}
        </Button>
      )}

      {contract.state === 'accepted' && party && (
        <Button
          tone="primary"
          disabled={busy !== null}
          onClick={() => run('fund', () => api.fund(contract.id))}
        >
          {busy === 'fund' ? 'funding…' : 'Fund it'}
        </Button>
      )}

      {(contract.state === 'proposed' || contract.state === 'accepted') && party && (
        <Button
          tone="danger"
          disabled={busy !== null}
          onClick={() => run('cancel', () => api.cancel(contract.id))}
        >
          Cancel
        </Button>
      )}

      {contract.state === 'active' && (
        <Button disabled={busy !== null} onClick={() => run('settle', () => api.settle(contract.id))}>
          {busy === 'settle' ? 'settling…' : 'Settle'}
        </Button>
      )}

      {contract.state === 'active' && (
        <span className="text-xs text-muted">
          Settling needs nobody's permission — the settlement leaf carries no party key, so a
          contract that has liquidated cannot need the losing side to cooperate.
        </span>
      )}

      {(contract.state === 'funding' ||
        contract.state === 'settling' ||
        contract.state === 'redeeming') && (
        <span className="text-xs text-muted">
          Working. This takes tens of seconds against a live stack, and it survives a restart.
        </span>
      )}
    </div>
  )
}

// Presets put the price exactly on a boundary, so a click always triggers what
// it says it will rather than landing a cent short.
function Presets({ contract, onMoved }: { contract: Contract; onMoved: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const move = async (price: number) => {
    setBusy(true)
    setError(null)
    try {
      await api.setPrice(price)
      onMoved()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const low = contract.terms.low_liquidation_cents
  const high = contract.terms.high_liquidation_cents
  const opened = Math.round((low + high) / 2)

  return (
    <div>
      <div className="flex flex-wrap gap-2">
        <Button disabled={busy} onClick={() => move(low)}>
          Crash to {usd(low)}
        </Button>
        <Button disabled={busy} onClick={() => move(opened)}>
          Back to the middle
        </Button>
        <Button disabled={busy} onClick={() => move(high)}>
          Spike to {usd(high)}
        </Button>
      </div>
      <p className="mt-2 text-xs text-muted">
        These publish a signed price from the oracle, which is a separate process that knows nothing
        about this contract.
      </p>
      <div className="mt-2">
        <Notice>{error}</Notice>
      </div>
    </div>
  )
}

function Details({ contract }: { contract: Contract }) {
  return (
    <>
      <dl className="grid gap-4 text-sm sm:grid-cols-2">
        <Field label="Address">
          <span className="mono break-all text-muted">{contract.address ?? 'not until both sides are known'}</span>
        </Field>
        <Field label="Funding">
          <span className="mono text-muted">
            {contract.funding ? `${shortHex(contract.funding.txid)}:${contract.funding.vout}` : '—'}
          </span>
        </Field>
        <Field label="Liquidates at">
          {usd(contract.terms.low_liquidation_cents)} / {usd(contract.terms.high_liquidation_cents)}
        </Field>
        <Field label="Matures">
          {when(contract.terms.maturity_timestamp)} ({until(contract.terms.maturity_timestamp)})
        </Field>
        <Field label="Oracle">
          <span className="mono text-muted">{shortHex(contract.terms.oracle_pubkey)}</span>
        </Field>
        <Field label="Exit delay">
          {contract.terms.exit_delay} {contract.terms.exit_delay_in_blocks ? 'blocks' : 'seconds'}
        </Field>
      </dl>

      <div className="mt-4 rounded-md border border-edge bg-ink/60 p-3 text-xs text-muted">
        {contract.exit_ready ? (
          <>
            <span className="text-short">The exit is signed by both parties.</span> From here either
            of them can leave alone after the delay, without the other and without the operator.
          </>
        ) : (
          <>The unilateral exit is signed at funding, before either party needs it.</>
        )}
      </div>

      <p className="mt-3 text-xs text-muted">
        Everything the address is a function of is in this response, so a client could re-derive it
        rather than take our word for it. Doing that in the browser is the verifier, and it is not
        built yet — for now the server checks on the parties' behalf, which is exactly the trust the
        design refuses to require.
      </p>
    </>
  )
}

function Timeline({ contract }: { contract: Contract }) {
  if (!contract.events?.length) return <p className="text-sm text-muted">Nothing yet.</p>

  return (
    <ol className="space-y-3">
      {contract.events.map((e, i) => (
        <li key={i} className="flex gap-3 text-sm">
          <span className="mt-1 h-1.5 w-1.5 shrink-0 rounded-full bg-edge" />
          <div>
            <span className="text-slate-300">
              {e.from ? `${e.from} → ${e.to}` : e.to}
            </span>
            {e.detail && <div className="text-xs text-muted">{e.detail}</div>}
          </div>
        </li>
      ))}
    </ol>
  )
}
