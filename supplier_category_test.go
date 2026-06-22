package main

// supplier_category_test.go
// =============================================================================
// Tests for the supplier→category "dictionary" auto-categorisation feature.
//
// Two layers, matching the two halves of the feature:
//   - TestSupplierCategoryLearnTrigger exercises the POPULATE side — the
//     learn_supplier_category() plpgsql trigger — directly against real
//     PostgreSQL. The trigger fires for ANY writer, so the natural level to test
//     it is the DB: we INSERT/UPDATE expense rows and assert what lands in
//     supplier_category_map. (DB only — runs without GCS, like the other DB tests.)
//   - TestSupplierCategoryAutoCategorise exercises the CONSUME side end-to-end
//     through the real Smart Upload + OCR pipeline (faking only Document AI),
//     proving a captured receipt from a known supplier gets auto-categorised.
//     (Needs GCS, like the other capture tests.)
// =============================================================================

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	// Dot-import (test-only, this file): the auto-categorise test references the
	// ocr package's ExtractionResult + DocumentTypeReceipt and drives OcrService;
	// dot-importing keeps them unqualified, as before the package split.
	attachments "github.com/operationfb/accounting-saas/internal/attachments"
	expenses "github.com/operationfb/accounting-saas/internal/expenses"
	. "github.com/operationfb/accounting-saas/internal/ocr"
)

// =============================================================================
// HELPERS
// =============================================================================

// categoryUUID returns the id (as text) of an org's category with the given
// nominal code — so a test can assert against a SPECIFIC category (Travel,
// Office, the Sundries placeholder) rather than a random one.
func categoryUUID(t *testing.T, ts *testServer, orgID, nominal string) string {
	t.Helper()
	var id string
	if err := ts.pool.QueryRow(context.Background(),
		`SELECT id::text FROM expense_categories WHERE organisation_id = $1 AND nominal_code = $2`,
		orgID, nominal).Scan(&id); err != nil {
		t.Fatalf("categoryUUID(%s): %v", nominal, err)
	}
	return id
}

// insertExpenseRow inserts a minimal expense directly (bypassing the service) so
// the trigger test can control supplier_name + needs_review precisely. Returns
// the new id and registers cleanup. supplier is *string so nil → SQL NULL.
func insertExpenseRow(t *testing.T, ts *testServer, orgID, userID, categoryID string, supplier *string, needsReview bool) string {
	t.Helper()
	id := uuid.NewString()
	ctx := context.Background()
	if _, err := ts.pool.Exec(ctx, `
		INSERT INTO expenses (
			id, organisation_id, user_id, created_by_user_id, category_id,
			dated_on, description, gross_value_minor, native_gross_value_minor,
			supplier_name, needs_review
		) VALUES ($1, $2, $3, $3, $4, CURRENT_DATE, 'test expense', 1000, 1000, $5, $6)`,
		id, orgID, userID, categoryID, supplier, needsReview); err != nil {
		t.Fatalf("insertExpenseRow: %v", err)
	}
	t.Cleanup(func() { _, _ = ts.pool.Exec(ctx, `DELETE FROM expenses WHERE id = $1`, id) })
	return id
}

// mappedCategory reads the category (as text) the dictionary remembers for a
// normalised supplier_key, plus whether any mapping exists at all.
func mappedCategory(t *testing.T, ts *testServer, orgID, supplierKey string) (string, bool) {
	t.Helper()
	var cat string
	err := ts.pool.QueryRow(context.Background(),
		`SELECT category_id::text FROM supplier_category_map WHERE organisation_id = $1 AND supplier_key = $2`,
		orgID, supplierKey).Scan(&cat)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatalf("mappedCategory(%q): %v", supplierKey, err)
	}
	return cat, true
}

// cleanupMapKeys deletes the given supplier_keys for an org after the test, so
// the shared dev DB's dictionary stays clean (these rows are not cascade-deleted
// with the expenses, since they reference categories, not expenses).
func cleanupMapKeys(t *testing.T, ts *testServer, orgID string, keys ...string) {
	t.Cleanup(func() {
		for _, k := range keys {
			_, _ = ts.pool.Exec(context.Background(),
				`DELETE FROM supplier_category_map WHERE organisation_id = $1 AND supplier_key = $2`, orgID, k)
		}
	})
}

// =============================================================================
// POPULATE — the learn_supplier_category() trigger
// =============================================================================

