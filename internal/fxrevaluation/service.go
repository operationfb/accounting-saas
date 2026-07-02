package fxrevaluation

// service.go
// =============================================================================
// Periodic UNREALISED FX revaluation of open foreign-currency invoices (Phase 3).
//
// A foreign invoice posts its receivable (681 Trade Debtors) at the INVOICE-DATE
// rate and never re-translates it, so the Trial Balance would show an open EUR/USD
// debtor at the booking rate forever. This service retranslates the OUTSTANDING
// portion of each open foreign invoice to today's stored rate and posts the
// difference as an unrealised gain/loss journal (DR/CR 681 vs 391), so the books —
// and the Trial Balance — reflect today's value. Income (001) stays at the booking
// rate; only the monetary receivable is revalued.
//
// Two entry points:
//   - RunRevaluation(asOf)            — the daily job (chained after the FX-rate
//                                       refresh): revalue EVERY org's open foreign
//                                       invoices, one transaction per invoice.
//   - OnInvoiceReceiptChanged(...)    — called by the banking explain flow inside
//                                       the receipt transaction, so 391/681 are never
//                                       stale after a payment: partial ⇒ re-revalue the
//                                       reduced due; full settlement ⇒ crystallise with a
//                                       delta-to-zero append (target U = 0).
//
// No double-count with realised (390): 391 (unrealised) and 390 (realised) are
// SEPARATE nominals. The receipt crystallises each paid portion's realised gain in
// 390; 391 only ever carries the still-open portion and is walked back to zero once
// settled — by ITS OWN balance, never the 390 figure. INCREMENTAL DELTA model
// (append-only): each run reads the invoice's current 391 balance and posts ONLY the
// movement to today's target (ΔU = targetU − U_booked). No reversals, and a zero delta
// posts nothing — so re-runs never double up and an unchanged rate is a no-op (no churn).
// =============================================================================

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	auth "github.com/operationfb/accounting-saas/db/auth"
	invoicesdb "github.com/operationfb/accounting-saas/db/invoices"
	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/internal/ledger"
	"github.com/operationfb/accounting-saas/money"
)

const (
	// The synthetic ledger event + source for an unrealised-revaluation entry. The
	// posting rules (gl_posting_rules) sign-split on the FX_GAIN/FX_LOSS bases.
	eventInvoiceRevaluation  = "INVOICE_REVALUATION"
	sourceInvoiceRevaluation = "INVOICE_REVALUATION"
	basisFXGain              = "FX_GAIN"
	basisFXLoss              = "FX_LOSS"
	statusSent               = "SENT"
)

// RateLookup is the read seam over the exchange-rate service (internal/fxrates) —
// the same shape the invoices service uses. Given a currency and a date it returns
// the home-per-unit rate at or before that date.
type RateLookup interface {
	RateOnOrBefore(ctx context.Context, currency string, on time.Time) (decimal.Decimal, bool, error)
}

// Service holds the pool (for the daily job's per-invoice transactions), the invoices
// query set (open-invoice list + currency lookups), the auth queries (org home
// currency / company type / country), the ledger poster, and the rate seam.
type Service struct {
	pool     *pgxpool.Pool
	invoices *invoicesdb.Queries
	auth     auth.Querier
	poster   ledger.Poster
	rates    RateLookup
}

// NewService is the constructor, called once in main.go. A nil rates seam (FX module
// unwired) makes every method a no-op.
func NewService(pool *pgxpool.Pool, invoices *invoicesdb.Queries, authQ auth.Querier, poster ledger.Poster, rates RateLookup) *Service {
	return &Service{pool: pool, invoices: invoices, auth: authQ, poster: poster, rates: rates}
}

// openInput is the per-invoice data the core revaluation needs, built either from the
// cross-org list row (daily job) or from a freshly-loaded invoice (banking hook).
type openInput struct {
	id, orgID                         uuid.UUID
	reference, currency, homeCurrency string
	companyType, countryCode          string
	dueMinor, totalMinor, nativeTotal int64
}

