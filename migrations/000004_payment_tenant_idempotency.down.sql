BEGIN;

ALTER TABLE payments
    DROP CONSTRAINT payments_user_idempotency_key_key,
    ADD CONSTRAINT payments_idempotency_key_key
        UNIQUE (idempotency_key);

COMMIT;
