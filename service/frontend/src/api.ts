import type { Contract, Price, Redemption, Stack, User, Wallet } from './types'

const base = import.meta.env.VITE_API ?? 'http://localhost:8080'

// Identity lives per tab, not per browser.
//
// A cookie is shared by every tab of an origin, and the whole demo is two
// people in two tabs of the same browser — so it goes in a header the tab sets
// for itself.
const identityKey = 'hedge.user'

export function currentUser(): string | null {
  return sessionStorage.getItem(identityKey) ?? localStorage.getItem(identityKey)
}

export function setCurrentUser(id: string | null) {
  if (id === null) {
    sessionStorage.removeItem(identityKey)
    localStorage.removeItem(identityKey)
    return
  }
  // sessionStorage is per tab, which is what makes two tabs two people.
  // localStorage is the fallback so a reload in a fresh tab still knows who it
  // was.
  sessionStorage.setItem(identityKey, id)
  localStorage.setItem(identityKey, id)
}

export class ApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers)
  if (init?.body) headers.set('Content-Type', 'application/json')

  const user = currentUser()
  if (user) headers.set('X-Hedge-User', user)

  const response = await fetch(base + path, { ...init, headers })

  if (!response.ok) {
    let message = response.statusText
    try {
      const body = await response.json()
      if (body?.message) message = body.message
    } catch {
      // A response with no JSON body is still a failure worth reporting.
    }
    throw new ApiError(response.status, message)
  }

  if (response.status === 204 || response.status === 202) return undefined as T
  return response.json() as Promise<T>
}

function post<T>(path: string, body?: unknown): Promise<T> {
  return request<T>(path, {
    method: 'POST',
    body: body === undefined ? undefined : JSON.stringify(body),
  })
}

export const api = {
  users: () => request<User[]>('/api/users'),
  createUser: (name: string) => post<User>('/api/users', { name }),
  me: () => request<User>('/api/me'),

  wallet: () => request<Wallet>('/api/wallet'),
  fundWallet: (sats: number) => post<void>('/api/wallet/fund', { sats }),
  recoverWallet: () => post<void>('/api/wallet/recover'),

  contracts: (query = '') => request<Contract[]>('/api/contracts' + query),
  contract: (id: string) => request<Contract>(`/api/contracts/${id}`),
  propose: (proposal: Proposal) => post<Contract>('/api/contracts', proposal),
  accept: (id: string) => post<Contract>(`/api/contracts/${id}/accept`),
  cancel: (id: string) => post<Contract>(`/api/contracts/${id}/cancel`),
  fund: (id: string) => post<Contract>(`/api/contracts/${id}/fund`),
  settle: (id: string) => post<Contract>(`/api/contracts/${id}/settle`),

  proposeRedemption: (id: string, split?: { short_sats: number; long_sats: number }) =>
    post<Redemption>(`/api/contracts/${id}/redemption`, split ?? {}),
  signRedemption: (id: string) => post<Redemption>(`/api/contracts/${id}/redemption/sign`),
  rejectRedemption: (id: string) => post<Contract>(`/api/contracts/${id}/redemption/reject`),

  price: () => request<Price>('/api/oracle'),
  priceHistory: (limit = 60) => request<Price[]>(`/api/oracle/history?limit=${limit}`),
  setPrice: (price: number) => post<void>('/api/oracle/price', { price }),

  stack: () => request<Stack>('/api/demo/stack'),

  eventsURL: (contract?: string) =>
    base + '/api/events' + (contract ? `?contract=${contract}` : ''),
}

export type Proposal = {
  side: 'short' | 'long'
  hedge_value_cents: number
  payout_sats: number
  low_liquidation_cents: number
  high_liquidation_cents: number
  maturity_in_seconds: number
  enable_mutual_redemption: boolean
}
