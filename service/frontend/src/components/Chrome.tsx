import { NavLink, Outlet } from 'react-router'
import { api, currentUser, setCurrentUser } from '../api'
import { sats, usd } from '../format'
import { usePoll } from '../hooks'
import type { User } from '../types'

// Chrome is the frame every page sits in: who you are, what the price is, and
// the fact that the stack underneath is real.
export function Chrome() {
  const { value: users } = usePoll<User[]>(() => api.users(), 10_000)
  const { value: price } = usePoll(() => api.price(), 2000)
  const { value: wallet } = usePoll(() => (currentUser() ? api.wallet() : Promise.resolve(null)), 3000)

  const me = currentUser()

  return (
    <div className="min-h-screen">
      <header className="sticky top-0 z-10 border-b border-edge bg-ink/90 backdrop-blur">
        <div className="mx-auto flex max-w-5xl flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3">
          <NavLink to="/" className="text-sm font-semibold tracking-tight">
            Arkade Hedge
          </NavLink>

          <nav className="flex gap-4 text-sm text-muted">
            <Tab to="/">Lobby</Tab>
            <Tab to="/contracts/new">New position</Tab>
            <Tab to="/oracle">Oracle</Tab>
          </nav>

          <div className="ml-auto flex items-center gap-4 text-sm">
            {price && (
              <span className="mono text-slate-300" title={`sequence ${price.sequence}`}>
                {usd(price.price)}
              </span>
            )}
            {wallet && <span className="mono text-muted">{sats(wallet.spendable_sats)}</span>}

            <label className="flex items-center gap-2">
              <span className="text-muted">You are</span>
              <select
                value={me ?? ''}
                onChange={(e) => {
                  setCurrentUser(e.target.value || null)
                  window.location.reload()
                }}
                className="rounded border border-edge bg-panel px-2 py-1"
              >
                <option value="">nobody</option>
                {users?.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.name}
                  </option>
                ))}
              </select>
            </label>
          </div>
        </div>

        {/* This is why the demo is two tabs and not one screen split in half. */}
        <div className="border-t border-edge/60 bg-panel/40 px-4 py-1 text-center text-xs text-muted">
          Open a second tab and pick the other person there — the switcher is per tab.
        </div>
      </header>

      <main className="mx-auto max-w-5xl space-y-6 px-4 py-8">
        <Outlet />
      </main>
    </div>
  )
}

function Tab({ to, children }: { to: string; children: string }) {
  return (
    <NavLink
      to={to}
      end={to === '/'}
      className={({ isActive }) =>
        isActive ? 'text-slate-100' : 'text-muted transition-colors hover:text-slate-300'
      }
    >
      {children}
    </NavLink>
  )
}
