package main

// reconcile_test.go
// =============================================================================
// Data-layer tests for the explain-reconcile increment (categories + the
// bank_transaction_explanations record), against real PostgreSQL — same approach
// as banking_test.go. Two concerns:
//
//   TestExplanationRecomputeTrigger — the recompute trigger keeps a transaction's
//     unexplained_amount_minor + status in sync as explanations are added, split,
//     marked for review, and soft-deleted. Uses a FRESH ephemeral org; category_id
//     is left NULL (the trigger only sums gross_value_minor), so it needs no seed.
//
//   TestListCategoriesForType — the type -> category mapping resolves the offered
//     accounts per transaction type, branched by company_type. Reads the SEEDED CoA
//     (db/seeds/categories.sql), so it runs against the dev org, read-only.
//
// Skips without DATABASE_URL, like every other DB test.
// =============================================================================

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/operationfb/accounting-saas/db/banking"
	"github.com/operationfb/accounting-saas/db/categories"
)

func TestExplanationRecomputeTrigger(t *testing.T) {
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })
	q := banking.New(ts.pool)
	ctx := context.Background()

	org, user := newOrgWithOwner(t, ts)
	acc := createBankAccount(t, ts, q, org, user, nil)
	// Hard-delete this org's explanations BEFORE the account/txn cleanup runs
	// (LIFO: registered last → runs first), so the FK from explanations to
	// bank_transactions doesn't block the transaction delete.
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(),
			`DELETE FROM bank_transaction_explanations WHERE organisation_id = $1`, org)
	})

	orgID := mustUUID(t, org)

	// reload the parent transaction's derived state.
	reload := func(txnID uuid.UUID) banking.BankTransaction {
		got, err := q.GetBankTransaction(ctx, banking.GetBankTransactionParams{ID: txnID, OrganisationID: orgID})
		if err != nil {
			t.Fatalf("GetBankTransaction: %v", err)
		}
		return got
	}
	// add one explanation portion (NULL category — the trigger only sums the value).
	addExpl := func(txnID uuid.UUID, amount int64, review bool) uuid.UUID {
		e, err := q.CreateExplanation(ctx, banking.CreateExplanationParams{
			OrganisationID:    orgID,
			BankTransactionID: txnID,
			DatedOn:           mkdate(2026, 6, 23),
			Type:              "PAYMENT",
			GrossValueMinor:   amount,
			SalesTaxStatus:    "TAXABLE",
			MarkedForReview:   review,
		})
		if err != nil {
			t.Fatalf("CreateExplanation: %v", err)
		}
		return e.ID
	}
	// assert the derived state after a change.
	assertState := func(t *testing.T, txnID uuid.UUID, wantStatus string, wantUnexplained int64) {
		t.Helper()
		got := reload(txnID)
		if got.Status != wantStatus {
			t.Errorf("status: got %q, want %q", got.Status, wantStatus)
		}
		if !got.UnexplainedAmountMinor.Valid || got.UnexplainedAmountMinor.Int64 != wantUnexplained {
			t.Errorf("unexplained_amount_minor: got %v, want %d", got.UnexplainedAmountMinor, wantUnexplained)
		}
	}

	// Each scenario gets its own £100 money-OUT line (-10000 pence).
	newTxn := func() uuid.UUID {
		return createBankTxn(t, q, org, acc.ID, mkdate(2026, 6, 23), -10000, nil).ID
	}

	t.Run("full explanation marks explained, unexplained = 0", func(t *testing.T) {
		txn := newTxn()
		addExpl(txn, -10000, false)
		assertState(t, txn, "explained", 0)
	})

	t.Run("split into two still fully explains", func(t *testing.T) {
		txn := newTxn()
		addExpl(txn, -6000, false)
		assertState(t, txn, "unexplained", -4000) // partial after the first split
		addExpl(txn, -4000, false)
		assertState(t, txn, "explained", 0) // -6000 + -4000 = -10000
	})

	t.Run("partial explanation leaves the remainder unexplained", func(t *testing.T) {
		txn := newTxn()
		addExpl(txn, -6000, false)
		assertState(t, txn, "unexplained", -4000)
	})

	t.Run("a marked-for-review explanation is for_approval", func(t *testing.T) {
		txn := newTxn()
		addExpl(txn, -10000, true)
		assertState(t, txn, "for_approval", 0)
	})

	t.Run("soft-deleting an explanation re-opens the remainder", func(t *testing.T) {
		txn := newTxn()
		addExpl(txn, -6000, false)
		second := addExpl(txn, -4000, false)
		assertState(t, txn, "explained", 0)
		// Remove the -4000 portion → back to -4000 unexplained.
		if err := q.SoftDeleteExplanation(ctx, banking.SoftDeleteExplanationParams{ID: second, OrganisationID: orgID}); err != nil {
			t.Fatalf("SoftDeleteExplanation: %v", err)
		}
		assertState(t, txn, "unexplained", -4000)
	})
}

