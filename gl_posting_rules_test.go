package main

// gl_posting_rules_test.go
// =============================================================================
// OFFLINE validation of the gl_posting_rules mapping (real Postgres, no network).
//
//   TestGLPostingRulesBalance — every event's legs balance. Symbolically GROSS =
//     NET + VAT, so summing DR as + and CR as − must cancel BOTH the net and vat
//     components. This is the double-entry invariant checked on the seed data
//     itself (the DB Σ=0 trigger on the future journal lines enforces it at post
//     time; this catches a bad rule before any posting happens).
//
//   TestGLPostingRulesCategoryLegsMatchExplainPicker — the cross-check against the
//     FreeAgent-sourced mapping: a bank transaction type posts to the user-picked
//     category (EXPLANATION_CATEGORY) IFF transaction_type_categories offers
//     categories for that type. Catches a rule that posts to a category a FreeAgent
//     explanation of that type couldn't pick (or an entity-link type that wrongly
//     posts to a free category instead of its control account).
//
// Both read the rules through the generated db/ledger layer, so they also exercise
// that the sqlc package works.
// =============================================================================

import (
	"context"
	"testing"

	dbledger "github.com/operationfb/accounting-saas/db/ledger"
)

// basisVec maps an amount_basis to its (net, vat) component weights. GROSS spans
// both axes because GROSS = NET + VAT.
func basisVec(basis string) (net, vat int) {
	switch basis {
	case "GROSS":
		return 1, 1
	case "NET":
		return 1, 0
	case "VAT":
		return 0, 1
	default:
		return 0, 0
	}
}

func TestGLPostingRulesBalance(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()

	rules, err := dbledger.New(ts.pool).ListPostingRules(ctx)
	if err != nil {
		t.Fatalf("list posting rules: %v", err)
	}
	if len(rules) == 0 {
		t.Fatal("no gl_posting_rules seeded — apply db/seeds/gl_posting_rules.sql")
	}

	type acc struct {
		net, vat         int
		drCount, crCount int
	}
	byEvent := map[string]*acc{}
	for _, r := range rules {
		a := byEvent[r.EventCode]
		if a == nil {
			a = &acc{}
			byEvent[r.EventCode] = a
		}
		n, v := basisVec(r.AmountBasis)
		sign := 1
		if r.Direction == "CR" {
			sign = -1
			a.crCount++
		} else {
			a.drCount++
		}
		a.net += sign * n
		a.vat += sign * v
	}

	for ev, a := range byEvent {
		if a.net != 0 || a.vat != 0 {
			t.Errorf("event %q does not balance: net=%d vat=%d (DR must equal CR per component)", ev, a.net, a.vat)
		}
		if a.drCount == 0 || a.crCount == 0 {
			t.Errorf("event %q must have at least one DR and one CR leg (got DR=%d CR=%d)", ev, a.drCount, a.crCount)
		}
	}
}

func TestGLPostingRulesCategoryLegsMatchExplainPicker(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()

	rules, err := dbledger.New(ts.pool).ListPostingRules(ctx)
	if err != nil {
		t.Fatalf("list posting rules: %v", err)
	}

	// Events whose rules post to the user-picked category.
	usesCategory := map[string]bool{}
	for _, r := range rules {
		if r.AccountRole == "EXPLANATION_CATEGORY" {
			usesCategory[r.EventCode] = true
		}
	}

	// The 18 bank transaction types (the only events these two tables share).
	bankTypes := map[string]bool{}
	rows, err := ts.pool.Query(ctx, `SELECT code FROM transaction_types`)
	if err != nil {
		t.Fatalf("query transaction_types: %v", err)
	}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			rows.Close()
			t.Fatalf("scan transaction_types: %v", err)
		}
		bankTypes[code] = true
	}
	rows.Close()
	if len(bankTypes) == 0 {
		t.Fatal("no transaction_types seeded — apply db/seeds/transaction_types.sql")
	}

	// Types that OFFER categories in the FreeAgent-sourced explain mapping.
	offersCategory := map[string]bool{}
	rows2, err := ts.pool.Query(ctx, `SELECT DISTINCT transaction_type_code FROM transaction_type_categories`)
	if err != nil {
		t.Fatalf("query transaction_type_categories: %v", err)
	}
	for rows2.Next() {
		var code string
		if err := rows2.Scan(&code); err != nil {
			rows2.Close()
			t.Fatalf("scan transaction_type_categories: %v", err)
		}
		offersCategory[code] = true
	}
	rows2.Close()

	for code := range bankTypes {
		if usesCategory[code] != offersCategory[code] {
			t.Errorf("transaction type %q: gl_posting_rules posts-to-picked-category=%v, but transaction_type_categories offers categories=%v — the GL rule and the explain picker disagree",
				code, usesCategory[code], offersCategory[code])
		}
	}
}
