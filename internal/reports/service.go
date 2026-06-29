package reports

// service.go
// =============================================================================
// Service — the business logic for the financial reports, a READ-ONLY surface
// over the posted general ledger (gl_journal_lines). Mirrors the thin
// service+handler shape of internal/overview:
//
//   HTTP handler → Service (this file) → ledger.Queries (db/ledger) → Postgres
//
// The organisation in scope is ALWAYS the caller's, from the bearer token — so
// there is no id to pass and multi-tenant isolation is inherent. Reads are
// single-statement, so there is no pool/transaction to keep.
//
// Access rule: any ACTIVE member may read a report (no role gate, like the
// overview dashboard). Balances are stored as integer pence (int64, signed: DR +,
// CR -) and converted to decimal pound strings at this boundary via
// money.MinorToPounds — never float.
// =============================================================================

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	auth "github.com/operationfb/accounting-saas/db/auth"
	categoriesdb "github.com/operationfb/accounting-saas/db/categories"
	ledgerdb "github.com/operationfb/accounting-saas/db/ledger"
	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/money"
)

// Service holds the auth query set (for the membership/authorisation check), the
// ledger read queries (db/ledger), and the categories query set (db/categories,
// for the Chart-of-Accounts lookups that back the Account Transactions report). No
// pool: every report read is a single statement.
type Service struct {
	authQueries auth.Querier
	ledger      ledgerdb.Querier
	cats        categoriesdb.Querier
}

// NewService is the constructor, called once in main.go. authQueries is the shared
// auth.Querier; ledger is the general-ledger query set (db/ledger); cats is the
// Chart-of-Accounts query set (db/categories).
func NewService(ledger ledgerdb.Querier, cats categoriesdb.Querier, authQueries auth.Querier) *Service {
	return &Service{authQueries: authQueries, ledger: ledger, cats: cats}
}

// TrialBalance returns the trial balance as of asOf: every CoA account with at
// least one journal line on or before that date, split into Debit/Credit columns,
// plus the balancing totals. Any active member of the org may read it.
func (s *Service) TrialBalance(ctx context.Context, userID, orgID uuid.UUID, asOf time.Time) (*TrialBalanceResponse, error) {
	// Authorisation: caller must be an active member of the org (role unused —
	// reports are open to any member, like the overview dashboard).
	if _, err := kernel.AuthorizeMember(ctx, s.authQueries, userID, orgID); err != nil {
		return nil, err
	}

	// The org's base (native) currency, shown as the report's currency label. The
	// ledger stores base_amount_minor in this currency (iteration 1 assumes a
	// single base currency for the org).
	org, err := s.authQueries.GetOrganisation(ctx, orgID)
	if err != nil {
		return nil, err
	}

	rows, err := s.ledger.GetTrialBalance(ctx, ledgerdb.GetTrialBalanceParams{
		OrganisationID: orgID,
		AsOfDate:       pgtype.Date{Time: asOf, Valid: true},
	})
	if err != nil {
		return nil, err
	}

	// Accumulate the totals in pence (int64) so they stay exact; the pence→pounds
	// conversion happens once per value, at the boundary.
	out := make([]TrialBalanceRow, 0, len(rows))
	var totalDebitMinor, totalCreditMinor int64
	for _, r := range rows {
		row := TrialBalanceRow{
			NominalCode: r.NominalCode,
			Name:        r.Name,
			AccountType: r.AccountType,
		}
		// Signed convention: DR is +, CR is -. A zero balance lands in the Debit
		// column (the report shows "0.00" there for accounts that net to zero).
		if r.BalanceMinor >= 0 {
			row.Debit = money.MinorToPounds(r.BalanceMinor)
			totalDebitMinor += r.BalanceMinor
		} else {
			row.Credit = money.MinorToPounds(-r.BalanceMinor)
			totalCreditMinor += -r.BalanceMinor
		}
		out = append(out, row)
	}

	return &TrialBalanceResponse{
		AsOfDate:    asOf.Format("2006-01-02"),
		Currency:    org.NativeCurrency,
		Rows:        out,
		TotalDebit:  money.MinorToPounds(totalDebitMinor),
		TotalCredit: money.MinorToPounds(totalCreditMinor),
	}, nil
}

