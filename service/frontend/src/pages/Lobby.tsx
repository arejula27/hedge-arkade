import { useState } from 'react'
import { Link } from 'react-router'
import { api, currentUser, setCurrentUser } from '../api'
import { Button, Empty, Field, Notice, Panel, StateBadge } from '../components/ui'
import { sats, shortHex, until, usd } from '../format'
import { usePoll } from '../hooks'
import type { Contract } from '../types'

export function LobbyPage() {
  const me = currentUser()

  const { value: open, reload: reloadOpen } = usePoll(() => api.contracts('?open=true'))
  const { value: mine, reload: reloadMine } = usePoll(() =>
    me ? api.contracts(`?user=${me}`) : Promise.resolve([]),
  )

  const reload = () => {
    void reloadOpen()
    void reloadMine()
  }

  if (!me) return <WhoAreYou />

  return (
    <>
      <WalletCard />

      <Panel
        title="On offer"
        action={
          <Link to="/contracts/new" className="text-sm text-short hover:underline">
            Open a position
          </Link>
        }
      >
        {open?.length ? (
          <ul className="divide-y divide-edge">
            {open.map((c) => (
              <Offer key={c.id} contract={c} me={me} onAccepted={reload} />
            ))}
          </ul>
        ) : (
          <Empty>Nobody is offering a position. Open one and switch tabs to take it.</Empty>
        )}
      </Panel>

      <Panel title="Mine">
        {mine?.length ? (
          <ul className="divide-y divide-edge">
            {mine.map((c) => (
              <Mine key={c.id} contract={c} me={me} />
            ))}
          </ul>
        ) : (
          <Empty>No positions yet.</Empty>
        )}
      </Panel>

      <StackCard />
    </>
  )
}

