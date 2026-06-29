package ledger

// accounts.go
// =============================================================================
// The general-ledger ACCOUNT RESOLVER: turns a gl_posting_rules.account_role into
// a concrete categories row id, org-scoped, at post time.
//
// Three classes of role (see ledger_schema.sql / gl_account_roles):
//   1. PASS-THROUGH  (EXPLANATION_CATEGORY, SOURCE_CATEGORY) — the category id is
//      already on the source row; we just load it.
//   2. FIXED CONTROL (DEBTORS, CREDITORS, VAT_CONTROL, SALES_DEFAULT, OPENING_EQUITY,
//      SUSPENSE, USER_ACCOUNT) — gl_account_roles maps role→nominal_code, looked up
//      in the caller's org categories.
//   3. ENTITY-DERIVED bank roles (BANK, TRANSFER_*_BANK) — resolve from the event's
//      bank account; NOT YET SUPPORTED (per-bank-account ledger accounts land with
//      the bank sub-account work).
//
// PER-USER SUB-ACCOUNTS: whichever class a role lands on, if the resolved account is
// is_user_subdivided AND the event links a user, we expand it to that user's
// sub-account row (FreeAgent's 908-1 / 907-2 per director), creating it lazily. This
// is what makes "Dividend to director A vs B" post to distinct ledger accounts.
//
// No transaction is threaded yet (there is no poster consuming this). The lazy
// sub-account create is idempotent via idx_categories_user_subaccount; the poster
// iteration will pass a tx-bound querier so the create commits with the journal entry.
// =============================================================================

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/operationfb/accounting-saas/db/auth"
	"github.com/operationfb/accounting-saas/db/categories"
	ledgerdb "github.com/operationfb/accounting-saas/db/ledger"
)

// account_role values (mirrors the CHECK in gl_posting_rules.account_role).
const (
	RoleBank                = "BANK"
	RoleDebtors             = "DEBTORS"
	RoleCreditors           = "CREDITORS"
	RoleVATControl          = "VAT_CONTROL"
	RoleUserAccount         = "USER_ACCOUNT"
	RoleOpeningEquity       = "OPENING_EQUITY"
	RoleExplanationCategory = "EXPLANATION_CATEGORY"
	RoleSourceCategory      = "SOURCE_CATEGORY"
	RoleSalesDefault        = "SALES_DEFAULT"
	RoleTransferSourceBank  = "TRANSFER_SOURCE_BANK"
	RoleTransferDestBank    = "TRANSFER_DEST_BANK"
	RoleSuspense            = "SUSPENSE"

	// Payroll accrual (PAYROLL_COMPLETED). All resolve via gl_account_roles like the
	// other fixed control roles; NET_PAY_PAYABLE → 902 then user-subdivides per payslip.
	RolePayrollGrossExpense           = "PAYROLL_GROSS_EXPENSE"
	RolePayrollEmployerNIExpense      = "PAYROLL_EMPLOYER_NI_EXPENSE"
	RolePayrollEmployerPensionExpense = "PAYROLL_EMPLOYER_PENSION_EXPENSE"
	RolePAYENILiability               = "PAYE_NI_LIABILITY"
	RolePensionLiability              = "PENSION_LIABILITY"
	RoleStudentLoanLiability          = "STUDENT_LOAN_LIABILITY"
	RoleNetPayPayable                 = "NET_PAY_PAYABLE"
	RoleOtherPayrollDeductions        = "OTHER_PAYROLL_DEDUCTIONS"

	// Director variants of the three employer-cost expense legs (staff use the plain
	// PAYROLL_*_EXPENSE roles above). The rule's employee_filter selects which leg fires.
	RolePayrollDirectorGrossExpense           = "PAYROLL_DIRECTOR_GROSS_EXPENSE"
	RolePayrollDirectorEmployerNIExpense      = "PAYROLL_DIRECTOR_EMPLOYER_NI_EXPENSE"
	RolePayrollDirectorEmployerPensionExpense = "PAYROLL_DIRECTOR_EMPLOYER_PENSION_EXPENSE"
)

// ErrChartNotProvisioned is returned when a control account's nominal isn't in the
// org's chart of accounts — i.e. the org hasn't been provisioned with a CoA, so it
// cannot have a general ledger. Source-service GL hooks treat it as "skip posting"
// rather than failing the business operation. (Per-org CoA provisioning is a separate
// piece of work; until then only seeded orgs post to the GL.)
var ErrChartNotProvisioned = errors.New("ledger: organisation has no chart of accounts")

// Accounts resolves account roles to categories rows. It reads (and, for user
// sub-accounts, writes) the per-org categories table, the global gl_account_roles
// map, and users (to label a new sub-account).
type Accounts struct {
	cats  categories.Querier
	roles ledgerdb.Querier
	users auth.Querier
}

// NewAccounts wires the resolver. The queriers are the generated interfaces, so a
// future poster can hand in tx-bound ones.
func NewAccounts(cats categories.Querier, roles ledgerdb.Querier, users auth.Querier) *Accounts {
	return &Accounts{cats: cats, roles: roles, users: users}
}

