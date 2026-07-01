package main

// gl_resolver_test.go
// =============================================================================
// Tests for the general-ledger account resolver (internal/ledger.Accounts) against
// real Postgres: fixed control roles → the right nominal, pass-through categories
// returned unchanged, and — the point of this iteration — per-user sub-account
// expansion (Dividend 908 → 908-1 for director A, 908-2 for director B), idempotent.
// =============================================================================

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/operationfb/accounting-saas/db/auth"
	dbcategories "github.com/operationfb/accounting-saas/db/categories"
	dbledger "github.com/operationfb/accounting-saas/db/ledger"
	"github.com/operationfb/accounting-saas/internal/ledger"
)

// categoryIDByNominal returns the dev org's PARENT account (user_id IS NULL) for a
// nominal code.
func categoryIDByNominal(t *testing.T, ts *testServer, nominal string) uuid.UUID {
	t.Helper()
	var id string
	err := ts.pool.QueryRow(context.Background(),
		`SELECT id::text FROM categories
		 WHERE organisation_id = $1 AND nominal_code = $2 AND user_id IS NULL`,
		devOrgID, nominal).Scan(&id)
	if err != nil {
		t.Fatalf("look up category %s: %v", nominal, err)
	}
	return uuid.MustParse(id)
}

func TestLedgerResolver(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	org := uuid.MustParse(devOrgID)

	res := ledger.NewAccounts(dbcategories.New(ts.pool), dbledger.New(ts.pool), auth.New(ts.pool))

	// --- fixed control roles resolve to their gl_account_roles nominal -------------
	for _, tc := range []struct{ role, nominal string }{
		{ledger.RoleDebtors, "681"},
		{ledger.RoleCreditors, "796"},
		{ledger.RoleVATControl, "817"},
		{ledger.RoleVATCharged, "819"},   // output VAT on sales
		{ledger.RoleVATReclaimed, "818"}, // input VAT on purchases
		{ledger.RoleSalesDefault, "001"},
	} {
		got, err := res.Resolve(ctx, tc.role, ledger.ResolveInput{OrganisationID: org, CompanyType: "limited"})
		if err != nil {
			t.Fatalf("resolve %s: %v", tc.role, err)
		}
		if want := categoryIDByNominal(t, ts, tc.nominal); got != want {
			t.Errorf("role %s: got category %s, want nominal %s (%s)", tc.role, got, tc.nominal, want)
		}
	}

	// --- a non-subdivided picked category is returned unchanged, even with a user --
	travel := categoryIDByNominal(t, ts, "254") // Travel and Subsistence
	someUser := uuid.MustParse(newMemberUser(t, ts, devOrgID))
	got, err := res.Resolve(ctx, ledger.RoleExplanationCategory, ledger.ResolveInput{
		OrganisationID: org, PickedCategoryID: &travel, UserID: &someUser,
	})
	if err != nil {
		t.Fatalf("resolve EXPLANATION_CATEGORY (travel): %v", err)
	}
	if got != travel {
		t.Errorf("non-subdivided category should be returned unchanged: got %s, want %s", got, travel)
	}

	// --- per-user sub-accounts: Dividend (908) splits per director -----------------
	userA := uuid.MustParse(newMemberUser(t, ts, devOrgID))
	userB := uuid.MustParse(newMemberUser(t, ts, devOrgID))
	// Clean up the sub-account rows we create BEFORE newMemberUser's user-delete
	// cleanups run (LIFO: registered later → runs first → no FK violation).
	t.Cleanup(func() {
		for _, u := range []uuid.UUID{userA, userB, someUser} {
			_, _ = ts.pool.Exec(context.Background(),
				`DELETE FROM categories WHERE organisation_id = $1 AND user_id = $2`, devOrgID, u)
		}
	})

	dividend := categoryIDByNominal(t, ts, "908")

	subA, err := res.Resolve(ctx, ledger.RoleExplanationCategory, ledger.ResolveInput{
		OrganisationID: org, PickedCategoryID: &dividend, UserID: &userA,
	})
	if err != nil {
		t.Fatalf("resolve Dividend for user A: %v", err)
	}
	if subA == dividend {
		t.Fatal("expected a sub-account distinct from the 908 parent for user A")
	}
	assertSubAccount(t, ts, subA, "908", userA)

	// Idempotent: the same user resolves to the same sub-account (no second row).
	subA2, err := res.Resolve(ctx, ledger.RoleExplanationCategory, ledger.ResolveInput{
		OrganisationID: org, PickedCategoryID: &dividend, UserID: &userA,
	})
	if err != nil {
		t.Fatalf("re-resolve Dividend for user A: %v", err)
	}
	if subA2 != subA {
		t.Errorf("user A's Dividend sub-account is not idempotent: %s then %s", subA, subA2)
	}

	// A different director gets a DIFFERENT sub-account.
	subB, err := res.Resolve(ctx, ledger.RoleExplanationCategory, ledger.ResolveInput{
		OrganisationID: org, PickedCategoryID: &dividend, UserID: &userB,
	})
	if err != nil {
		t.Fatalf("resolve Dividend for user B: %v", err)
	}
	if subB == subA {
		t.Error("user B's Dividend sub-account must differ from user A's")
	}
	assertSubAccount(t, ts, subB, "908", userB)

	// --- the whole 900–910 set now subdivides: 902 Net Salary expands per user too --
	netSalary := categoryIDByNominal(t, ts, "902")
	salSub, err := res.Resolve(ctx, ledger.RoleExplanationCategory, ledger.ResolveInput{
		OrganisationID: org, PickedCategoryID: &netSalary, UserID: &userA,
	})
	if err != nil {
		t.Fatalf("resolve Net Salary (902) for user A: %v", err)
	}
	if salSub == netSalary {
		t.Error("902 should now be user-subdivided (all 900–910 are)")
	}
	assertSubAccount(t, ts, salSub, "902", userA)

	// --- USER_ACCOUNT role expands to the claimant's 907 (DLA) sub-account ---------
	dlaSub, err := res.Resolve(ctx, ledger.RoleUserAccount, ledger.ResolveInput{
		OrganisationID: org, CompanyType: "limited", UserID: &userA,
	})
	if err != nil {
		t.Fatalf("resolve USER_ACCOUNT for user A: %v", err)
	}
	assertSubAccount(t, ts, dlaSub, "907", userA)
}

