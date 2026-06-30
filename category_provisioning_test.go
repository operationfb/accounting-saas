package main

// category_provisioning_test.go
// =============================================================================
// Per-org chart-of-accounts provisioning (real Postgres). A fresh org starts with no
// chart, so the GL resolver fails closed (ErrChartNotProvisioned); after
// categories.ProvisionChart copies the chart_template into its categories, the full
// chart is present and GL control roles resolve — proving the ledger works beyond the
// dev org. Also covers the country → global-fallback resolution and idempotency.
// =============================================================================

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	auth "github.com/operationfb/accounting-saas/db/auth"
	dbcategories "github.com/operationfb/accounting-saas/db/categories"
	dbledger "github.com/operationfb/accounting-saas/db/ledger"
	categories "github.com/operationfb/accounting-saas/internal/categories"
	ledger "github.com/operationfb/accounting-saas/internal/ledger"
)

func orgCategoryCount(t *testing.T, ts *testServer, orgID string) (total, subdivided int) {
	t.Helper()
	if err := ts.pool.QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE is_user_subdivided) FROM categories WHERE organisation_id = $1`,
		orgID).Scan(&total, &subdivided); err != nil {
		t.Fatalf("count org categories: %v", err)
	}
	return total, subdivided
}

func TestProvisionChartForNewOrg(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	org, _ := newOrgWithOwner(t, ts)
	orgUUID := uuid.MustParse(org)
	catQ := dbcategories.New(ts.pool)
	res := ledger.NewAccounts(catQ, dbledger.New(ts.pool), auth.New(ts.pool))

	// Before provisioning: no chart → the resolver fails closed.
	if _, err := res.Resolve(ctx, ledger.RoleDebtors, ledger.ResolveInput{OrganisationID: orgUUID, CompanyType: "limited"}); !errors.Is(err, ledger.ErrChartNotProvisioned) {
		t.Fatalf("a fresh org should have no chart (ErrChartNotProvisioned), got %v", err)
	}

	// Provision from the template (GB has no specific template → global fallback).
	if err := categories.ProvisionChart(ctx, catQ, orgUUID, "GB"); err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(), `DELETE FROM categories WHERE organisation_id = $1`, org)
	})

	// The full template was copied.
	var tmpl int
	if err := ts.pool.QueryRow(ctx, `SELECT count(*) FROM chart_template WHERE country_code IS NULL`).Scan(&tmpl); err != nil {
		t.Fatalf("template count: %v", err)
	}
	total, subdivided := orgCategoryCount(t, ts, org)
	if total != tmpl {
		t.Errorf("provisioned %d categories, want %d (the template)", total, tmpl)
	}
	if subdivided != 7 {
		t.Errorf("expected 7 user-subdivided accounts (900–910), got %d", subdivided)
	}

	// The payoff: GL control roles now resolve for this NON-dev org.
	debtors, err := res.Resolve(ctx, ledger.RoleDebtors, ledger.ResolveInput{OrganisationID: orgUUID, CompanyType: "limited"})
	if err != nil {
		t.Fatalf("DEBTORS should resolve after provisioning: %v", err)
	}
	var nominal string
	if err := ts.pool.QueryRow(ctx, `SELECT nominal_code FROM categories WHERE id = $1`, debtors).Scan(&nominal); err != nil {
		t.Fatalf("read resolved account: %v", err)
	}
	if nominal != "681" {
		t.Errorf("DEBTORS resolved to nominal %s, want 681 (Trade Debtors)", nominal)
	}

	// Idempotent: re-provisioning changes nothing.
	if err := categories.ProvisionChart(ctx, catQ, orgUUID, "GB"); err != nil {
		t.Fatalf("re-provision: %v", err)
	}
	if total2, _ := orgCategoryCount(t, ts, org); total2 != total {
		t.Errorf("re-provisioning changed the count: %d → %d", total, total2)
	}
}

func TestProvisionChartCountryFallback(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	org, _ := newOrgWithOwner(t, ts)

	// A country with no specific template still gets the global fallback chart.
	if err := categories.ProvisionChart(ctx, dbcategories.New(ts.pool), uuid.MustParse(org), "ZZ"); err != nil {
		t.Fatalf("provision ZZ: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(), `DELETE FROM categories WHERE organisation_id = $1`, org)
	})
	if total, _ := orgCategoryCount(t, ts, org); total == 0 {
		t.Error("a country with no template should fall back to the global chart, got 0 categories")
	}
}