func TestSupplierCategoryLearnTrigger(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	travel := categoryUUID(t, ts, devOrgID, "365")   // Travel
	sundries := categoryUUID(t, ts, devOrgID, "280") // Sundries (also the Smart Upload placeholder)
	office := categoryUUID(t, ts, devOrgID, "250")   // Office Costs

	t.Run("a confirmed expense teaches supplier→category", func(t *testing.T) {
		cleanupMapKeys(t, ts, devOrgID, "tesco stores")
		sup := "Tesco Stores"
		insertExpenseRow(t, ts, devOrgID, devUserID, travel, &sup, false)

		got, ok := mappedCategory(t, ts, devOrgID, "tesco stores")
		if !ok {
			t.Fatal("a confirmed expense with a supplier should create a mapping")
		}
		if got != travel {
			t.Errorf("category: got %s, want Travel %s", got, travel)
		}
	})

	t.Run("normalisation collapses case and whitespace to one key", func(t *testing.T) {
		cleanupMapKeys(t, ts, devOrgID, "pret a manger")
		a, b := "Pret A Manger", "  PRET a manger "
		insertExpenseRow(t, ts, devOrgID, devUserID, travel, &a, false)
		insertExpenseRow(t, ts, devOrgID, devUserID, sundries, &b, false) // different spelling, same supplier

		var n int
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM supplier_category_map WHERE organisation_id = $1 AND supplier_key = 'pret a manger'`,
			devOrgID).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 1 {
			t.Errorf("case/whitespace variants must collapse to ONE row; got %d", n)
		}
	})

	t.Run("last-write-wins overwrites the remembered category", func(t *testing.T) {
		cleanupMapKeys(t, ts, devOrgID, "costa coffee")
		sup := "Costa Coffee"
		insertExpenseRow(t, ts, devOrgID, devUserID, travel, &sup, false)
		if got, _ := mappedCategory(t, ts, devOrgID, "costa coffee"); got != travel {
			t.Fatalf("setup: want Travel %s, got %s", travel, got)
		}
		insertExpenseRow(t, ts, devOrgID, devUserID, office, &sup, false) // newer confirmation wins
		if got, _ := mappedCategory(t, ts, devOrgID, "costa coffee"); got != office {
			t.Errorf("last-write-wins: want Office %s, got %s", office, got)
		}
	})

	t.Run("an unconfirmed capture does NOT teach until confirmed", func(t *testing.T) {
		cleanupMapKeys(t, ts, devOrgID, "greggs")
		sup := "Greggs"
		// needs_review = TRUE with the placeholder category: learning here would
		// poison the dictionary with 'Sundries'. The trigger must skip it.
		id := insertExpenseRow(t, ts, devOrgID, devUserID, sundries, &sup, true)
		if _, ok := mappedCategory(t, ts, devOrgID, "greggs"); ok {
			t.Fatal("must NOT learn from a needs_review capture")
		}

		// A human confirms with the real category → now it learns.
		if _, err := ts.pool.Exec(context.Background(),
			`UPDATE expenses SET needs_review = FALSE, category_id = $2 WHERE id = $1`, id, travel); err != nil {
			t.Fatalf("confirm: %v", err)
		}
		got, ok := mappedCategory(t, ts, devOrgID, "greggs")
		if !ok || got != travel {
			t.Errorf("after confirm: want Travel %s, got %s (exists=%v)", travel, got, ok)
		}
	})

	t.Run("a blank or missing supplier is never learned", func(t *testing.T) {
		blank := "   "
		insertExpenseRow(t, ts, devOrgID, devUserID, travel, &blank, false)
		insertExpenseRow(t, ts, devOrgID, devUserID, travel, nil, false)
		if _, ok := mappedCategory(t, ts, devOrgID, ""); ok {
			t.Error("a blank/NULL supplier must not create a mapping")
		}
	})

	t.Run("an unrelated edit does not clobber a newer mapping", func(t *testing.T) {
		// The subtle bug the change-guard prevents: editing an OLD expense's
		// unrelated field must not relearn its (now stale) category and overwrite a
		// newer mapping a later expense established.
		cleanupMapKeys(t, ts, devOrgID, "churn co")
		sup := "Churn Co"
		old := insertExpenseRow(t, ts, devOrgID, devUserID, travel, &sup, false) // churn co → Travel

		// Simulate a later, different confirmation winning last-write-wins:
		if _, err := ts.pool.Exec(context.Background(),
			`UPDATE supplier_category_map SET category_id = $2 WHERE organisation_id = $1 AND supplier_key = 'churn co'`,
			devOrgID, office); err != nil {
			t.Fatalf("set newer mapping: %v", err)
		}

		// Edit the OLD expense's description only (supplier/category unchanged,
		// already confirmed). The trigger must SKIP — leaving churn co → Office.
		if _, err := ts.pool.Exec(context.Background(),
			`UPDATE expenses SET description = 'fixed a typo' WHERE id = $1`, old); err != nil {
			t.Fatalf("edit description: %v", err)
		}
		if got, _ := mappedCategory(t, ts, devOrgID, "churn co"); got != office {
			t.Errorf("an unrelated edit clobbered the newer mapping: got %s, want Office %s", got, office)
		}
	})

	t.Run("mappings are organisation-scoped", func(t *testing.T) {
		cleanupMapKeys(t, ts, devOrgID, "boots")
		sup := "Boots"
		insertExpenseRow(t, ts, devOrgID, devUserID, travel, &sup, false)

		orgB, _ := newOrgWithOwner(t, ts)
		if _, ok := mappedCategory(t, ts, orgB, "boots"); ok {
			t.Error("another organisation must NOT see this org's learned mapping")
		}
	})
}

// =============================================================================
// CONSUME — auto-categorise a capture through the OCR pipeline
// =============================================================================

func TestSupplierCategoryAutoCategorise(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()

	devOrg := mustUUID(t, devOrgID)
	travel := categoryUUID(t, ts, devOrgID, "365")
	office := categoryUUID(t, ts, devOrgID, "250")
	placeholder := categoryUUID(t, ts, devOrgID, attachments.PlaceholderCategoryNominal) // 280 Sundries

	// seedMapping puts a remembered mapping in place directly, so the consume test
	// doesn't depend on the trigger having run.
	seedMapping := func(t *testing.T, key, categoryID string) {
		t.Helper()
		cleanupMapKeys(t, ts, devOrgID, key)
		if _, err := ts.pool.Exec(context.Background(),
			`INSERT INTO supplier_category_map (organisation_id, supplier_key, category_id)
			 VALUES ($1, $2, $3)
			 ON CONFLICT (organisation_id, supplier_key) DO UPDATE SET category_id = EXCLUDED.category_id`,
			devOrgID, key, categoryID); err != nil {
			t.Fatalf("seedMapping: %v", err)
		}
	}

	categoryOf := func(t *testing.T, expenseID string) string {
		t.Helper()
		var id string
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT category_id::text FROM expenses WHERE id = $1`, expenseID).Scan(&id); err != nil {
			t.Fatalf("categoryOf: %v", err)
		}
		return id
	}

	runOCR := func(t *testing.T, draft *expenses.ExpenseDetailResponse, supplier string) {
		t.Helper()
		fake := &fakeExtractor{result: &ExtractionResult{SupplierName: &supplier, Confidence: decimal.NewFromInt(1)}}
		if err := newOCRService(ts, fake).Process(context.Background(),
			mustUUID(t, draft.Attachments[0].ID), devOrg, DocumentTypeReceipt); err != nil {
			t.Fatalf("process: %v", err)
		}
	}

	t.Run("a known supplier auto-fills the category on capture", func(t *testing.T) {
		seedMapping(t, "tesco", travel)
		draft := captureAs(t, ts, &spyEnqueuer{}, devUserID, devOrgID, DocumentTypeReceipt, "r.pdf", samplePDF())
		if categoryOf(t, draft.ID) != placeholder {
			t.Fatalf("a fresh capture should start at the Sundries placeholder")
		}

		runOCR(t, draft, "Tesco") // OCR sees the supplier; the dictionary knows it

		if got := categoryOf(t, draft.ID); got != travel {
			t.Errorf("auto-categorise: category got %s, want Travel %s", got, travel)
		}
		// Auto-categorising is still NOT a human confirmation.
		var needsReview bool
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT needs_review FROM expenses WHERE id = $1`, draft.ID).Scan(&needsReview); err != nil {
			t.Fatalf("read needs_review: %v", err)
		}
		if !needsReview {
			t.Error("needs_review must stay true after auto-categorise (OCR ≠ confirmation)")
		}
	})

	t.Run("an unknown supplier leaves the placeholder", func(t *testing.T) {
		draft := captureAs(t, ts, &spyEnqueuer{}, devUserID, devOrgID, DocumentTypeReceipt, "r.pdf", samplePDF())
		runOCR(t, draft, "Totally Unseen Supplier Ltd")
		if got := categoryOf(t, draft.ID); got != placeholder {
			t.Errorf("an unknown supplier must keep the placeholder; got %s", got)
		}
	})

	t.Run("does not override a category the user already confirmed", func(t *testing.T) {
		seedMapping(t, "tesco", travel)
		draft := captureAs(t, ts, &spyEnqueuer{}, devUserID, devOrgID, DocumentTypeReceipt, "r.pdf", samplePDF())

		// User confirms with THEIR own category before OCR finishes.
		if _, err := ts.pool.Exec(context.Background(),
			`UPDATE expenses SET category_id = $2, needs_review = FALSE WHERE id = $1`, draft.ID, office); err != nil {
			t.Fatalf("user confirm: %v", err)
		}

		runOCR(t, draft, "Tesco") // dictionary says Travel, but the user chose Office

		if got := categoryOf(t, draft.ID); got != office {
			t.Errorf("auto-categorise must not override a confirmed category; got %s, want Office %s", got, office)
		}
	})
}