// Accounts returns the org's active Chart-of-Accounts accounts (by nominal code) —
// the source for the Account Transactions report's account-picker dropdown. Any
// active member may read.
func (s *Service) Accounts(ctx context.Context, userID, orgID uuid.UUID) ([]AccountSummary, error) {
	if _, err := kernel.AuthorizeMember(ctx, s.authQueries, userID, orgID); err != nil {
		return nil, err
	}

	cats, err := s.cats.ListCategories(ctx, orgID)
	if err != nil {
		return nil, err
	}

	out := make([]AccountSummary, 0, len(cats))
	for _, c := range cats {
		out = append(out, AccountSummary{
			NominalCode: c.NominalCode,
			Name:        c.Name,
			AccountType: c.AccountType,
		})
	}
	return out, nil
}

// AccountTransactions returns the general-ledger lines posted to one account (by
// nominal code) over a date range: from is the optional lower bound (nil = open),
// to is the inclusive upper bound. Each line is split into Debit/Credit by the sign
// of base_amount_minor, with column totals. Any active member may read.
func (s *Service) AccountTransactions(ctx context.Context, userID, orgID uuid.UUID, nominal string, from *time.Time, to time.Time) (*AccountTransactionsResponse, error) {
	if _, err := kernel.AuthorizeMember(ctx, s.authQueries, userID, orgID); err != nil {
		return nil, err
	}

	// Resolve the account for the report header — and to 404 on an unknown/inactive
	// nominal code before running the (otherwise empty) lines query.
	cat, err := s.cats.GetCategoryByNominal(ctx, categoriesdb.GetCategoryByNominalParams{
		OrganisationID: orgID,
		NominalCode:    nominal,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, kernel.ErrNotFound("account", nominal)
		}
		return nil, err
	}

	org, err := s.authQueries.GetOrganisation(ctx, orgID)
	if err != nil {
		return nil, err
	}

	// from is optional (open lower bound → pgtype.Date{Valid:false}); to is inclusive.
	fromParam := pgtype.Date{}
	fromLabel := ""
	if from != nil {
		fromParam = pgtype.Date{Time: *from, Valid: true}
		fromLabel = from.Format("2006-01-02")
	}

	rows, err := s.ledger.GetAccountTransactions(ctx, ledgerdb.GetAccountTransactionsParams{
		OrganisationID: orgID,
		NominalCode:    nominal,
		FromDate:       fromParam,
		ToDate:         pgtype.Date{Time: to, Valid: true},
	})
	if err != nil {
		return nil, err
	}

	out := make([]AccountTransactionRow, 0, len(rows))
	var totalDebitMinor, totalCreditMinor int64
	for _, r := range rows {
		row := AccountTransactionRow{
			Date:        r.EntryDate.Time.Format("2006-01-02"),
			Description: describeLine(r),
			SourceType:  r.SourceType,
		}
		if r.SourceID.Valid {
			row.SourceID = uuid.UUID(r.SourceID.Bytes).String()
		}
		// Signed convention: DR is +, CR is -. Zero lands in the Debit column.
		if r.BaseAmountMinor >= 0 {
			row.Debit = money.MinorToPounds(r.BaseAmountMinor)
			totalDebitMinor += r.BaseAmountMinor
		} else {
			row.Credit = money.MinorToPounds(-r.BaseAmountMinor)
			totalCreditMinor += -r.BaseAmountMinor
		}
		out = append(out, row)
	}

	return &AccountTransactionsResponse{
		NominalCode: cat.NominalCode,
		Name:        cat.Name,
		AccountType: cat.AccountType,
		Currency:    org.NativeCurrency,
		FromDate:    fromLabel,
		ToDate:      to.Format("2006-01-02"),
		Rows:        out,
		TotalDebit:  money.MinorToPounds(totalDebitMinor),
		TotalCredit: money.MinorToPounds(totalCreditMinor),
	}, nil
}

// describeLine picks the most meaningful label for a transaction's Description: the
// entry narrative (e.g. "Invoice 002"), else the line description, else the raw
// source_type as a last resort.
func describeLine(r ledgerdb.GetAccountTransactionsRow) string {
	if r.Narrative.Valid && r.Narrative.String != "" {
		return r.Narrative.String
	}
	if r.Description.Valid && r.Description.String != "" {
		return r.Description.String
	}
	return r.SourceType
}