// assertSubAccount checks a resolved id is a sub-account row: nominal '<parent>-N',
// the right parent_nominal_code + user_id, and account_type inherited from the parent.
func assertSubAccount(t *testing.T, ts *testServer, id uuid.UUID, parentNominal string, user uuid.UUID) {
	t.Helper()
	var (
		nominal, accountType string
		parentCode           pgtype.Text
		userID               pgtype.UUID
	)
	err := ts.pool.QueryRow(context.Background(),
		`SELECT nominal_code, account_type, parent_nominal_code, user_id
		 FROM categories WHERE id = $1`, id).Scan(&nominal, &accountType, &parentCode, &userID)
	if err != nil {
		t.Fatalf("read sub-account %s: %v", id, err)
	}
	if !strings.HasPrefix(nominal, parentNominal+"-") {
		t.Errorf("sub-account nominal %q should start with %q-", nominal, parentNominal)
	}
	if parentCode.String != parentNominal {
		t.Errorf("sub-account parent_nominal_code = %q, want %q", parentCode.String, parentNominal)
	}
	if gotUser := uuid.UUID(userID.Bytes); !userID.Valid || gotUser != user {
		t.Errorf("sub-account user_id = %v (valid=%v), want %s", gotUser, userID.Valid, user)
	}
	if accountType != "USER_ACCOUNT" {
		t.Errorf("sub-account account_type = %q, want USER_ACCOUNT (inherited from parent)", accountType)
	}
}

