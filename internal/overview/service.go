package overview

// service.go
// =============================================================================
// Service — the business logic for the "Overview" dashboard, a READ-ONLY,
// cross-domain summary (the FreeAgent-style landing page). Like internal/vat it
// holds the shared auth.Querier (for the membership/authorisation check) plus its
// own generated query set (db/overview) for the dashboard aggregations:
//
//   HTTP handler → Service (this file) → overview.Queries (db/overview) → Postgres
//
// The organisation in scope is ALWAYS the caller's, from the bearer token — so
// there is no id to pass and multi-tenant isolation is inherent. All reads are
// single-statement, so there is no pool/transaction to keep.
//
// Access rule: any ACTIVE member may read the dashboard (mirrors the VAT
// dashboard reads). Money is stored as integer pence (int64) and converted to
// decimal pound strings at this boundary via money.MinorToPounds — never float.
// =============================================================================

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	auth "github.com/operationfb/accounting-saas/db/auth"
	overviewdb "github.com/operationfb/accounting-saas/db/overview"
	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/money"
)

// Service holds the auth query set (for authorisation) and the overview read
// queries (db/overview). No pool: every dashboard read is a single statement.
type Service struct {
	authQueries auth.Querier
	queries     *overviewdb.Queries
}

// NewService is the constructor, called once in main.go. authQueries is the
// shared auth.Querier; queries is the overview read-query set (db/overview).
func NewService(authQueries auth.Querier, queries *overviewdb.Queries) *Service {
	return &Service{authQueries: authQueries, queries: queries}
}

// Cashflow returns the Cashflow card data: money in vs money out for each of the
// last 12 months, plus the window totals and the net Balance (incoming −
// outgoing). Any active member of the org may read it.
func (s *Service) Cashflow(ctx context.Context, userID, orgID uuid.UUID) (*CashflowResponse, error) {
	// Authorisation: caller must be an active member of the org (role unused —
	// reads are open to any member, like the VAT dashboard).
	if _, err := kernel.AuthorizeMember(ctx, s.authQueries, userID, orgID); err != nil {
		return nil, err
	}

	// The query yields exactly 12 month buckets (zero-filled), oldest→newest, with
	// outgoing already a positive magnitude. reference_date = today; only the date
	// part is used (the column is cast ::date in SQL).
	rows, err := s.queries.CashflowByMonth(ctx, overviewdb.CashflowByMonthParams{
		OrganisationID: orgID,
		ReferenceDate:  pgtype.Date{Time: time.Now(), Valid: true},
	})
	if err != nil {
		return nil, err
	}

	// Map each row to the DTO and accumulate the window totals in pence (int64) so
	// the totals stay exact — the pence→pounds conversion happens once, at the end.
	months := make([]CashflowMonth, 0, len(rows))
	var inTotal, outTotal int64
	for _, r := range rows {
		inTotal += r.IncomingMinor
		outTotal += r.OutgoingMinor
		months = append(months, CashflowMonth{
			Month:    r.Month.Time.Format("2006-01-02"),
			Incoming: money.MinorToPounds(r.IncomingMinor),
			Outgoing: money.MinorToPounds(r.OutgoingMinor),
		})
	}

	return &CashflowResponse{
		Months:   months,
		Incoming: money.MinorToPounds(inTotal),
		Outgoing: money.MinorToPounds(outTotal),
		Balance:  money.MinorToPounds(inTotal - outTotal), // net cashflow (signed)
	}, nil
}

// InvoiceTimeline returns the Invoice Timeline card data: SENT invoices' totals
// bucketed by month into Overdue / Due / Paid (the −8..+3 month window, matching
// the invoice list's status semantics), plus the headline Outstanding figure.
// Any active member of the org may read it.
func (s *Service) InvoiceTimeline(ctx context.Context, userID, orgID uuid.UUID) (*InvoiceTimelineResponse, error) {
	if _, err := kernel.AuthorizeMember(ctx, s.authQueries, userID, orgID); err != nil {
		return nil, err
	}

	// 12 zero-filled month buckets (oldest→newest), each split into the three
	// status series. reference_date = today; only the date part is used.
	rows, err := s.queries.InvoiceTimelineByMonth(ctx, overviewdb.InvoiceTimelineByMonthParams{
		OrganisationID: orgID,
		ReferenceDate:  pgtype.Date{Time: time.Now(), Valid: true},
	})
	if err != nil {
		return nil, err
	}

	// Outstanding is the full unpaid-SENT figure (not window-bound), summed in pence.
	outstanding, err := s.queries.OutstandingInvoiceTotal(ctx, orgID)
	if err != nil {
		return nil, err
	}

	months := make([]InvoiceTimelineMonth, 0, len(rows))
	for _, r := range rows {
		months = append(months, InvoiceTimelineMonth{
			Month:   r.Month.Time.Format("2006-01-02"),
			Overdue: money.MinorToPounds(r.OverdueMinor),
			Due:     money.MinorToPounds(r.DueMinor),
			Paid:    money.MinorToPounds(r.PaidMinor),
		})
	}

	return &InvoiceTimelineResponse{
		Months:      months,
		Outstanding: money.MinorToPounds(outstanding),
	}, nil
}

// Banking returns the Banking card data: the org's month-end TOTAL bank balance
// for each of the last 12 months (a cumulative series), plus the current total
// balance and the live-account count. Any active member of the org may read it.
func (s *Service) Banking(ctx context.Context, userID, orgID uuid.UUID) (*BankingResponse, error) {
	if _, err := kernel.AuthorizeMember(ctx, s.authQueries, userID, orgID); err != nil {
		return nil, err
	}

	// 12 month-end points (oldest→newest), each the running total balance at that
	// month end. reference_date = today; only the date part is used.
	rows, err := s.queries.BankBalanceByMonth(ctx, overviewdb.BankBalanceByMonthParams{
		OrganisationID: orgID,
		ReferenceDate:  pgtype.Date{Time: time.Now(), Valid: true},
	})
	if err != nil {
		return nil, err
	}

	// Headline: the current total balance (opening + all transactions) + the
	// live-account count — derived exactly like the Bank Accounts page.
	summary, err := s.queries.BankBalanceSummary(ctx, orgID)
	if err != nil {
		return nil, err
	}

	months := make([]BankBalancePoint, 0, len(rows))
	for _, r := range rows {
		months = append(months, BankBalancePoint{
			Month:   r.Month.Time.Format("2006-01-02"),
			Balance: money.MinorToPounds(r.BalanceMinor),
		})
	}

	return &BankingResponse{
		Months:   months,
		Balance:  money.MinorToPounds(summary.TotalBalanceMinor),
		Accounts: summary.AccountCount,
	}, nil
}
