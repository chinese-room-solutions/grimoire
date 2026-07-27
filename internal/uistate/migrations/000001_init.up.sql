-- ui_state is a small key/value table for the workspace UI state Grimoire
-- restores on reopen (open tabs, the focused tab, scroll positions). One row per
-- key; the value is an opaque JSON blob owned by the frontend.
CREATE TABLE ui_state (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