// ResolveInput carries everything the resolver needs from an economic event to turn
// a role into an account: the org, its company_type (for role branching), the
// already-picked category (for the pass-through roles), and the linked user (for
// USER_ACCOUNT and for per-user sub-account expansion).
type ResolveInput struct {
	OrganisationID        uuid.UUID
	CompanyType           string     // organisations.company_type ('limited', …); '' falls back to the 'ALL' rule
	CountryCode           string     // organisations.country_code ('GB', …); '' matches only the country-agnostic role rows
	PickedCategoryID      *uuid.UUID // EXPLANATION_CATEGORY / SOURCE_CATEGORY: the category on the source row
	UserID                *uuid.UUID // claimant / paid_user — drives USER_ACCOUNT + sub-account expansion
	BankAccountID         *uuid.UUID // BANK: the transaction's own bank account (→ its 750-x)
	TransferBankAccountID *uuid.UUID // TRANSFER_*_BANK: the other side of a transfer (→ its 750-x)
}

// Resolve returns the categories.id a role posts to for this event.
func (a *Accounts) Resolve(ctx context.Context, role string, in ResolveInput) (uuid.UUID, error) {
	switch role {
	case RoleExplanationCategory, RoleSourceCategory:
		if in.PickedCategoryID == nil {
			return uuid.Nil, fmt.Errorf("ledger: role %s requires a picked category but none was provided", role)
		}
		cat, err := a.cats.GetCategory(ctx, categories.GetCategoryParams{
			ID:             *in.PickedCategoryID,
			OrganisationID: in.OrganisationID,
		})
		if err != nil {
			return uuid.Nil, fmt.Errorf("ledger: load picked category for role %s: %w", role, err)
		}
		return a.maybeUserSubAccount(ctx, in, cat)

	case RoleDebtors, RoleCreditors, RoleVATControl, RoleSalesDefault,
		RoleOpeningEquity, RoleSuspense, RoleUserAccount,
		RolePayrollGrossExpense, RolePayrollEmployerNIExpense, RolePayrollEmployerPensionExpense,
		RolePayrollDirectorGrossExpense, RolePayrollDirectorEmployerNIExpense, RolePayrollDirectorEmployerPensionExpense,
		RolePAYENILiability, RolePensionLiability, RoleStudentLoanLiability,
		RoleNetPayPayable, RoleOtherPayrollDeductions:
		cat, err := a.fixedRoleCategory(ctx, role, in)
		if err != nil {
			return uuid.Nil, err
		}
		// NET_PAY_PAYABLE → 902, which is user-subdivided, so this expands to the
		// payslip employee's sub-account; the other control roles return the parent.
		return a.maybeUserSubAccount(ctx, in, cat)

	case RoleBank:
		// The transaction's own bank account → its 750-x ledger account.
		return a.bankSubAccount(ctx, in, in.BankAccountID)

	case RoleTransferSourceBank, RoleTransferDestBank:
		// The OTHER side of a transfer → that account's 750-x.
		return a.bankSubAccount(ctx, in, in.TransferBankAccountID)

	default:
		return uuid.Nil, fmt.Errorf("ledger: unknown account_role %q", role)
	}
}