// TestLedgerResolverBankAccounts: the BANK / TRANSFER_*_BANK roles expand the 750
// parent to a distinct 750-x ledger account per bank account, idempotent.
func TestLedgerResolverBankAccounts(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	org := uuid.MustParse(devOrgID)
	res := ledger.NewAccounts(dbcategories.New(ts.pool), dbledger.New(ts.pool), auth.New(ts.pool))

	// bank_account_id is a plain (un-FK'd) column, so any UUIDs serve as account ids.
	bankA, bankB := uuid.New(), uuid.New()
	t.Cleanup(func() {
		for _, b := range []uuid.UUID{bankA, bankB} {
			_, _ = ts.pool.Exec(context.Background(),
				`DELETE FROM categories WHERE organisation_id = $1 AND bank_account_id = $2`, devOrgID, b)
		}
	})

	parent750 := categoryIDByNominal(t, ts, "750")

	subA, err := res.Resolve(ctx, ledger.RoleBank, ledger.ResolveInput{OrganisationID: org, BankAccountID: &bankA})
	if err != nil {
		t.Fatalf("resolve BANK for account A: %v", err)
	}
	if subA == parent750 {
		t.Fatal("BANK should expand to a 750-x sub-account, not the 750 parent")
	}
	assertBankSubAccount(t, ts, subA, bankA)

	// Idempotent for the same account.
	if subA2, err := res.Resolve(ctx, ledger.RoleBank, ledger.ResolveInput{OrganisationID: org, BankAccountID: &bankA}); err != nil || subA2 != subA {
		t.Fatalf("BANK not idempotent for account A: %s then %s (err %v)", subA, subA2, err)
	}

	// A different account → a different sub-account; reached via TRANSFER_DEST_BANK too.
	subB, err := res.Resolve(ctx, ledger.RoleTransferDestBank, ledger.ResolveInput{OrganisationID: org, TransferBankAccountID: &bankB})
	if err != nil {
		t.Fatalf("resolve TRANSFER_DEST_BANK for account B: %v", err)
	}
	if subB == subA {
		t.Error("account B's ledger account must differ from account A's")
	}
	assertBankSubAccount(t, ts, subB, bankB)

	// A bank role with no account provided is a clear error, not a silent default.
	if _, err := res.Resolve(ctx, ledger.RoleBank, ledger.ResolveInput{OrganisationID: org}); err == nil {
		t.Error("BANK with no bank account should error")
	}
}

// TestLedgerResolverPayrollRoles: the payroll fixed roles resolve to their nominals,
// and NET_PAY_PAYABLE expands per employee (902 is user-subdivided).
func TestLedgerResolverPayrollRoles(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	org := uuid.MustParse(devOrgID)
	res := ledger.NewAccounts(dbcategories.New(ts.pool), dbledger.New(ts.pool), auth.New(ts.pool))

	for _, tc := range []struct{ role, nominal string }{
		{ledger.RolePayrollGrossExpense, "401"},
		{ledger.RolePayrollEmployerNIExpense, "402"},
		{ledger.RolePayrollDirectorGrossExpense, "407"},
		{ledger.RolePayrollDirectorEmployerNIExpense, "408"},
		{ledger.RolePayrollDirectorEmployerPensionExpense, "409"},
		{ledger.RolePAYENILiability, "814"},
		{ledger.RolePensionLiability, "813"},
	} {
		got, err := res.Resolve(ctx, tc.role, ledger.ResolveInput{OrganisationID: org, CompanyType: "limited"})
		if err != nil {
			t.Fatalf("resolve %s: %v", tc.role, err)
		}
		if want := categoryIDByNominal(t, ts, tc.nominal); got != want {
			t.Errorf("role %s: got %s, want nominal %s", tc.role, got, tc.nominal)
		}
	}

	// NET_PAY_PAYABLE → 902, which is user-subdivided → the employee's own sub-account.
	emp := uuid.MustParse(newMemberUser(t, ts, devOrgID))
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(),
			`DELETE FROM categories WHERE organisation_id = $1 AND user_id = $2`, devOrgID, emp)
	})
	netSub, err := res.Resolve(ctx, ledger.RoleNetPayPayable, ledger.ResolveInput{OrganisationID: org, CompanyType: "limited", UserID: &emp})
	if err != nil {
		t.Fatalf("resolve NET_PAY_PAYABLE: %v", err)
	}
	assertSubAccount(t, ts, netSub, "902", emp)
}

