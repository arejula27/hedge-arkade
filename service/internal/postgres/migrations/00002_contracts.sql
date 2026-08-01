-- +goose Up

CREATE TABLE users (
    id         UUID        PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE,
    public_key BYTEA       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The demo holds each user's wallet seed.
--
-- This table is the demo's whole custody story, and it is the one thing here
-- that has no place in the real service: the coordinator never holds a party's
-- key. When wallets move out to the user's own device this table goes with
-- them, and nothing above it changes — every signature already goes through a
-- port.
CREATE TABLE wallets (
    user_id    UUID        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    seed       BYTEA       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE contracts (
    id    UUID PRIMARY KEY,
    state TEXT NOT NULL CHECK (state IN (
        'proposed', 'accepted', 'funding', 'active',
        'settling', 'settled',
        'redemption_proposed', 'redeeming', 'redeemed',
        'exiting', 'exited', 'arbitrating', 'arbitrated',
        'cancelled', 'failed'
    )),

    -- One of the two is null until someone accepts. creator says which side
    -- the proposer took, so the other is the one on offer.
    creator       TEXT NOT NULL CHECK (creator IN ('short', 'long')),
    short_user_id UUID REFERENCES users(id),
    long_user_id  UUID REFERENCES users(id),

    -- The covenant's constructor parameters, as columns rather than a blob, so
    -- the address can be recomputed from the row and compared with pk_script.
    nominal_units     BIGINT NOT NULL,
    leverage_sats     BIGINT NOT NULL,
    payout_sats       BIGINT NOT NULL,
    low_liquidation   BIGINT NOT NULL,
    high_liquidation  BIGINT NOT NULL,
    -- Null while the contract is on offer: only the creator's payout script is
    -- known, and the other arrives with whoever accepts.
    short_lock_script BYTEA,
    long_lock_script  BYTEA,
    oracle_pubkey     BYTEA  NOT NULL,
    start_ts          BIGINT NOT NULL,
    maturity_ts       BIGINT NOT NULL,

    -- The four keys the taproot tree is built from. The operator's and the
    -- emulator's are stored rather than re-read from GetInfo: they are baked
    -- into the address, so a rotated key must not silently move a funded
    -- contract. The party keys are null until both parties exist.
    short_key       BYTEA,
    long_key        BYTEA,
    arkd_signer     BYTEA NOT NULL,
    emulator_signer BYTEA NOT NULL,

    exit_delay_value         INTEGER NOT NULL,
    exit_delay_blocks        BOOLEAN NOT NULL,
    enable_mutual_redemption BOOLEAN NOT NULL,

    -- The address is a function of both payout scripts, so it does not exist
    -- until both sides do.
    pk_script BYTEA,

    -- What each side puts in, fixed at the opening price. They sum to
    -- payout_sats, which is what the covenant pins the input to.
    short_stake BIGINT NOT NULL,
    long_stake  BIGINT NOT NULL,

    funding_txid TEXT,
    funding_vout INTEGER,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A contract with a side but no user, or a funding vout with no txid, is a
    -- row no code should have to defend against.
    CHECK ((funding_txid IS NULL) = (funding_vout IS NULL)),
    CHECK (short_stake + long_stake = payout_sats)
);

-- The lists the UI asks for: what is on offer, and what is mine.
CREATE INDEX contracts_state_idx ON contracts (state);
CREATE INDEX contracts_short_user_idx ON contracts (short_user_id);
CREATE INDEX contracts_long_user_idx ON contracts (long_user_id);

-- Every transition a contract has made. It is the audit log and the UI's
-- timeline, and it is what makes a contract stuck in a transient state
-- diagnosable rather than mysterious.
CREATE TABLE contract_events (
    id          BIGSERIAL   PRIMARY KEY,
    contract_id UUID        NOT NULL REFERENCES contracts(id) ON DELETE CASCADE,
    at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    from_state  TEXT        NOT NULL,
    to_state    TEXT        NOT NULL,
    detail      TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX contract_events_contract_idx ON contract_events (contract_id, id);

-- +goose Down

DROP TABLE contract_events;
DROP TABLE contracts;
DROP TABLE wallets;
DROP TABLE users;