function WhoAreYou() {
  const { value: users } = usePoll(() => api.users(), 0)
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)

  const pick = (id: string) => {
    setCurrentUser(id)
    window.location.reload()
  }

  const create = async () => {
    try {
      const user = await api.createUser(name)
      pick(user.id)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  return (
    <Panel title="Who is at this tab?">
      <p className="mb-4 text-sm text-muted">
        The two sides of a contract are two people. Pick one here and the other in a second tab —
        the choice is per tab, so both stay signed in at once.
      </p>

      <div className="flex flex-wrap gap-2">
        {users?.map((u) => (
          <Button key={u.id} onClick={() => pick(u.id)}>
            {u.name}
          </Button>
        ))}
      </div>

      <div className="mt-6 flex gap-2">
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="a new name"
          className="flex-1 rounded-md border border-edge bg-ink px-3 py-1.5 text-sm"
        />
        <Button onClick={create} disabled={!name.trim()}>
          Create
        </Button>
      </div>

      <div className="mt-3">
        <Notice>{error}</Notice>
      </div>
    </Panel>
  )
}

function WalletCard() {
  const { value: wallet, reload } = usePoll(() => api.wallet(), 3000)
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  const run = async (what: string, call: () => Promise<unknown>) => {
    setBusy(what)
    setError(null)
    try {
      await call()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
      void reload()
    }
  }

  return (
    <Panel
      title="Your wallet"
      action={
        <div className="flex gap-2">
          {!!wallet?.recoverable_sats && (
            <Button onClick={() => run('recover', api.recoverWallet)} disabled={busy !== null}>
              {busy === 'recover' ? 'recovering…' : 'Recover'}
            </Button>
          )}
          <Button
            onClick={() => run('fund', () => api.fundWallet(50_000_000))}
            disabled={busy !== null}
          >
            {busy === 'fund' ? 'boarding…' : 'Top up 0.5 BTC'}
          </Button>
        </div>
      }
    >
      <dl className="grid gap-4 sm:grid-cols-3">
        <Field label="Spendable">
          <span className="mono text-lg">{wallet ? sats(wallet.spendable_sats) : '—'}</span>
        </Field>
        <Field label="Arkade address">
          <span className="mono text-muted">{wallet ? shortHex(wallet.offchain_address, 10) : '—'}</span>
        </Field>
        <Field label="Boarding address">
          <span className="mono text-muted">{wallet ? shortHex(wallet.boarding_address, 10) : '—'}</span>
        </Field>
      </dl>

      {!!wallet?.recoverable_sats && (
        <p className="mt-3 text-xs text-warn">
          {sats(wallet.recoverable_sats)} more is yours but cannot be spent offchain: its batch was
          swept, so it has to go back through one. Recovering takes about as long as boarding.
        </p>
      )}

      <p className="mt-3 text-xs text-muted">
        Topping up pays the regtest faucet and then settles the boarding output into a VTXO, which
        takes a minute or so.
      </p>

      <div className="mt-3">
        <Notice>{error}</Notice>
      </div>
    </Panel>
  )
}

function Offer({
  contract,
  me,
  onAccepted,
}: {
  contract: Contract
  me: string
  onAccepted: () => void
}) {
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const mine = contract.short?.id === me || contract.long?.id === me
  const takes = contract.creator === 'short' ? 'long' : 'short'

  const accept = async () => {
    setBusy(true)
    setError(null)
    try {
      await api.accept(contract.id)
      onAccepted()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <li className="flex flex-wrap items-center gap-4 py-3">
      <div className="min-w-48 flex-1">
        <Link to={`/contracts/${contract.id}`} className="text-sm hover:underline">
          {usd(contract.terms.hedge_value_cents)} hedged against{' '}
          <span className="mono">{sats(contract.terms.payout_sats)}</span>
        </Link>
        <div className="text-xs text-muted">
          {usd(contract.terms.low_liquidation_cents)} — {usd(contract.terms.high_liquidation_cents)},
          matures {until(contract.terms.maturity_timestamp)}, offered by{' '}
          {contract.short?.name ?? contract.long?.name}
        </div>
      </div>

      {mine ? (
        <span className="text-xs text-muted">yours — take it from the other tab</span>
      ) : (
        <Button tone="primary" onClick={accept} disabled={busy}>
          Take the {takes}
        </Button>
      )}

      {error && <div className="w-full"><Notice>{error}</Notice></div>}
    </li>
  )
}

function Mine({ contract, me }: { contract: Contract; me: string }) {
  const side = contract.short?.id === me ? 'short' : 'long'
  const stake = side === 'short' ? contract.short_stake : contract.long_stake

  return (
    <li className="flex flex-wrap items-center gap-4 py-3">
      <StateBadge state={contract.state} />
      <Link to={`/contracts/${contract.id}`} className="flex-1 text-sm hover:underline">
        {usd(contract.terms.hedge_value_cents)} · you are the{' '}
        <span className={side === 'short' ? 'text-short' : 'text-long'}>{side}</span>
      </Link>
      <span className="mono text-xs text-muted">{sats(stake)} in</span>
    </li>
  )
}

function StackCard() {
  const { value: stack } = usePoll(() => api.stack(), 0)
  if (!stack) return null

  return (
    <Panel title="The stack underneath">
      <dl className="grid gap-4 text-sm sm:grid-cols-4">
        <Field label="Operator">
          <span className="mono text-muted">{shortHex(stack.arkd_signer)}</span>
        </Field>
        <Field label="Emulator">
          <span className="mono text-muted">{shortHex(stack.emulator_signer)}</span>
        </Field>
        <Field label="Exit delay">
          {stack.exit_delay} {stack.exit_delay_in_blocks ? 'blocks' : 'seconds'}
        </Field>
        <Field label="Dust">
          <span className="mono">{stack.dust}</span>
        </Field>
      </dl>
      <p className="mt-3 text-xs text-muted">
        Read from the operator and the emulator at startup. Not one of these numbers is one we
        chose, which is what makes this a real stack rather than a mock.
      </p>
    </Panel>
  )
}
