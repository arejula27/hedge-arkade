import type { ReactNode } from 'react'
import type { State } from '../types'

export function Panel({
  title,
  action,
  children,
}: {
  title?: string
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <section className="rounded-lg border border-edge bg-panel">
      {title && (
        <header className="flex items-center justify-between border-b border-edge px-4 py-3">
          <h2 className="text-sm font-medium text-slate-300">{title}</h2>
          {action}
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  )
}

export function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-muted">{label}</dt>
      <dd className="mt-0.5 text-slate-200">{children}</dd>
    </div>
  )
}

// How a state reads at a glance: what is waiting on someone, what is in flight,
// and what is over.
const stateStyles: Record<State, string> = {
  proposed: 'bg-amber-500/10 text-warn border-amber-500/30',
  accepted: 'bg-amber-500/10 text-warn border-amber-500/30',
  funding: 'bg-sky-500/10 text-sky-300 border-sky-500/30 animate-pulse',
  active: 'bg-emerald-500/10 text-short border-emerald-500/30',
  settling: 'bg-sky-500/10 text-sky-300 border-sky-500/30 animate-pulse',
  settled: 'bg-slate-500/10 text-slate-300 border-slate-500/30',
  redemption_proposed: 'bg-amber-500/10 text-warn border-amber-500/30',
  redeeming: 'bg-sky-500/10 text-sky-300 border-sky-500/30 animate-pulse',
  redeemed: 'bg-slate-500/10 text-slate-300 border-slate-500/30',
  exiting: 'bg-sky-500/10 text-sky-300 border-sky-500/30 animate-pulse',
  exited: 'bg-amber-500/10 text-warn border-amber-500/30',
  arbitrating: 'bg-sky-500/10 text-sky-300 border-sky-500/30 animate-pulse',
  arbitrated: 'bg-slate-500/10 text-slate-300 border-slate-500/30',
  cancelled: 'bg-slate-500/10 text-slate-400 border-slate-500/30',
  failed: 'bg-red-500/10 text-bad border-red-500/30',
}

export function StateBadge({ state }: { state: State }) {
  return (
    <span
      className={`inline-block rounded border px-2 py-0.5 text-xs font-medium ${stateStyles[state] ?? stateStyles.proposed}`}
    >
      {state.replace(/_/g, ' ')}
    </span>
  )
}

export function Button({
  children,
  onClick,
  disabled,
  tone = 'default',
  type = 'button',
}: {
  children: ReactNode
  onClick?: () => void
  disabled?: boolean
  tone?: 'default' | 'primary' | 'danger'
  type?: 'button' | 'submit'
}) {
  const tones = {
    default: 'border-edge bg-panel hover:bg-edge text-slate-200',
    primary: 'border-emerald-500/40 bg-emerald-500/15 hover:bg-emerald-500/25 text-short',
    danger: 'border-red-500/40 bg-red-500/10 hover:bg-red-500/20 text-bad',
  }

  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled}
      className={`rounded-md border px-3 py-1.5 text-sm transition-colors disabled:cursor-not-allowed disabled:opacity-40 ${tones[tone]}`}
    >
      {children}
    </button>
  )
}

export function Notice({ children, tone = 'bad' }: { children: ReactNode; tone?: 'bad' | 'muted' }) {
  if (!children) return null

  const tones = {
    bad: 'border-red-500/30 bg-red-500/10 text-bad',
    muted: 'border-edge bg-panel text-muted',
  }
  return (
    <div className={`rounded-md border px-3 py-2 text-sm ${tones[tone]}`}>{children}</div>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="py-6 text-center text-sm text-muted">{children}</p>
}
