-- =============================================================================
-- SEED: bank_accounts + bank_transactions  (DEV DATA)
-- One dummy bank account and a handful of transactions for the DEV org
-- (00000000-0000-0000-0000-000000000001), so the banking data model can be
-- eyeballed and the derived-balance query exercised against real rows.
--
-- Modelled on the "Business Current Account" in the FreeAgent screenshots
-- (NatWest, sort code 601441, account 66686210).
--
-- Unlike vat_rates, this is NOT global reference data — it is throwaway dev data
-- under the dev org only. Fixed UUIDs + ON CONFLICT (id) DO NOTHING make it
-- idempotent (re-running changes nothing). created_by_user_id is resolved by
-- subquery on the seeded dev login (dev@example.com), so no user UUID is hardcoded.
--
-- amount_minor is SIGNED minor units (pence): POSITIVE = money in, NEGATIVE out.
-- The account's DERIVED balance = opening_balance_minor + SUM(amount_minor):
--   410942  (opening, £4,109.42)
--   +1641359 -1008 -2296 -5500 -900 -2434 -154980   (the 7 lines below)
--   = 1885183  →  £18,851.83   (what ListBankAccounts.current_balance_minor returns)
--
-- Apply with:
--   psql "$DATABASE_URL" -f db/seeds/bank_accounts.sql
-- =============================================================================

-- The account ----------------------------------------------------------------
INSERT INTO bank_accounts (
    id, organisation_id, created_by_user_id,
    name, currency, status, is_personal, is_primary,
    bank_name, account_number, sort_code,
    show_on_invoices, opening_balance_minor, opening_balance_date, guess_explanations
) VALUES (
    '20000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001',
    (SELECT id FROM users WHERE email = 'dev@example.com'),
    'Business Current Account', 'GBP', 'active', FALSE, TRUE,
    'NatWest', '66686210', '601441',
    TRUE, 410942, '2026-03-13', TRUE
)
ON CONFLICT (id) DO NOTHING;

-- Its transactions -----------------------------------------------------------
-- A realistic mix that exercises every interesting column: money in AND out,
-- feed vs manual source, with/without external_id + balance_minor, and a spread
-- of status. created_by_user_id is the dev user for MANUAL lines, NULL for FEED
-- lines (a feed has no human author).
INSERT INTO bank_transactions (
    id, organisation_id, bank_account_id, created_by_user_id,
    dated_on, amount_minor, description, bank_memo, balance_minor, status, source, external_id
) VALUES
  -- Feed lines (no author; carry the bank's external id + running balance)
  ('20000000-0000-0000-0000-000000000101', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000001',
   NULL, '2026-06-09', -1008, 'Ubr* Pending.uber.com', 'Ubr* Pending.uber.com//OTHER/£10.08', 409934, 'unexplained', 'feed', 'uber-2026-06-09-001'),
  ('20000000-0000-0000-0000-000000000102', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000001',
   NULL, '2026-06-09', -2296, 'Ubr* Pending.uber.com', 'Ubr* Pending.uber.com//OTHER/£22.96', 407638, 'unexplained', 'feed', 'uber-2026-06-09-002'),
  ('20000000-0000-0000-0000-000000000104', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000001',
   NULL, '2026-06-13', -5500, 'L B Of Merton', 'L B Of Merton//OTHER/£55.00', NULL, 'for_approval', 'feed', 'merton-2026-06-13'),
  ('20000000-0000-0000-0000-000000000105', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000001',
   NULL, '2026-06-15', -900, 'Ee Limited', 'Ee Limited//OTHER/£9.00', NULL, 'explained', 'feed', 'ee-2026-06-15'),
  ('20000000-0000-0000-0000-000000000106', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000001',
   NULL, '2026-06-18', -2434, 'Godaddy#4079137781', 'Godaddy#4079137781//OTHER/£24.34', NULL, 'unexplained', 'feed', 'godaddy-2026-06-18'),
  ('20000000-0000-0000-0000-000000000107', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000001',
   NULL, '2026-06-21', -154980, 'Airbnb * Hm995kjjqn', 'Airbnb * Hm995kjjqn//OTHER/£1,549.80', NULL, 'for_approval', 'feed', 'airbnb-2026-06-21'),
  -- Manual line (authored by the dev user; a transfer the user keyed in)
  ('20000000-0000-0000-0000-000000000103', '00000000-0000-0000-0000-000000000001', '20000000-0000-0000-0000-000000000001',
   (SELECT id FROM users WHERE email = 'dev@example.com'),
   '2026-06-11', 1641359, 'Transfer from Revolut EUR Main', NULL, NULL, 'explained', 'manual', NULL)
ON CONFLICT (id) DO NOTHING;