func TestListCategoriesForType(t *testing.T) {
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })
	cq := categories.New(ts.pool)
	ctx := context.Background()
	// The seeded dev org (db/seeds/categories.sql targets it). Read-only test.
	devOrg := mustUUID(t, "00000000-0000-0000-0000-000000000001")

	list := func(typeCode, companyType string) []categories.ListCategoriesForTypeRow {
		rows, err := cq.ListCategoriesForType(ctx, categories.ListCategoriesForTypeParams{
			OrganisationID:      devOrg,
			TransactionTypeCode: typeCode,
			CompanyType:         companyType,
		})
		if err != nil {
			t.Fatalf("ListCategoriesForType(%s, %s): %v", typeCode, companyType, err)
		}
		return rows
	}
	// label returns the offered label (display_label override, else category name) for a nominal.
	label := func(rows []categories.ListCategoriesForTypeRow, nominal string) (string, bool) {
		for _, r := range rows {
			if r.Category.NominalCode == nominal {
				if r.DisplayLabel.Valid {
					return r.DisplayLabel.String, true
				}
				return r.Category.Name, true
			}
		}
		return "", false
	}

	t.Run("Payment offers every expense category + the payable taxes", func(t *testing.T) {
		rows := list("PAYMENT", "limited")
		// 4 cost-of-sales + 61 admin-expenses + 4 tax accounts = 69. The 61 admin includes
		// the 390/391 Currency Exchange Gain/Loss accounts (realised/unrealised FX, added
		// 2026-06-29/06-30), both api_group admin_expenses_categories so PAYMENT offers them.
		if len(rows) != 69 {
			t.Errorf("PAYMENT/limited: got %d categories, want 69", len(rows))
		}
		if _, ok := label(rows, "254"); !ok { // Travel and Subsistence (admin)
			t.Error("PAYMENT should offer 254 Travel and Subsistence")
		}
		if _, ok := label(rows, "820"); !ok { // Corporation Tax
			t.Error("PAYMENT should offer 820 Corporation Tax")
		}
	})

	t.Run("Sales offers income", func(t *testing.T) {
		rows := list("SALES", "limited")
		if _, ok := label(rows, "001"); !ok {
			t.Error("SALES should offer 001 Sales")
		}
	})

	t.Run("Money Paid to User branches Ltd vs sole trader, with tab labels", func(t *testing.T) {
		ltd := list("MONEY_PAID_TO_USER", "limited")
		if len(ltd) != 5 {
			t.Errorf("MONEY_PAID_TO_USER/limited: got %d, want 5", len(ltd))
		}
		if lbl, _ := label(ltd, "908"); lbl != "Dividend" { // tab label agrees with the CoA name (908 = Dividend)
			t.Errorf("908 under Ltd Money Paid to User: got %q, want Dividend", lbl)
		}
		if lbl, _ := label(ltd, "907"); lbl != "Director's Loan Account" {
			t.Errorf("907 under Ltd: got %q, want Director's Loan Account", lbl)
		}

		sole := list("MONEY_PAID_TO_USER", "sole_trader")
		if len(sole) != 2 {
			t.Errorf("MONEY_PAID_TO_USER/sole_trader: got %d, want 2", len(sole))
		}
		if lbl, _ := label(sole, "907"); lbl != "Drawings" { // 907 is Drawings for a sole trader
			t.Errorf("907 under sole trader: got %q, want Drawings", lbl)
		}
	})

	t.Run("entity-link types offer no categories", func(t *testing.T) {
		for _, ty := range []string{"TRANSFER_TO_ACCOUNT", "BILL_PAYMENT", "INVOICE_RECEIPT"} {
			if rows := list(ty, "limited"); len(rows) != 0 {
				t.Errorf("%s: got %d categories, want 0 (account comes from the linked entity)", ty, len(rows))
			}
		}
	})
}