// RunRevaluation revalues every org's OPEN foreign invoices as of asOf. One
// transaction per invoice, so a bad/missing rate on one invoice skips just that one
// (logged) rather than failing the whole run. Chained after the daily FX-rate refresh.
func (s *Service) RunRevaluation(ctx context.Context, asOf time.Time) error {
	if s.rates == nil {
		return nil
	}
	rows, err := s.invoices.ListOpenForeignInvoicesAllOrgs(ctx)
	if err != nil {
		return kernel.ErrInternal(err)
	}
	for _, r := range rows {
		in := openInput{
			id:           r.Invoice.ID,
			orgID:        r.Invoice.OrganisationID,
			reference:    r.Invoice.Reference.String,
			currency:     r.Invoice.Currency,
			homeCurrency: r.NativeCurrency,
			companyType:  r.CompanyType.String,
			countryCode:  r.CountryCode,
			dueMinor:     r.Invoice.DueValueMinor.Int64,
			totalMinor:   r.Invoice.TotalValueMinor,
			nativeTotal:  r.Invoice.NativeTotalValueMinor,
		}
		if err := kernel.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
			// The daily job has no acting user — attribute the entry to the system (Nil).
			return s.revalueOpen(ctx, tx, in, asOf, uuid.Nil)
		}); err != nil {
			// Best-effort: log and carry on; the next run (or a manual re-run) replaces it.
			slog.Error("fx revaluation failed for invoice; skipping", "invoice_id", in.id, "err", err)
		}
	}
	return nil
}

