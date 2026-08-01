# Running the demo

Two people open a hedge position against each other, the price moves, and the
contract settles itself. Everything below runs against a real arkd and a real
emulator on regtest — nothing is mocked.

## Starting it

```sh
just demo
```

That is the whole thing: an empty chain with arkd and the emulator on it, a
fresh database, alice and bob with 0.5 BTC each, and the oracle, the API and the
web server. It takes a few minutes the first time, mostly waiting for the stack
to come up.

Ctrl-C stops the three processes. `just demo-clean` takes down everything it
made — containers, volumes, the regtest clone — and `just stop` kills whatever
is still listening if a trap did not fire.

Two things it does that are worth knowing about. It starts the chain **on an
empty history**, because a stack left over from a previous run has its block
height wherever a timelock test put it and bitcoind comes back with no wallet
loaded. And it sets **`AUTOMINE_INTERVAL=10`**, so a block is mined every ten
seconds — without that nothing mines and boarding never confirms. The tests want
the opposite, a height that only moves when they ask, which is why it is not the
default.

When it says the demo is ready, open **two browser tabs** on
<http://localhost:5173>.

The demo is two people, and the switcher is per tab: pick one person in each
and both stay signed in at once. One tab is not enough, and one tab switching
back and forth loses the point of watching a contract change under someone who
did not touch it.

## The base flow

### 1. Be somebody, in each tab

The page asks *Who is at this tab?* and alice and bob are already there, with
money — `just demo` boarded them so the first thing you do is open a contract
rather than wait for a faucet.

- **Tab A** → **alice**.
- **Tab B** → **bob**.

Both should read about **49,500,000 sats** spendable. (The half a million that
is missing is the operator's fee for boarding.)

If you want more people, **Top up 0.5 BTC** on the wallet card does the same
thing for whoever is at that tab. It takes a minute — the faucet has to confirm
and a batch has to close — and the balance updates on its own.

### 2. Alice offers a position

In **tab A**:

1. **New position** in the top bar.
2. Leave **short** selected. Alice is hedging: she wants to lock in a dollar
   value, so a crash pays her back.
3. The defaults are the standard position — $10,000 hedged against 0.2 BTC,
   liquidating at $50,000 and $200,000, maturing in 24 hours. Leave them.
4. **Offer the short**.

You land on the contract page. There is no address yet, and that is not a
loading state: the address is a function of *both* payout scripts, and only
one of them exists so far.

### 3. Bob takes the other side

In **tab B**:

1. **Lobby**.
2. Under *On offer* → **Take the long**.

Now there is an address, and each side's stake — 10,000,000 sats each. That is
exactly what the covenant would pay them back at today's price, so a contract
that settled this instant would move nothing.

### 4. Fund it

In **either** tab, on the contract page: **Fund it**.

One Arkade transaction with an input from each party. It takes twenty to forty
seconds and the state goes `funding` → `active`.

Watch the **other** tab while it happens. It is not polling for this — the
server pushes the transition, and the dot in the top right of the contract
header reads *live*.

When it lands, the contract page says **the exit is signed by both parties**.
That happened during funding, before either of them needs it: from here either
one can leave alone after the delay, without the other and without the
operator.

### 5. Crash the price

On the contract page, under *Price*: **Crash to $50,000**.

The preset puts the price exactly on the low boundary rather than near it, so
it always triggers. The bar turns red and says the covenant clamps here.

Look at *What it would pay right now*: the short is up to **19,998,668 sats**
and the long is down to **1,332** — the dust floor. Alice's hedge was paid in
full and Bob took the loss, which is what he signed up for.

### 6. Settle

**Settle**.

The state goes `settling` → `settled` in a few seconds. The emulator runs the
covenant, signs, and forwards to arkd.

Nobody had to authorise this. The settlement leaf carries no party key at all
— which is the point, because a contract that has liquidated must not need the
losing side to cooperate.

### 7. Check the money is really there

Go back to the **Lobby** in each tab and read *Spendable*.

Alice is up 9,998,668 sats and Bob is down the same. Those are spendable VTXOs
in their own Arkade wallets, not a number in our database.

## Doing it again

Run it a second time with the same two people and you will hit this: after a
settlement, the payout VTXOs cannot be spent offchain until they have been back
through a batch. The wallet card says so and offers **Recover**, which takes
about as long as boarding.

It is not a bug in the contract. The operator refuses to spend a VTXO whose
batch has been swept, and there is only one way to un-sweep it.

## Things worth pointing at while demoing

- **The stack card at the bottom of the lobby.** The operator's key, the
  emulator's key, the exit delay, the dust threshold. Not one of them is a
  number we chose — they are read from the two services at startup. That is
  what makes this a real stack rather than a mock.
- **The oracle page.** A separate process that knows about no contract. It
  signs a 24-byte message on a cadence and stores it. The sequence has no gaps
  and cannot have any: settling needs a message *and the one immediately before
  it*, so a number that was never published makes a settlement impossible
  forever.
- **The timeline on the contract page.** Every transition, with what caused it.
  It is also the audit trail: a contract stuck mid-step is diagnosable rather
  than mysterious.
- **`Spike to $200,000` instead.** The mirror image: the long takes 15,000,000
  and the short 5,000,000, because at $200,000 a $10,000 hedge is worth
  5,000,000 sats.

## What this demo does not show

- **Client-side verification.** Everything the address is a function of is in
  the API's response, so a browser could re-derive it and refuse to fund
  anything it did not recognise. It does not yet — the server checks on the
  parties' behalf, which is exactly the trust the design refuses to require.
  That is the TypeScript verifier, and it is not built.
- **Custody.** The service holds both wallets here. It should hold neither: in
  the version that ships each party has their own wallet on their own device,
  and the coordinator holds only the oracle's key and its own third of the
  2-of-3. Nothing above `internal/signer` changes when that happens — every
  signature already goes through a port that never sees a private key.
- **Renewal.** A contract inherits the batch expiry of whatever funded it, so a
  long-lived one has to be swapped into a later batch before its own expires.
  That ceremony is not built.

## If something looks stuck

```sh
just regtest-logs arkd     # or emulator, or bitcoin
```

A contract sitting in `funding` or `settling` is normal for tens of seconds.
If it sits there for ten minutes the worker writes it off — `funding` becomes
`failed`, and `settling` goes back to `active`, because a settlement that
failed changed nothing.
