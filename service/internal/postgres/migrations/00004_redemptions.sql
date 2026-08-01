-- +goose Up

-- An early close through leaf 2: both parties agree to end the contract at a
-- split they choose, with no oracle and no covenant involved.
--
-- The packets are stored because the two signatures arrive in separate
-- requests, minutes apart, and they accumulate on the same transaction. There
-- is one open proposal per contract at a time — a second one would be a second
-- transaction spending the same VTXO, and only one of them could win.
CREATE TABLE redemptions (
    id          UUID PRIMARY KEY,
    contract_id UUID NOT NULL UNIQUE REFERENCES contracts(id) ON DELETE CASCADE,
    proposed_by UUID NOT NULL REFERENCES users(id),

    short_sats BIGINT NOT NULL,
    long_sats  BIGINT NOT NULL,

    -- The oracle publication the split came from, when it came from one. It is
    -- kept so the close can be audited afterwards against the same bytes the
    -- other party checked it against. Null for a split the two of them simply
    -- agreed on: there is nothing to check it against, and that is the point of
    -- the leaf.
    price     BIGINT,
    message   BYTEA,
    signature BYTEA,

    ark_tx      TEXT NOT NULL,
    -- A JSON array. There is normally one, and the driver here has no native
    -- array support worth pulling a second one in for.
    checkpoints TEXT NOT NULL,

    short_signed BOOLEAN NOT NULL DEFAULT false,
    long_signed  BOOLEAN NOT NULL DEFAULT false,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Evidence is whole or absent.
    CHECK ((price IS NULL) = (signature IS NULL)),
    CHECK ((message IS NULL) = (signature IS NULL))
);

-- +goose Down

DROP TABLE redemptions;