// maybeUserSubAccount returns the parent's id unless the parent is_user_subdivided AND
// the event links a user — in which case it returns that user's sub-account, creating
// it lazily (idempotent via idx_categories_user_subaccount).
func (a *Accounts) maybeUserSubAccount(ctx context.Context, in ResolveInput, parent categories.Category) (uuid.UUID, error) {
	if !parent.IsUserSubdivided || in.UserID == nil {
		return parent.ID, nil
	}

	uid := pgtype.UUID{Bytes: *in.UserID, Valid: true}
	parentCode := pgtype.Text{String: parent.NominalCode, Valid: true}
	lookup := categories.GetUserSubAccountParams{
		OrganisationID:    in.OrganisationID,
		ParentNominalCode: parentCode,
		UserID:            uid,
	}

	// Already exists?
	if sub, err := a.cats.GetUserSubAccount(ctx, lookup); err == nil {
		return sub.ID, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("ledger: look up user sub-account of %s: %w", parent.NominalCode, err)
	}

	// Create it: next '-N' suffix, inheriting the parent's account_type / api_group.
	suffix, err := a.cats.NextSubAccountSuffix(ctx, categories.NextSubAccountSuffixParams{
		OrganisationID:    in.OrganisationID,
		ParentNominalCode: parentCode,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("ledger: next sub-account suffix for %s: %w", parent.NominalCode, err)
	}
	created, err := a.cats.CreateUserSubAccount(ctx, categories.CreateUserSubAccountParams{
		OrganisationID:    in.OrganisationID,
		NominalCode:       fmt.Sprintf("%s-%d", parent.NominalCode, suffix),
		Name:              parent.Name + " - " + a.userLabel(ctx, *in.UserID),
		AccountType:       parent.AccountType,
		ApiGroup:          parent.ApiGroup,
		ParentNominalCode: parentCode,
		UserID:            uid,
	})
	if err != nil {
		// A concurrent post may have created it first (the unique index fired). Re-read.
		if sub, gerr := a.cats.GetUserSubAccount(ctx, lookup); gerr == nil {
			return sub.ID, nil
		}
		return uuid.Nil, fmt.Errorf("ledger: create user sub-account of %s: %w", parent.NominalCode, err)
	}
	return created.ID, nil
}

// fixedRoleCategory resolves a fixed control role to its org category via the
// gl_account_roles nominal map (shared by all the control + payroll roles).
func (a *Accounts) fixedRoleCategory(ctx context.Context, role string, in ResolveInput) (categories.Category, error) {
	nominal, err := a.roles.GetAccountRoleNominal(ctx, ledgerdb.GetAccountRoleNominalParams{
		Role:           role,
		OrganisationID: pgtype.UUID{Bytes: in.OrganisationID, Valid: true},
		CountryCode:    pgtype.Text{String: in.CountryCode, Valid: in.CountryCode != ""},
		CompanyType:    in.CompanyType,
	})
	if err != nil {
		return categories.Category{}, fmt.Errorf("ledger: no gl_account_roles mapping for role %s (company_type %q): %w", role, in.CompanyType, err)
	}
	cat, err := a.cats.GetCategoryByNominal(ctx, categories.GetCategoryByNominalParams{
		OrganisationID: in.OrganisationID,
		NominalCode:    nominal,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The control nominal isn't in the org's chart — the org has no CoA, so it
			// can't have a GL. Surface the typed sentinel so source hooks skip posting.
			return categories.Category{}, fmt.Errorf("%w: role %s → nominal %s", ErrChartNotProvisioned, role, nominal)
		}
		return categories.Category{}, fmt.Errorf("ledger: role %s → nominal %s lookup failed: %w", role, nominal, err)
	}
	return cat, nil
}

// bankSubAccount resolves the BANK / TRANSFER_*_BANK roles: the 750 parent (from the
// role map) expanded to the given bank account's own 750-x sub-account.
func (a *Accounts) bankSubAccount(ctx context.Context, in ResolveInput, bankID *uuid.UUID) (uuid.UUID, error) {
	if bankID == nil {
		return uuid.Nil, fmt.Errorf("ledger: a bank role requires a bank account but none was provided")
	}
	parent, err := a.fixedRoleCategory(ctx, RoleBank, in)
	if err != nil {
		return uuid.Nil, err
	}

	bid := pgtype.UUID{Bytes: *bankID, Valid: true}
	parentCode := pgtype.Text{String: parent.NominalCode, Valid: true}
	lookup := categories.GetBankSubAccountParams{
		OrganisationID:    in.OrganisationID,
		ParentNominalCode: parentCode,
		BankAccountID:     bid,
	}

	// Already exists? (A bank account ALWAYS gets its own account — no flag needed.)
	if sub, err := a.cats.GetBankSubAccount(ctx, lookup); err == nil {
		return sub.ID, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("ledger: look up bank sub-account of %s: %w", parent.NominalCode, err)
	}

	suffix, err := a.cats.NextSubAccountSuffix(ctx, categories.NextSubAccountSuffixParams{
		OrganisationID:    in.OrganisationID,
		ParentNominalCode: parentCode,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("ledger: next sub-account suffix for %s: %w", parent.NominalCode, err)
	}
	created, err := a.cats.CreateBankSubAccount(ctx, categories.CreateBankSubAccountParams{
		OrganisationID:    in.OrganisationID,
		NominalCode:       fmt.Sprintf("%s-%d", parent.NominalCode, suffix),
		Name:              fmt.Sprintf("%s %d", parent.Name, suffix), // "Bank Account 1" (cosmetic; the link is the identity)
		AccountType:       parent.AccountType,
		ApiGroup:          parent.ApiGroup,
		ParentNominalCode: parentCode,
		BankAccountID:     bid,
	})
	if err != nil {
		// A concurrent post may have created it first (the unique index fired). Re-read.
		if sub, gerr := a.cats.GetBankSubAccount(ctx, lookup); gerr == nil {
			return sub.ID, nil
		}
		return uuid.Nil, fmt.Errorf("ledger: create bank sub-account of %s: %w", parent.NominalCode, err)
	}
	return created.ID, nil
}

// userLabel is the display name for a sub-account ("Dividend - Alice Smith"); falls
// back to the user id if the user can't be read or has no name.
func (a *Accounts) userLabel(ctx context.Context, userID uuid.UUID) string {
	u, err := a.users.GetUser(ctx, userID)
	if err != nil {
		return userID.String()
	}
	if name := strings.TrimSpace(u.FirstName + " " + u.LastName); name != "" {
		return name
	}
	return userID.String()
}