// TestLedgerResolverRoleOverrides: gl_account_roles is overridable per org and per
// country, with precedence org → country → global default.
func TestLedgerResolverRoleOverrides(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	org := uuid.MustParse(devOrgID)
	otherOrg := uuid.New() // an org with NO override → sees the global default
	res := ledger.NewAccounts(dbcategories.New(ts.pool), dbledger.New(ts.pool), auth.New(ts.pool))
	q := dbledger.New(ts.pool)

	// An ORG override (DEBTORS → 682 for the dev org) and a COUNTRY override
	// (DEBTORS → 684 for France). The global default stays 681.
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO gl_account_roles (role, nominal_code, organisation_id, country_code) VALUES
		   ('DEBTORS','682',$1,NULL),
		   ('DEBTORS','684',NULL,'FR')`, devOrgID); err != nil {
		t.Fatalf("insert overrides: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(),
			`DELETE FROM gl_account_roles WHERE role='DEBTORS' AND (organisation_id=$1 OR country_code='FR')`, devOrgID)
	})

	getNominal := func(org uuid.UUID, country string) string {
		t.Helper()
		n, err := q.GetAccountRoleNominal(ctx, dbledger.GetAccountRoleNominalParams{
			Role:           "DEBTORS",
			OrganisationID: pgtype.UUID{Bytes: org, Valid: true},
			CountryCode:    pgtype.Text{String: country, Valid: country != ""},
			CompanyType:    "limited",
		})
		if err != nil {
			t.Fatalf("GetAccountRoleNominal(org=%s,country=%q): %v", org, country, err)
		}
		return n
	}

	// Precedence at the query layer.
	if got := getNominal(org, ""); got != "682" {
		t.Errorf("org override: DEBTORS for dev org = %q, want 682", got)
	}
	if got := getNominal(otherOrg, ""); got != "681" {
		t.Errorf("global default: DEBTORS for another org = %q, want 681", got)
	}
	if got := getNominal(otherOrg, "FR"); got != "684" {
		t.Errorf("country override: DEBTORS for FR = %q, want 684", got)
	}
	if got := getNominal(otherOrg, "GB"); got != "681" {
		t.Errorf("no GB override: DEBTORS for GB = %q, want global 681", got)
	}

	// End-to-end: the org override resolves to the org's 682 (Other Debtors) category.
	got, err := res.Resolve(ctx, ledger.RoleDebtors, ledger.ResolveInput{OrganisationID: org, CompanyType: "limited"})
	if err != nil {
		t.Fatalf("resolve DEBTORS with org override: %v", err)
	}
	if want := categoryIDByNominal(t, ts, "682"); got != want {
		t.Errorf("DEBTORS resolved to %s, want the org-override account 682 (%s)", got, want)
	}
}

// assertBankSubAccount checks a resolved id is a 750-x row owned by the bank account.
func assertBankSubAccount(t *testing.T, ts *testServer, id uuid.UUID, bank uuid.UUID) {
	t.Helper()
	var (
		nominal, accountType string
		parentCode           pgtype.Text
		bankID               pgtype.UUID
	)
	err := ts.pool.QueryRow(context.Background(),
		`SELECT nominal_code, account_type, parent_nominal_code, bank_account_id
		 FROM categories WHERE id = $1`, id).Scan(&nominal, &accountType, &parentCode, &bankID)
	if err != nil {
		t.Fatalf("read bank sub-account %s: %v", id, err)
	}
	if !strings.HasPrefix(nominal, "750-") {
		t.Errorf("bank sub-account nominal %q should start with 750-", nominal)
	}
	if parentCode.String != "750" {
		t.Errorf("bank sub-account parent_nominal_code = %q, want 750", parentCode.String)
	}
	if gotBank := uuid.UUID(bankID.Bytes); !bankID.Valid || gotBank != bank {
		t.Errorf("bank sub-account bank_account_id = %v (valid=%v), want %s", gotBank, bankID.Valid, bank)
	}
	if accountType != "BANK" {
		t.Errorf("bank sub-account account_type = %q, want BANK (inherited)", accountType)
	}
}
