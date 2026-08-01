-- +goose Up

-- The split after a unilateral exit, when the covenant is gone and the money is
-- sitting in a 2-of-3 on plain Bitcoin.
--
-- The oracle message is kept with it, not as a convenience: it is what lets the
-- other party check the numbers before signing and anyone audit them
-- afterwards. Without a valid signature the service cannot produce a proposal
-- at all, which is what keeps it from having any discretion here.
CREATE TABLE arbitrations (
    id          UUID PRIMARY KEY,
    contract_id UUID NOT NULL UNIQUE REFERENCES contracts(id) ON DELETE CASCADE,

    short_sats BIGINT NOT NULL,
    long_sats  BIGINT NOT NULL,

    price     BIGINT NOT NULL,
    message   BYTEA  NOT NULL,
    signature BYTEA  NOT NULL,

    raw_tx    TEXT   NOT NULL,
    available BIGINT NOT NULL,

    -- Signatures keyed by the x-only key that made each. A JSON object, because
    -- the sweep takes exactly two of three and which two is not known in
    -- advance.
    signatures TEXT NOT NULL DEFAULT '{}',

    txid       TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Where the exit landed on chain, so a restart can pick the arbitration up
-- from the row alone.
ALTER TABLE exit_packages
    ADD COLUMN sweep_txid TEXT,
    ADD COLUMN sweep_vout INTEGER,
    ADD COLUMN sweep_sats BIGINT,
    ADD CONSTRAINT sweep_outpoint_is_whole
        CHECK ((sweep_txid IS NULL) = (sweep_vout IS NULL));

-- +goose Down

ALTER TABLE exit_packages
    DROP CONSTRAINT sweep_outpoint_is_whole,
    DROP COLUMN sweep_sats,
    DROP COLUMN sweep_vout,
    DROP COLUMN sweep_txid;

DROP TABLE arbitrations;
