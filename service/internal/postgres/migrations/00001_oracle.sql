-- +goose Up

-- Every price the oracle has ever published, kept forever.
--
-- History is an input, not an archive: the covenant settles on a message and
-- its immediate predecessor, so a publication that is dropped takes a
-- settlement with it.
--
-- sequence is written by the application under an advisory lock, not by a
-- BIGSERIAL. A Postgres sequence is monotonic but not dense, and a number burnt
-- by a rollback can never be published — which makes every settlement that
-- would have needed it as a predecessor impossible.
CREATE TABLE oracle_publications (
    sequence   BIGINT      PRIMARY KEY,
    ts         BIGINT      NOT NULL,
    price      BIGINT      NOT NULL CHECK (price > 0),
    message    BYTEA       NOT NULL,
    signature  BYTEA       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down

DROP TABLE oracle_publications;
