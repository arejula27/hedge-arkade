-- +goose Up

-- The exit both parties sign at funding, before either of them needs it.
--
-- raw_tx is the unsigned transaction. Both parties derive the same bytes
-- independently from the contract and the outpoint, so this row is a
-- convenience rather than something anyone has to trust: what matters is that
-- the two signatures exist and cover it.
--
-- The sweep is the 2-of-3 the exit pays into, on plain Bitcoin. Its leaf and
-- control block are stored because spending it later needs them and nothing
-- else in the system knows them.
CREATE TABLE exit_packages (
    contract_id   UUID PRIMARY KEY REFERENCES contracts(id) ON DELETE CASCADE,
    raw_tx        BYTEA  NOT NULL,
    amount        BIGINT NOT NULL,
    sweep_pkscript BYTEA NOT NULL,
    sweep_leaf    BYTEA  NOT NULL,
    sweep_control BYTEA  NOT NULL,
    short_sig     BYTEA  NOT NULL,
    long_sig      BYTEA  NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A package with one signature is one nobody can use. It is written whole
    -- or not at all.
    CHECK (length(short_sig) > 0 AND length(long_sig) > 0)
);

-- +goose Down

DROP TABLE exit_packages;