// OnInvoiceReceiptChanged keeps an invoice's unrealised revaluation correct in the
// SAME transaction as a receipt, so 391/681 are never stale after a payment. Foreign,
// SENT invoices only: a partial receipt re-revalues the reduced due; a full settlement
// (due → 0) crystallises with a delta-to-zero append (the realised gain already sits in
// 390 from the receipt). A no-op for home-currency or non-SENT invoices.
func (s *Service) OnInvoiceReceiptChanged(ctx context.Context, tx pgx.Tx, orgID, invoiceID uuid.UUID, asOf time.Time, createdBy uuid.UUID) error {
	if s.rates == nil {
		return nil
	}
	q := s.invoices.WithTx(tx)
	inv, err := q.GetInvoice(ctx, invoicesdb.GetInvoiceParams{ID: invoiceID, OrganisationID: orgID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return kernel.ErrInternal(err)
	}
	org, err := s.auth.GetOrganisation(ctx, orgID)
	if err != nil {
		return kernel.ErrInternal(err)
	}
	if inv.Currency == org.NativeCurrency || inv.Status != statusSent {
		return nil // home-currency or no-longer-SENT: nothing to revalue here
	}

	in := openInput{
		id:           inv.ID,
		orgID:        orgID,
		reference:    inv.Reference.String,
		currency:     inv.Currency,
		homeCurrency: org.NativeCurrency,
		companyType:  org.CompanyType.String,
		countryCode:  org.CountryCode,
		dueMinor:     inv.DueValueMinor.Int64,
		totalMinor:   inv.TotalValueMinor,
		nativeTotal:  inv.NativeTotalValueMinor,
	}

	if inv.DueValueMinor.Int64 <= 0 {
		// Fully settled → crystallise: walk the unrealised 391/681 back to zero with a
		// delta-to-zero append (target U = 0), so the whole standing balance is removed in one
		// entry — no rate lookup, no reversal, and correct for any number of prior runs. The
		// realised gain already sits in 390 from the receipt.
		return s.applyRevaluationDelta(ctx, tx, in, 0, asOf, createdBy)
	}

	// Partial settlement → re-revalue the remaining open portion.
	return s.revalueOpen(ctx, tx, in, asOf, createdBy)
}

// revalueOpen computes today's TARGET unrealised U on the invoice's CURRENT open (due) portion,
// then applies the delta. Used by the daily job and the partial-payment re-revaluation.
func (s *Service) revalueOpen(ctx context.Context, tx pgx.Tx, in openInput, asOf time.Time, createdBy uuid.UUID) error {
	if in.dueMinor <= 0 || in.totalMinor <= 0 {
		return nil
	}
	rate, ok, err := s.rates.RateOnOrBefore(ctx, in.currency, asOf)
	if err != nil {
		return err
	}
	if !ok {
		return nil // no stored rate for this currency/date — leave the booking value as-is
	}

	q := s.invoices.WithTx(tx)
	foreignExp, err := currencyExp(ctx, q, in.currency)
	if err != nil {
		return err
	}
	homeExp, err := currencyExp(ctx, q, in.homeCurrency)
	if err != nil {
		return err
	}

	// home value of the OUTSTANDING foreign amount, at the booking rate vs today's rate.
	homeAtBooking := money.Apportion(in.nativeTotal, in.dueMinor, in.totalMinor)
	homeAtToday := money.ConvertMinor(in.dueMinor, foreignExp, homeExp, rate)
	targetU := homeAtToday - homeAtBooking
	return s.applyRevaluationDelta(ctx, tx, in, targetU, asOf, createdBy)
}

// nominalFXUnrealised is the FX-unrealised gain/loss control account (391). BOTH the gain and
// loss revaluation legs post their FX side here, so an invoice's net 391 base across its
// INVOICE_REVALUATION entries is exactly −(unrealised U already booked). That's the anchor the
// delta model reads to decide how much still to post.
const nominalFXUnrealised = "391"

// applyRevaluationDelta walks an invoice's unrealised revaluation to targetU by posting ONLY the
// movement since the last run — append-only, never a reversal, so re-runs never double up and an
// unchanged rate posts nothing (no churn). It:
//  1. LOCKS the source, THEN reads its 391 balance, THEN appends — so two concurrent
//     revaluations of the same invoice can't both read the same stale balance and double-post.
//  2. bal391 = −(U already booked), so ΔU = targetU + bal391 = targetU − U_booked.
//  3. A zero delta posts nothing; otherwise the DELTA is sign-split across FX_GAIN/FX_LOSS.
//
// Full settlement passes targetU = 0, which walks the whole standing 391/681 balance back to
// zero in a single append (the realised gain already lives in 390 from the receipt).
func (s *Service) applyRevaluationDelta(ctx context.Context, tx pgx.Tx, in openInput, targetU int64, asOf time.Time, createdBy uuid.UUID) error {
	if err := s.poster.LockSource(ctx, tx, in.orgID, sourceInvoiceRevaluation, in.id); err != nil {
		return err
	}
	bal391, err := s.poster.SourceAccountBase(ctx, tx, in.orgID, sourceInvoiceRevaluation, in.id, nominalFXUnrealised)
	if err != nil {
		return err
	}
	delta := targetU + bal391
	if delta == 0 {
		return nil // no movement → post nothing (append-only, no churn)
	}

	// Sign-split the DELTA: the poster drops the zero leg, so exactly two legs fire. All home-ccy.
	var gain, loss int64
	if delta > 0 {
		gain = delta
	} else {
		loss = -delta
	}
	return s.poster.AppendEntry(ctx, tx, ledger.EntryContext{
		OrganisationID: in.orgID,
		CompanyType:    in.companyType,
		CountryCode:    in.countryCode,
		BaseCurrency:   in.homeCurrency,
		TxnCurrency:    in.homeCurrency,
		EventCode:      eventInvoiceRevaluation,
		SourceType:     sourceInvoiceRevaluation,
		SourceID:       in.id,
		EntryDate:      pgtype.Date{Time: asOf, Valid: true},
		Narrative:      "FX revaluation " + in.reference,
		CreatedBy:      createdBy,
		Amounts: map[string]ledger.Amount{
			basisFXGain: {Txn: gain, Base: gain},
			basisFXLoss: {Txn: loss, Base: loss},
		},
	})
}

// currencyExp returns a currency's minor_unit (decimal places) for money.ConvertMinor.
func currencyExp(ctx context.Context, q *invoicesdb.Queries, code string) (int, error) {
	c, err := q.GetCurrency(ctx, code)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, kernel.ErrValidation("unknown currency "+code, nil)
		}
		return 0, kernel.ErrInternal(err)
	}
	return int(c.MinorUnit), nil
}
