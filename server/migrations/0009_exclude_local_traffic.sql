-- Let each site ignore development traffic before it reaches raw events or
-- permanent rollups. Disabled by default to preserve existing behaviour.
ALTER TABLE sites ADD COLUMN exclude_local_traffic INTEGER NOT NULL DEFAULT 0;
