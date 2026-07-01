-- =============================================================================
-- SEED: gl_posting_rules  (the double-entry mapping, AS DATA)
-- GLOBAL reference data (no organisation_id) — the Dr/Cr recipe for every economic
-- event. One event_code = several leg rows that together form one BALANCED journal
-- entry. A generic interpreter reads these to post entries (no Go per-event logic).
--
-- Sign / balance convention (symbolic): GROSS = NET + VAT. An entry balances when,
-- summed with DR as + and CR as −, both the NET and VAT components cancel. e.g.
-- PAYMENT: DR category(NET) + DR VAT(VAT) − CR bank(GROSS) = (NET+VAT) − (NET+VAT) = 0.
-- At runtime a leg whose resolved amount is 0 (e.g. the VAT leg on a no-VAT line)
-- is simply dropped. (Validated offline in gl_posting_rules_test.go and against
-- FreeAgent's chart/trial balance in gl_posting_rules_freeagent_test.go.)
--
-- account_role is symbolic and resolved to a categories row at post time:
--   EXPLANATION_CATEGORY = the category the user picked on the bank explanation
--   SOURCE_CATEGORY      = the bill/expense line's category
--   BANK                 = the bank line's own account; TRANSFER_*_BANK the two sides
--   DEBTORS/CREDITORS/USER_ACCOUNT/OPENING_EQUITY = control accounts
--   VAT_CHARGED = output VAT on sales (819); VAT_RECLAIMED = input VAT on purchases (818)
--     (VAT_CONTROL/817 is the VAT-return control — reserved, not used by any rule below)
--   SALES_DEFAULT        = default income (001) until invoices carry per-line categories
--   SUSPENSE             = holding account for not-yet-built entity types (credit
--                          note / HP) — PROVISIONAL, replace when those modules land
--
-- Idempotent: ON CONFLICT (event_code, leg_no, company_type) DO NOTHING.
-- Apply with:  psql "$DATABASE_URL" -f db/seeds/gl_posting_rules.sql
-- =============================================================================

INSERT INTO gl_posting_rules (event_code, leg_no, account_role, amount_basis, direction, display_order) VALUES

  -- ========================== BANK EXPLANATIONS — MONEY OUT ==========================
  -- Payment to a CoA category (net to the category, input VAT to VAT Reclaimed 818, gross out of bank).
  ('PAYMENT',                 1, 'EXPLANATION_CATEGORY', 'NET',   'DR', 1),
  ('PAYMENT',                 2, 'VAT_RECLAIMED',        'VAT',   'DR', 2),
  ('PAYMENT',                 3, 'BANK',                 'GROSS', 'CR', 3),

  -- Bill payment: settles a payable (no VAT — that was booked at bill creation).
  ('BILL_PAYMENT',            1, 'CREDITORS',            'GROSS', 'DR', 1),
  ('BILL_PAYMENT',            2, 'BANK',                 'GROSS', 'CR', 2),

  -- Transfer to another account: money out of this bank, into the destination bank.
  ('TRANSFER_TO_ACCOUNT',     1, 'TRANSFER_DEST_BANK',   'GROSS', 'DR', 1),
  ('TRANSFER_TO_ACCOUNT',     2, 'BANK',                 'GROSS', 'CR', 2),

  -- Money paid to a user: debit the picked account (salary/dividend/DLA/expense), pay from bank.
  ('MONEY_PAID_TO_USER',      1, 'EXPLANATION_CATEGORY', 'GROSS', 'DR', 1),
  ('MONEY_PAID_TO_USER',      2, 'BANK',                 'GROSS', 'CR', 2),

  -- Purchase of a capital asset: the picked 602 account (net) + input VAT, out of bank.
  ('PURCHASE_CAPITAL_ASSET',  1, 'EXPLANATION_CATEGORY', 'NET',   'DR', 1),
  ('PURCHASE_CAPITAL_ASSET',  2, 'VAT_RECLAIMED',        'VAT',   'DR', 2),
  ('PURCHASE_CAPITAL_ASSET',  3, 'BANK',                 'GROSS', 'CR', 3),

  -- Sales refund (money out reduces sales): debit the income category (net) + reverse VAT.
  ('SALES_REFUND',            1, 'EXPLANATION_CATEGORY', 'NET',   'DR', 1),
  ('SALES_REFUND',            2, 'VAT_CHARGED',          'VAT',   'DR', 2),
  ('SALES_REFUND',            3, 'BANK',                 'GROSS', 'CR', 3),

  -- Credit note refund (future entity): provisional — bank vs suspense until the module lands.
  ('CREDIT_NOTE_REFUND',      1, 'SUSPENSE',             'GROSS', 'DR', 1),
  ('CREDIT_NOTE_REFUND',      2, 'BANK',                 'GROSS', 'CR', 2),

  -- HP payment (future entity): provisional — bank vs suspense.
  ('HP_PAYMENT',              1, 'SUSPENSE',             'GROSS', 'DR', 1),
  ('HP_PAYMENT',              2, 'BANK',                 'GROSS', 'CR', 2),

  -- Other money out: a specific control account (net) + any VAT, out of bank.
  ('OTHER_MONEY_OUT',         1, 'EXPLANATION_CATEGORY', 'NET',   'DR', 1),
  ('OTHER_MONEY_OUT',         2, 'VAT_RECLAIMED',        'VAT',   'DR', 2),
  ('OTHER_MONEY_OUT',         3, 'BANK',                 'GROSS', 'CR', 3),

  -- ========================== BANK EXPLANATIONS — MONEY IN ===========================
  -- Invoice receipt: cash in (BANK, bank-ccy), receivable down (DEBTORS, invoice-ccy at
  -- the booking rate), and the realised FX gain/loss between them (home-ccy). DEBTOR_RELIEF
  -- is the debtor relief at the ORIGINAL booking rate (≠ GROSS once the rate has moved);
  -- FX_GAIN/FX_LOSS are GROSS-base − relief, sign-split so the poster drops the zero leg.
  -- A home-currency receipt collapses to legs 1+2 (relief == gross, FX legs zero).
  ('INVOICE_RECEIPT',         1, 'BANK',                 'GROSS',         'DR', 1),
  ('INVOICE_RECEIPT',         2, 'DEBTORS',              'DEBTOR_RELIEF', 'CR', 2),
  ('INVOICE_RECEIPT',         3, 'FX_REALISED_GAIN',     'FX_GAIN',       'CR', 3),
  ('INVOICE_RECEIPT',         4, 'FX_REALISED_LOSS',     'FX_LOSS',       'DR', 4),

  -- Periodic unrealised revaluation of an OPEN foreign debtor (home-ccy only). Brings the
  -- outstanding receivable to today's rate: a gain raises the debtor (DR 681) against the
  -- unrealised gain account (CR 391); a loss is the mirror. Sign-split on FX_GAIN/FX_LOSS
  -- (the service supplies FX_GAIN = max(U,0), FX_LOSS = max(-U,0)) so the poster drops the
  -- zero pair — exactly two legs fire per run. Separate 391 nominal keeps it from
  -- double-counting the realised 390 booked on settlement; cleared/reversed when settled.
  ('INVOICE_REVALUATION',     1, 'DEBTORS',              'FX_GAIN',       'DR', 1),
  ('INVOICE_REVALUATION',     2, 'FX_UNREALISED_GAIN',   'FX_GAIN',       'CR', 2),
  ('INVOICE_REVALUATION',     3, 'DEBTORS',              'FX_LOSS',       'CR', 3),
  ('INVOICE_REVALUATION',     4, 'FX_UNREALISED_LOSS',   'FX_LOSS',       'DR', 4),

  -- Sales: cash in, income recognised (net) + output VAT.
  ('SALES',                   1, 'BANK',                 'GROSS', 'DR', 1),
  ('SALES',                   2, 'EXPLANATION_CATEGORY', 'NET',   'CR', 2),
  ('SALES',                   3, 'VAT_CHARGED',          'VAT',   'CR', 3),

  -- Transfer from another account: into this bank, out of the source bank.
  ('TRANSFER_FROM_ACCOUNT',   1, 'BANK',                 'GROSS', 'DR', 1),
  ('TRANSFER_FROM_ACCOUNT',   2, 'TRANSFER_SOURCE_BANK', 'GROSS', 'CR', 2),

  -- Refund received into a category (net) + VAT reclaimed back.
  ('REFUND',                  1, 'BANK',                 'GROSS', 'DR', 1),
  ('REFUND',                  2, 'EXPLANATION_CATEGORY', 'NET',   'CR', 2),
  ('REFUND',                  3, 'VAT_RECLAIMED',        'VAT',   'CR', 3),

  -- Bill refund: supplier returns money against a payable.
  ('BILL_REFUND',             1, 'BANK',                 'GROSS', 'DR', 1),
  ('BILL_REFUND',             2, 'CREDITORS',            'GROSS', 'CR', 2),

  -- Money received from a user: into bank, credit the picked account (DLA / capital introduced).
  ('MONEY_RECEIVED_FROM_USER',1, 'BANK',                 'GROSS', 'DR', 1),
  ('MONEY_RECEIVED_FROM_USER',2, 'EXPLANATION_CATEGORY', 'GROSS', 'CR', 2),

  -- Disposal of a capital asset: into bank, credit the 604 disposal account (net) + output VAT.
  ('DISPOSAL_CAPITAL_ASSET',  1, 'BANK',                 'GROSS', 'DR', 1),
  ('DISPOSAL_CAPITAL_ASSET',  2, 'EXPLANATION_CATEGORY', 'NET',   'CR', 2),
  ('DISPOSAL_CAPITAL_ASSET',  3, 'VAT_CHARGED',          'VAT',   'CR', 3),

  -- HP refund (future entity): provisional — bank vs suspense.
  ('HP_REFUND',               1, 'BANK',                 'GROSS', 'DR', 1),
  ('HP_REFUND',               2, 'SUSPENSE',             'GROSS', 'CR', 2),

  -- Other money in: a specific account (net) + any VAT, into bank.
  ('OTHER_MONEY_IN',          1, 'BANK',                 'GROSS', 'DR', 1),
  ('OTHER_MONEY_IN',          2, 'EXPLANATION_CATEGORY', 'NET',   'CR', 2),
  ('OTHER_MONEY_IN',          3, 'VAT_CHARGED',          'VAT',   'CR', 3),

  -- ============================ NON-BANK ECONOMIC EVENTS =============================
  -- Expense approved (claimant paid personally): expense (net) + input VAT, owed back to the user.
  ('EXPENSE_APPROVED',        1, 'SOURCE_CATEGORY',      'NET',   'DR', 1),
  ('EXPENSE_APPROVED',        2, 'VAT_RECLAIMED',        'VAT',   'DR', 2),
  ('EXPENSE_APPROVED',        3, 'USER_ACCOUNT',         'GROSS', 'CR', 3),

  -- Invoice sent: receivable up (gross) against income (net) + output VAT.
  ('INVOICE_SENT',            1, 'DEBTORS',              'GROSS', 'DR', 1),
  ('INVOICE_SENT',            2, 'SALES_DEFAULT',        'NET',   'CR', 2),
  ('INVOICE_SENT',            3, 'VAT_CHARGED',          'VAT',   'CR', 3),

  -- Bill created (unpaid): expense (net) + input VAT against a payable.
  ('BILL_CREATED',            1, 'SOURCE_CATEGORY',      'NET',   'DR', 1),
  ('BILL_CREATED',            2, 'VAT_RECLAIMED',        'VAT',   'DR', 2),
  ('BILL_CREATED',            3, 'CREDITORS',            'GROSS', 'CR', 3),

  -- Bank opening balance: opening cash against the opening-balances equity contra.
  ('BANK_OPENING',            1, 'BANK',                 'GROSS', 'DR', 1),
  ('BANK_OPENING',            2, 'OPENING_EQUITY',       'GROSS', 'CR', 2)

ON CONFLICT (event_code, leg_no, company_type) DO NOTHING;


-- =============================================================================
-- PAYROLL_COMPLETED — the payroll-run accrual (separate INSERT: it uses per_employee
-- + payroll amount_basis values). Triggered when a pay run goes draft → completed.
--
-- DEBITS  (employer's P&L cost):    gross pay + employer NI + employer pension
-- CREDITS (what is owed):           net pay (per employee) + PAYE/NI + pension
--                                   + student loan + other deductions
-- Balances because net = gross − paye − ee_ni − ee_pension − student_loan − other.
-- Zero-amount legs are dropped at post time, so v1 (nil pension/SL/other) collapses to
-- gross + employer_ni  ==  net + (paye + ee_ni + employer_ni).
--
-- The three EMPLOYER-COST expense legs split by director status via employee_filter:
-- STAFF (nic_calculation = 'employee') → 401/402/403; DIRECTOR (!= 'employee') →
-- 407/408/409. The poster sums each leg's basis over the matching payslips; an empty
-- group drops to a zero leg. Liability + net-pay legs stay ALL (same account for all).
--
-- NET_PAY_PAYABLE (leg 14) is per_employee = TRUE: one credit per payslip to that
-- employee's 902-x sub-account (902 is is_user_subdivided).
-- =============================================================================
INSERT INTO gl_posting_rules (event_code, leg_no, account_role, amount_basis, direction, per_employee, employee_filter, display_order) VALUES
  ('PAYROLL_COMPLETED',  1, 'PAYROLL_GROSS_EXPENSE',                     'GROSS_PAY',        'DR', FALSE, 'STAFF',     1),
  ('PAYROLL_COMPLETED',  2, 'PAYROLL_DIRECTOR_GROSS_EXPENSE',            'GROSS_PAY',        'DR', FALSE, 'DIRECTOR',  2),
  ('PAYROLL_COMPLETED',  3, 'PAYROLL_EMPLOYER_NI_EXPENSE',              'EMPLOYER_NI',      'DR', FALSE, 'STAFF',     3),
  ('PAYROLL_COMPLETED',  4, 'PAYROLL_DIRECTOR_EMPLOYER_NI_EXPENSE',     'EMPLOYER_NI',      'DR', FALSE, 'DIRECTOR',  4),
  ('PAYROLL_COMPLETED',  5, 'PAYROLL_EMPLOYER_PENSION_EXPENSE',         'EMPLOYER_PENSION', 'DR', FALSE, 'STAFF',     5),
  ('PAYROLL_COMPLETED',  6, 'PAYROLL_DIRECTOR_EMPLOYER_PENSION_EXPENSE','EMPLOYER_PENSION', 'DR', FALSE, 'DIRECTOR',  6),
  ('PAYROLL_COMPLETED',  7, 'PAYE_NI_LIABILITY',                        'PAYE',             'CR', FALSE, 'ALL',       7),
  ('PAYROLL_COMPLETED',  8, 'PAYE_NI_LIABILITY',                        'EMPLOYEE_NI',      'CR', FALSE, 'ALL',       8),
  ('PAYROLL_COMPLETED',  9, 'PAYE_NI_LIABILITY',                        'EMPLOYER_NI',      'CR', FALSE, 'ALL',       9),
  ('PAYROLL_COMPLETED', 10, 'PENSION_LIABILITY',                        'EMPLOYEE_PENSION', 'CR', FALSE, 'ALL',      10),
  ('PAYROLL_COMPLETED', 11, 'PENSION_LIABILITY',                        'EMPLOYER_PENSION', 'CR', FALSE, 'ALL',      11),
  ('PAYROLL_COMPLETED', 12, 'STUDENT_LOAN_LIABILITY',                   'STUDENT_LOAN',     'CR', FALSE, 'ALL',      12),
  ('PAYROLL_COMPLETED', 13, 'OTHER_PAYROLL_DEDUCTIONS',                 'OTHER_DEDUCTIONS', 'CR', FALSE, 'ALL',      13),
  ('PAYROLL_COMPLETED', 14, 'NET_PAY_PAYABLE',                          'NET_PAY',          'CR', TRUE,  'ALL',      14)
ON CONFLICT (event_code, leg_no, company_type) DO NOTHING;
