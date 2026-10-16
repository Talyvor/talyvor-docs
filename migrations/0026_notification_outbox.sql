-- 0026_notification_outbox.sql — B39.16: Docs emails a reviewer when their approval is requested.
-- ADDITIVE ONLY. The same shape as Track's 0033_notification_outbox.sql. Docs owns no workspaces or members
-- table, so workspace_id and member_id are Track's ids as TEXT (workspace_members, 0014).
--
-- One row per email. subject and body are fixed when the event happens, so a replay after a restart sends
-- the same mail. sent_at NULL means not sent yet; the worker claims a row by pushing due_at forward, so a
-- process that dies mid-send leaves it to be sent again once that lease runs out.
CREATE TABLE IF NOT EXISTS notification_outbox (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id TEXT NOT NULL,
    member_id    TEXT NOT NULL,
    kind         TEXT NOT NULL,
    to_email     TEXT NOT NULL,
    subject      TEXT NOT NULL,
    body         TEXT NOT NULL,
    attempts     INT  NOT NULL DEFAULT 0,
    due_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_at      TIMESTAMPTZ,
    error        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_notification_outbox_due
    ON notification_outbox (due_at) WHERE sent_at IS NULL;

-- A member's choice per kind of email. No row means off, except approval.requested, which is on until turned off.
CREATE TABLE IF NOT EXISTS notification_preferences (
    workspace_id TEXT NOT NULL,
    member_id    TEXT NOT NULL,
    kind         TEXT NOT NULL,
    email        BOOLEAN NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (workspace_id, member_id, kind)
);
