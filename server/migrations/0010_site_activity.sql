-- Persist the latest durably written human activity independently of raw-event
-- retention, so the dashboard can explain an empty realtime window.
CREATE TABLE site_activity (
    site_id       TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
    last_human_at TEXT NOT NULL
);
