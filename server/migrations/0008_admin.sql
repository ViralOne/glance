-- The admin credential, so it can be changed from the dashboard rather than
-- only from the environment, and so an instance is never accidentally public.
--
-- One row, id = 1. The password is a PBKDF2 derivation, never reversible.
-- source records where it came from: 'generated' on first boot (and the
-- operator is told to change it), or 'set' once someone chose it.
CREATE TABLE admin_credential (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    username      TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    source        TEXT NOT NULL DEFAULT 'generated',
    updated_at    TEXT NOT NULL
);
