-- 0024_page_ai_spend_billed_to.sql — B17.211: the Lens workspace a page's AI operation was billed to.
--
-- A Docs workspace is Track's id for it, which Lens does not know: the caller's own Lens workspace
-- pays for the call (the gateway names it — internal/gatewayauth.HeaderWorkspace), so the cost
-- sweep must ask Lens for the request under THAT workspace's token. NULL on a row bound before this
-- column existed, or by work that carries no caller: its own workspace_id was the payer, as before.

ALTER TABLE page_ai_spend_events ADD COLUMN IF NOT EXISTS billed_to TEXT;
