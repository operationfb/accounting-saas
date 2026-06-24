package banking

// explain.go
// =============================================================================
// "Explaining" a bank transaction — the reconcile WRITE path. An explanation
// assigns an accounting treatment (a type + category/entity + VAT) to all or PART
// of a line; the recompute trigger keeps the line's status + unexplained_amount in
// sync. A line can be SPLIT across several explanations summing to its amount.
//
// Type → what to supply (the type's entity_link, validated here):
//   NONE / CAPITAL_ASSET — a category OFFERED for the type (Payment, Sales, …)
//   BANK_ACCOUNT         — another bank account in the org (Transfer)
//   USER                 — an active member + the user-account category (Money to/from User)
// The 6 future-entity types (Bill/Invoice/Credit Note/HP) are NOT yet supported
// (categories.SupportedEntityLinks is the shared whitelist).
//
// MONEY: the portion amount crosses the API as a POSITIVE pound string; the service
// signs it to the line's direction (money-out line → negative gross). VAT is
// EXTRACTED from the portion gross (money.ComputeFixedVAT). Owner/admin only.
// =============================================================================

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	banking "github.com/operationfb/accounting-saas/db/banking"
	categoriesdb "github.com/operationfb/accounting-saas/db/categories"
	invoicesdb "github.com/operationfb/accounting-saas/db/invoices"
	categories "github.com/operationfb/accounting-saas/internal/categories"
	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/money"
)

// Local constants for the Invoice Receipt flow. typeInvoiceReceipt is the
// transaction_types code; invoiceStatusSent mirrors invoices.status = 'SENT' (only a
// sent invoice can be paid). Kept local so the banking package doesn't import the
// internal/invoices package just for two strings.
const (
	typeInvoiceReceipt = "INVOICE_RECEIPT"
	invoiceStatusSent  = "SENT"
)

// =============================================================================
// DTOs
// =============================================================================

// CreateExplanationRequest is the body for adding/editing one explanation (a whole
// line or a split portion). Amount is a POSITIVE pound string; the service signs it
// to the line's direction. Which of category/transfer/user is required depends on
// the chosen type's entity_link. Reused for create and update.
type CreateExplanationRequest struct {
	Type                  string  `json:"type" binding:"required"`
	Amount                string  `json:"amount" binding:"required"` // pounds, positive (this portion)
	CategoryID            *string `json:"category_id"`               // category types + user payments
	TransferBankAccountID *string `json:"transfer_bank_account_id"`  // transfers
	PaidUserID            *string `json:"paid_user_id"`              // user payments
	PaidInvoiceID         *string `json:"paid_invoice_id"`           // invoice receipts
	VATRateID             *string `json:"vat_rate_id"`               // optional
	VATAmount             *string `json:"vat_amount"`                // manual (non-fixed) rate only
	Description           *string `json:"description"`
	DatedOn               *string `json:"dated_on"` // YYYY-MM-DD, defaults to the transaction's date
}

// ExplanationResponse is one explanation in the API shape. Amount + VATValue are
// positive pound strings (the direction is the line's). The resolved display names
// come from the detailed JOIN query.
type ExplanationResponse struct {
	ID                    string  `json:"id"`
	Type                  string  `json:"type"`
	Amount                string  `json:"amount"`
	CategoryID            *string `json:"category_id,omitempty"`
	CategoryName          *string `json:"category_name,omitempty"`
	CategoryNominalCode   *string `json:"category_nominal_code,omitempty"`
	TransferBankAccountID *string `json:"transfer_bank_account_id,omitempty"`
	TransferAccountName   *string `json:"transfer_account_name,omitempty"`
	PaidUserID            *string `json:"paid_user_id,omitempty"`
	PaidUserName          *string `json:"paid_user_name,omitempty"`
	PaidInvoiceID         *string `json:"paid_invoice_id,omitempty"`
	InvoiceReference      *string `json:"invoice_reference,omitempty"`
	VATRateID             *string `json:"vat_rate_id,omitempty"`
	VATRate               *string `json:"vat_rate,omitempty"` // e.g. "20%"
	VATValue              string  `json:"vat_value"`          // pounds
	Description           *string `json:"description,omitempty"`
	DatedOn               string  `json:"dated_on"`
	MarkedForReview       bool    `json:"marked_for_review"`
}

// TransactionExplanationsResponse is the GET + every-mutation response: the line's
// recomputed reconcile state (from the trigger) plus its live explanations.
// UnexplainedAmount is the pounds remaining to explain.
type TransactionExplanationsResponse struct {
	TransactionID     string                 `json:"transaction_id"`
	Status            string                 `json:"status"`
	UnexplainedAmount string                 `json:"unexplained_amount"`
	Explanations      []*ExplanationResponse `json:"explanations"`
}

// =============================================================================
// READ
// =============================================================================

// ListExplanations returns a line's explanations + its reconcile state. Any active
// member may read (the explain panel needs it).
func (s *Service) ListExplanations(ctx context.Context, authUserID, authOrgID uuid.UUID, accountID, txnID string) (*TransactionExplanationsResponse, error) {
	accountUUID, txnUUID, err := parseAccountAndTxn(accountID, txnID)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorize(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	if _, err := s.loadTransactionForAccount(ctx, authOrgID, accountUUID, txnUUID); err != nil {
		return nil, err
	}
	return s.buildExplanationsResponse(ctx, authOrgID, txnUUID)
}

// =============================================================================
// MUTATIONS (owner/admin)
// =============================================================================

// CreateExplanation adds one explanation (a whole line or a split portion) and
// returns the line's refreshed reconcile state + explanations.
func (s *Service) CreateExplanation(ctx context.Context, authUserID, authOrgID uuid.UUID, accountID, txnID string, req CreateExplanationRequest) (*TransactionExplanationsResponse, error) {
	accountUUID, txnUUID, err := parseAccountAndTxn(accountID, txnID)
	if err != nil {
		return nil, err
	}
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	txn, err := s.loadTransactionForAccount(ctx, authOrgID, accountUUID, txnUUID)
	if err != nil {
		return nil, err
	}
	params, err := s.resolveExplanationFields(ctx, authUserID, authOrgID, accountUUID, txn, req, nil)
	if err != nil {
		return nil, err
	}
	// Write the explanation and (for an invoice receipt) re-sync the invoice's paid
	// value in ONE transaction, so invoices.paid_value_minor can never drift from the
	// explanations that drive it.
	err = kernel.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		qtx := s.queries.WithTx(tx)
		if _, err := qtx.CreateExplanation(ctx, params); err != nil {
			return kernel.ErrInternal(err)
		}
		return s.resyncInvoicePaid(ctx, qtx, s.invoiceQueries.WithTx(tx), authOrgID, params.PaidInvoiceID)
	})
	if err != nil {
		return nil, err
	}
	return s.buildExplanationsResponse(ctx, authOrgID, txnUUID)
}

// UpdateExplanation edits one explanation (re-categorise, fix the amount/VAT).
func (s *Service) UpdateExplanation(ctx context.Context, authUserID, authOrgID uuid.UUID, accountID, txnID, explID string, req CreateExplanationRequest) (*TransactionExplanationsResponse, error) {
	accountUUID, txnUUID, err := parseAccountAndTxn(accountID, txnID)
	if err != nil {
		return nil, err
	}
	explUUID, err := uuid.Parse(explID)
	if err != nil {
		return nil, kernel.ErrValidation("explanation id is not a valid UUID", err)
	}
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	txn, err := s.loadTransactionForAccount(ctx, authOrgID, accountUUID, txnUUID)
	if err != nil {
		return nil, err
	}
	existing, err := s.loadExplanationForTxn(ctx, authOrgID, txnUUID, explUUID)
	if err != nil {
		return nil, err
	}
	// Editing: existing is passed through so its old portion is "given back" before the
	// over-explain check AND the invoice overpayment cap.
	params, err := s.resolveExplanationFields(ctx, authUserID, authOrgID, accountUUID, txn, req, &existing)
	if err != nil {
		return nil, err
	}
	// Update the explanation and re-sync the affected invoice(s) in one transaction.
	// Re-pointing a receipt (or changing its type away from INVOICE_RECEIPT) must
	// recompute BOTH the new link and the old one, so paid moves correctly between them.
	err = kernel.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		qtx := s.queries.WithTx(tx)
		itx := s.invoiceQueries.WithTx(tx)
		if _, err := qtx.UpdateExplanation(ctx, banking.UpdateExplanationParams{
			ID:                    explUUID,
			OrganisationID:        authOrgID,
			DatedOn:               params.DatedOn,
			Description:           params.Description,
			Type:                  params.Type,
			GrossValueMinor:       params.GrossValueMinor,
			CategoryID:            params.CategoryID,
			SalesTaxStatus:        params.SalesTaxStatus,
			SalesTaxRateID:        params.SalesTaxRateID,
			SalesTaxValueMinor:    params.SalesTaxValueMinor,
			IsManualSalesTax:      params.IsManualSalesTax,
			EcStatus:              params.EcStatus,
			PlaceOfSupply:         params.PlaceOfSupply,
			TransferBankAccountID: params.TransferBankAccountID,
			PaidUserID:            params.PaidUserID,
			PaidInvoiceID:         params.PaidInvoiceID,
			MarkedForReview:       params.MarkedForReview,
			ChequeNumber:          params.ChequeNumber,
			ReceiptReference:      params.ReceiptReference,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return kernel.ErrNotFound("explanation", explID)
			}
			return kernel.ErrInternal(err)
		}
		if err := s.resyncInvoicePaid(ctx, qtx, itx, authOrgID, params.PaidInvoiceID); err != nil {
			return err
		}
		if existing.PaidInvoiceID.Valid && existing.PaidInvoiceID != params.PaidInvoiceID {
			if err := s.resyncInvoicePaid(ctx, qtx, itx, authOrgID, existing.PaidInvoiceID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.buildExplanationsResponse(ctx, authOrgID, txnUUID)
}

// DeleteExplanation removes one explanation (un-explain part of a line).
func (s *Service) DeleteExplanation(ctx context.Context, authUserID, authOrgID uuid.UUID, accountID, txnID, explID string) (*TransactionExplanationsResponse, error) {
	accountUUID, txnUUID, err := parseAccountAndTxn(accountID, txnID)
	if err != nil {
		return nil, err
	}
	explUUID, err := uuid.Parse(explID)
	if err != nil {
		return nil, kernel.ErrValidation("explanation id is not a valid UUID", err)
	}
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	if _, err := s.loadTransactionForAccount(ctx, authOrgID, accountUUID, txnUUID); err != nil {
		return nil, err
	}
	existing, err := s.loadExplanationForTxn(ctx, authOrgID, txnUUID, explUUID)
	if err != nil {
		return nil, err
	}
	// Soft-delete the explanation and, if it settled an invoice, re-sync that invoice's
	// paid value (it drops by this receipt's portion) in the same transaction.
	err = kernel.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		qtx := s.queries.WithTx(tx)
		if err := qtx.SoftDeleteExplanation(ctx, banking.SoftDeleteExplanationParams{ID: explUUID, OrganisationID: authOrgID}); err != nil {
			return kernel.ErrInternal(err)
		}
		return s.resyncInvoicePaid(ctx, qtx, s.invoiceQueries.WithTx(tx), authOrgID, existing.PaidInvoiceID)
	})
	if err != nil {
		return nil, err
	}
	return s.buildExplanationsResponse(ctx, authOrgID, txnUUID)
}

// =============================================================================
// VALIDATION / RESOLUTION
// =============================================================================

// resolveExplanationFields validates the request against the chosen type and the
// line, computes the signed gross + VAT, and returns the DB-ready insert params.
// existing is non-nil on UPDATE (the explanation being edited) — its portion is
// returned to the remaining pool before the over-explain check, and (for an invoice
// receipt) before the invoice's overpayment cap.
func (s *Service) resolveExplanationFields(ctx context.Context, authUserID, orgID, accountUUID uuid.UUID, txn banking.BankTransaction, req CreateExplanationRequest, existing *banking.BankTransactionExplanation) (banking.CreateExplanationParams, error) {
	var zero banking.CreateExplanationParams

	// 1. type: must exist, be supported in v1, and match the line's direction.
	typeCode := strings.TrimSpace(req.Type)
	tt, err := s.catQueries.GetTransactionType(ctx, typeCode)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, kernel.ErrValidation("unknown transaction type", nil)
		}
		return zero, kernel.ErrInternal(err)
	}
	if !categories.SupportedEntityLinks[tt.EntityLink] {
		return zero, kernel.ErrValidation("that transaction type isn't explainable yet", nil)
	}
	lineDir := "in"
	if txn.AmountMinor < 0 {
		lineDir = "out"
	}
	if tt.Direction != lineDir {
		return zero, kernel.ErrValidation("the chosen type is money-"+tt.Direction+", but this is a money-"+lineDir+" transaction", nil)
	}

	// 2. amount → signed gross (matches the line's direction).
	amtMinor, err := money.PoundsToMinor(strings.TrimSpace(req.Amount))
	if err != nil {
		return zero, kernel.ErrValidation("amount must be a valid amount", err)
	}
	if amtMinor <= 0 {
		return zero, kernel.ErrValidation("amount must be greater than zero", nil)
	}
	grossMinor := amtMinor
	if lineDir == "out" {
		grossMinor = -amtMinor
	}

	// 3. over-explain guard: this portion can't exceed what's left to explain.
	remaining := txn.AmountMinor
	if txn.UnexplainedAmountMinor.Valid {
		remaining = txn.UnexplainedAmountMinor.Int64
	}
	if existing != nil {
		remaining += existing.GrossValueMinor // editing: give the old portion back first
	}
	if absInt64(grossMinor) > absInt64(remaining) {
		return zero, kernel.ErrValidation("that's more than the amount left to explain on this transaction", nil)
	}

	// 4. category / entity, by the type's entity_link.
	var categoryID, transferAccountID, paidUserID, paidInvoiceID pgtype.UUID
	switch tt.EntityLink {
	case "BANK_ACCOUNT": // Transfer to/from Another Account
		transferAccountID, err = s.resolveTransferAccount(ctx, orgID, accountUUID, req.TransferBankAccountID)
		if err != nil {
			return zero, err
		}
	case "USER": // Money Paid/Received to/from User
		paidUserID, err = s.resolveUser(ctx, orgID, req.PaidUserID)
		if err != nil {
			return zero, err
		}
		categoryID, err = s.requireOfferedCategory(ctx, orgID, typeCode, req.CategoryID)
		if err != nil {
			return zero, err
		}
	case "INVOICE": // Invoice Receipt — settle a sent sales invoice (no category, no VAT)
		paidInvoiceID, err = s.resolveInvoice(ctx, orgID, req.PaidInvoiceID, grossMinor, existing)
		if err != nil {
			return zero, err
		}
	default: // NONE, CAPITAL_ASSET → a plain category pick
		categoryID, err = s.requireOfferedCategory(ctx, orgID, typeCode, req.CategoryID)
		if err != nil {
			return zero, err
		}
	}

	// 5. VAT: optional rate → a fixed rate extracts the VAT from the portion gross;
	// a manual (non-fixed) rate stores the client-entered amount. An invoice receipt
	// carries no VAT of its own (the VAT lived on the invoice), so it is skipped.
	var vatRateID pgtype.UUID
	var vatValueMinor int64
	var isManualVAT bool
	if tt.EntityLink != "INVOICE" {
		vatRateID, vatValueMinor, isManualVAT, err = s.resolveExplanationVAT(ctx, req.VATRateID, req.VATAmount, grossMinor)
		if err != nil {
			return zero, err
		}
	}

	// 6. dated_on defaults to the transaction's date.
	dated := txn.DatedOn
	if req.DatedOn != nil && strings.TrimSpace(*req.DatedOn) != "" {
		t, err := time.Parse("2006-01-02", strings.TrimSpace(*req.DatedOn))
		if err != nil {
			return zero, kernel.ErrValidation("dated_on must be in YYYY-MM-DD format", err)
		}
		dated = pgtype.Date{Time: t, Valid: true}
	}

	return banking.CreateExplanationParams{
		OrganisationID:        orgID,
		BankTransactionID:     txn.ID,
		CreatedByUserID:       pgtype.UUID{Bytes: authUserID, Valid: true},
		DatedOn:               dated,
		Description:           kernel.NullText(req.Description),
		Type:                  typeCode,
		GrossValueMinor:       grossMinor,
		CategoryID:            categoryID,
		SalesTaxStatus:        "TAXABLE",
		SalesTaxRateID:        vatRateID,
		SalesTaxValueMinor:    vatValueMinor,
		IsManualSalesTax:      isManualVAT,
		TransferBankAccountID: transferAccountID,
		PaidUserID:            paidUserID,
		PaidInvoiceID:         paidInvoiceID,
		MarkedForReview:       false,
	}, nil
}

// requireOfferedCategory validates category_id is present, in the org, and OFFERED
// for the chosen type (per the transaction_type_categories mapping + company_type).
func (s *Service) requireOfferedCategory(ctx context.Context, orgID uuid.UUID, typeCode string, categoryID *string) (pgtype.UUID, error) {
	if categoryID == nil || strings.TrimSpace(*categoryID) == "" {
		return pgtype.UUID{}, kernel.ErrValidation("category_id is required for this type", nil)
	}
	cID, err := uuid.Parse(strings.TrimSpace(*categoryID))
	if err != nil {
		return pgtype.UUID{}, kernel.ErrValidation("category_id is not a valid UUID", err)
	}
	offered, err := s.catQueries.CategoryOfferedForType(ctx, categoriesdb.CategoryOfferedForTypeParams{
		OrganisationID:      orgID,
		TransactionTypeCode: typeCode,
		CompanyType:         s.orgCompanyType(ctx, orgID),
		CategoryID:          cID,
	})
	if err != nil {
		return pgtype.UUID{}, kernel.ErrInternal(err)
	}
	if !offered {
		return pgtype.UUID{}, kernel.ErrValidation("that category isn't valid for the chosen type", nil)
	}
	return pgtype.UUID{Bytes: cID, Valid: true}, nil
}

// resolveTransferAccount validates the transfer target is another live account in
// the org (not this one).
func (s *Service) resolveTransferAccount(ctx context.Context, orgID, accountUUID uuid.UUID, transferID *string) (pgtype.UUID, error) {
	if transferID == nil || strings.TrimSpace(*transferID) == "" {
		return pgtype.UUID{}, kernel.ErrValidation("transfer_bank_account_id is required for a transfer", nil)
	}
	taID, err := uuid.Parse(strings.TrimSpace(*transferID))
	if err != nil {
		return pgtype.UUID{}, kernel.ErrValidation("transfer_bank_account_id is not a valid UUID", err)
	}
	if taID == accountUUID {
		return pgtype.UUID{}, kernel.ErrValidation("can't transfer to the same account", nil)
	}
	if _, err := s.queries.GetBankAccount(ctx, banking.GetBankAccountParams{ID: taID, OrganisationID: orgID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, kernel.ErrValidation("transfer account not found", nil)
		}
		return pgtype.UUID{}, kernel.ErrInternal(err)
	}
	return pgtype.UUID{Bytes: taID, Valid: true}, nil
}

// resolveUser validates the paid user is an active member of the org.
func (s *Service) resolveUser(ctx context.Context, orgID uuid.UUID, userID *string) (pgtype.UUID, error) {
	if userID == nil || strings.TrimSpace(*userID) == "" {
		return pgtype.UUID{}, kernel.ErrValidation("paid_user_id is required for a user payment", nil)
	}
	uID, err := uuid.Parse(strings.TrimSpace(*userID))
	if err != nil {
		return pgtype.UUID{}, kernel.ErrValidation("paid_user_id is not a valid UUID", err)
	}
	if _, err := kernel.AuthorizeMember(ctx, s.authQueries, uID, orgID); err != nil {
		return pgtype.UUID{}, kernel.ErrValidation("the chosen user isn't an active member of this organisation", nil)
	}
	return pgtype.UUID{Bytes: uID, Valid: true}, nil
}

// resolveInvoice validates the Invoice Receipt link and returns the invoice's id to
// store. The invoice must be a live SENT invoice in the caller's org (cross-tenant →
// 422), and it must still owe money. The receipt portion may not exceed the invoice's
// OUTSTANDING balance — overpayment is rejected, nudging the user to split the bank
// line instead (the chosen product rule). On an EDIT, this explanation's own prior
// portion against the SAME invoice is given back to the outstanding first, so re-saving
// the same amount isn't falsely rejected (mirrors the over-explain guard's give-back).
func (s *Service) resolveInvoice(ctx context.Context, orgID uuid.UUID, invoiceID *string, grossMinor int64, existing *banking.BankTransactionExplanation) (pgtype.UUID, error) {
	if invoiceID == nil || strings.TrimSpace(*invoiceID) == "" {
		return pgtype.UUID{}, kernel.ErrValidation("paid_invoice_id is required for an invoice receipt", nil)
	}
	invUUID, err := uuid.Parse(strings.TrimSpace(*invoiceID))
	if err != nil {
		return pgtype.UUID{}, kernel.ErrValidation("paid_invoice_id is not a valid UUID", err)
	}
	// Org-scoped fetch (soft-delete aware): a cross-tenant or missing id returns no row.
	inv, err := s.invoiceQueries.GetInvoice(ctx, invoicesdb.GetInvoiceParams{ID: invUUID, OrganisationID: orgID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, kernel.ErrValidation("invoice not found", nil)
		}
		return pgtype.UUID{}, kernel.ErrInternal(err)
	}
	if inv.Status != invoiceStatusSent {
		return pgtype.UUID{}, kernel.ErrValidation("only a sent invoice can receive a payment", nil)
	}

	link := pgtype.UUID{Bytes: invUUID, Valid: true}
	// Outstanding = total - paid (what the generated due_value_minor column holds).
	outstanding := inv.TotalValueMinor - inv.PaidValueMinor
	if existing != nil && existing.Type == typeInvoiceReceipt && existing.PaidInvoiceID == link {
		outstanding += existing.GrossValueMinor // editing the same invoice: give the old portion back
	}
	if outstanding <= 0 {
		return pgtype.UUID{}, kernel.ErrValidation("that invoice is already fully paid", nil)
	}
	if grossMinor > outstanding {
		return pgtype.UUID{}, kernel.ErrValidation("that's more than the invoice's outstanding balance — split the transaction instead", nil)
	}
	return link, nil
}

// resolveExplanationVAT turns an optional vat_rate_id (+ optional vat_amount) into the
// stored rate id, VAT value, and the is_manual flag — mirroring expenses.resolveVAT:
//   - no rate              → (NULL, 0, false)
//   - fixed-ratio rate     → VAT EXTRACTED from the inclusive gross (money.ComputeFixedVAT);
//                            any client amount is ignored; is_manual = false
//   - non-fixed-ratio rate → value = the client vat_amount (0 if omitted); is_manual = true
func (s *Service) resolveExplanationVAT(ctx context.Context, vatRateID, vatAmount *string, grossMinor int64) (pgtype.UUID, int64, bool, error) {
	if vatRateID == nil || strings.TrimSpace(*vatRateID) == "" {
		return pgtype.UUID{}, 0, false, nil
	}
	rID, err := uuid.Parse(strings.TrimSpace(*vatRateID))
	if err != nil {
		return pgtype.UUID{}, 0, false, kernel.ErrValidation("vat_rate_id is not a valid UUID", err)
	}
	rate, err := s.catQueries.GetVatRate(ctx, rID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, 0, false, kernel.ErrValidation("vat_rate_id not found", nil)
		}
		return pgtype.UUID{}, 0, false, kernel.ErrInternal(err)
	}
	id := pgtype.UUID{Bytes: rID, Valid: true}

	if rate.IsFixedRatio {
		// Rate-locked: extract the VAT from the inclusive gross; ignore any client amount.
		// A single explanation's gross fits int32 (the £21.4m ceiling); clamp as a guard.
		vat := money.ComputeFixedVAT(money.ClampToInt32(absInt64(grossMinor)), rate.RateBps)
		return id, int64(vat), false, nil
	}

	// Manual (non-fixed) rate: store the client-entered amount as-is (0 if omitted).
	var valueMinor int64
	if vatAmount != nil && strings.TrimSpace(*vatAmount) != "" {
		minor, err := money.PoundsToMinor(strings.TrimSpace(*vatAmount))
		if err != nil {
			return pgtype.UUID{}, 0, false, kernel.ErrValidation("vat_amount is not a valid amount", err)
		}
		valueMinor = minor
	}
	return id, valueMinor, true, nil
}

// orgCompanyType reads the org's company_type ('' if unset → only the 'ALL'
// mapping rows match). Best-effort.
func (s *Service) orgCompanyType(ctx context.Context, orgID uuid.UUID) string {
	org, err := s.authQueries.GetOrganisation(ctx, orgID)
	if err != nil || !org.CompanyType.Valid {
		return ""
	}
	return org.CompanyType.String
}

// resyncInvoicePaid recomputes ONE invoice's paid_value_minor = Σ(its live
// INVOICE_RECEIPT explanations) and writes it via UpdateInvoicePaidValue, inside the
// caller's transaction (qtx/itx are the banking + invoices query sets bound to that
// tx). A no-op when invoiceID is NULL (the explanation isn't an invoice receipt).
// Recomputing from the Σ — rather than incrementing — keeps the figure drift-free
// across split / edit / re-point / delete. Tolerates a vanished invoice (concurrently
// soft-deleted) so an unrelated edit can't fail on it.
func (s *Service) resyncInvoicePaid(ctx context.Context, qtx *banking.Queries, itx *invoicesdb.Queries, orgID uuid.UUID, invoiceID pgtype.UUID) error {
	if !invoiceID.Valid {
		return nil
	}
	sum, err := qtx.SumInvoiceReceiptsForInvoice(ctx, banking.SumInvoiceReceiptsForInvoiceParams{
		PaidInvoiceID:  invoiceID,
		OrganisationID: orgID,
	})
	if err != nil {
		return kernel.ErrInternal(err)
	}
	invUUID, err := uuid.FromBytes(invoiceID.Bytes[:])
	if err != nil {
		return kernel.ErrInternal(err)
	}
	if _, err := itx.UpdateInvoicePaidValue(ctx, invoicesdb.UpdateInvoicePaidValueParams{
		ID:             invUUID,
		OrganisationID: orgID,
		PaidValueMinor: sum,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // invoice no longer live — nothing to update
		}
		return kernel.ErrInternal(err)
	}
	return nil
}

// =============================================================================
// LOADERS / RESPONSE
// =============================================================================

// loadTransactionForAccount fetches a transaction org-scoped (404) and enforces it
// belongs to the given account (404). Unlike loadManualTransaction, ANY line may be
// explained (feed/statement lines too).
func (s *Service) loadTransactionForAccount(ctx context.Context, orgID, accountUUID, txnUUID uuid.UUID) (banking.BankTransaction, error) {
	t, err := s.queries.GetBankTransaction(ctx, banking.GetBankTransactionParams{ID: txnUUID, OrganisationID: orgID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return banking.BankTransaction{}, kernel.ErrNotFound("bank transaction", txnUUID.String())
		}
		return banking.BankTransaction{}, kernel.ErrInternal(err)
	}
	if t.BankAccountID != accountUUID {
		return banking.BankTransaction{}, kernel.ErrNotFound("bank transaction", txnUUID.String())
	}
	return t, nil
}

// loadExplanationForTxn fetches an explanation org-scoped (404) and enforces it
// belongs to the given transaction (404).
func (s *Service) loadExplanationForTxn(ctx context.Context, orgID, txnUUID, explUUID uuid.UUID) (banking.BankTransactionExplanation, error) {
	e, err := s.queries.GetExplanation(ctx, banking.GetExplanationParams{ID: explUUID, OrganisationID: orgID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return banking.BankTransactionExplanation{}, kernel.ErrNotFound("explanation", explUUID.String())
		}
		return banking.BankTransactionExplanation{}, kernel.ErrInternal(err)
	}
	if e.BankTransactionID != txnUUID {
		return banking.BankTransactionExplanation{}, kernel.ErrNotFound("explanation", explUUID.String())
	}
	return e, nil
}

// buildExplanationsResponse loads the (recomputed) transaction + its detailed
// explanations into the API shape. Shared by the read + every mutation.
func (s *Service) buildExplanationsResponse(ctx context.Context, orgID, txnUUID uuid.UUID) (*TransactionExplanationsResponse, error) {
	txn, err := s.queries.GetBankTransaction(ctx, banking.GetBankTransactionParams{ID: txnUUID, OrganisationID: orgID})
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}
	rows, err := s.queries.ListExplanationsForTransactionDetailed(ctx, banking.ListExplanationsForTransactionDetailedParams{
		BankTransactionID: txnUUID,
		OrganisationID:    orgID,
	})
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}
	out := make([]*ExplanationResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, explanationRowToResponse(r))
	}
	unexplained := txn.AmountMinor
	if txn.UnexplainedAmountMinor.Valid {
		unexplained = txn.UnexplainedAmountMinor.Int64
	}
	return &TransactionExplanationsResponse{
		TransactionID:     txn.ID.String(),
		Status:            txn.Status,
		UnexplainedAmount: money.MinorToPounds(unexplained),
		Explanations:      out,
	}, nil
}

// explanationRowToResponse maps one detailed (JOINed) row into the API shape. The
// stored gross is signed; the response exposes its positive magnitude.
func explanationRowToResponse(r banking.ListExplanationsForTransactionDetailedRow) *ExplanationResponse {
	resp := &ExplanationResponse{
		ID:                  r.ID.String(),
		Type:                r.Type,
		Amount:              money.MinorToPounds(absInt64(r.GrossValueMinor)),
		CategoryName:        kernel.NullTextToPtr(r.CategoryName),
		CategoryNominalCode: kernel.NullTextToPtr(r.CategoryNominalCode),
		TransferAccountName: kernel.NullTextToPtr(r.TransferAccountName),
		VATValue:            money.MinorToPounds(r.SalesTaxValueMinor),
		Description:         kernel.NullTextToPtr(r.Description),
		DatedOn:             r.DatedOn.Time.Format("2006-01-02"),
		MarkedForReview:     r.MarkedForReview,
	}
	resp.CategoryID = uuidPtr(r.CategoryID)
	resp.TransferBankAccountID = uuidPtr(r.TransferBankAccountID)
	if r.PaidUserID.Valid {
		resp.PaidUserID = uuidPtr(r.PaidUserID)
		resp.PaidUserName = fullNamePtr(r.PaidUserFirstName, r.PaidUserLastName)
	}
	if r.PaidInvoiceID.Valid {
		resp.PaidInvoiceID = uuidPtr(r.PaidInvoiceID)
		resp.InvoiceReference = kernel.NullTextToPtr(r.InvoiceReference)
	}
	resp.VATRateID = uuidPtr(r.SalesTaxRateID)
	if r.VatRateBps.Valid {
		s := money.BpsToPercent(r.VatRateBps.Int32)
		resp.VATRate = &s
	}
	return resp
}

// =============================================================================
// SMALL HELPERS
// =============================================================================

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// uuidPtr renders a nullable pgtype.UUID as an optional string (nil when NULL).
func uuidPtr(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	id, err := uuid.FromBytes(u.Bytes[:])
	if err != nil {
		return nil
	}
	s := id.String()
	return &s
}

// fullNamePtr joins a user's first/last name; nil when both are blank.
func fullNamePtr(first, last pgtype.Text) *string {
	name := strings.TrimSpace(strings.TrimSpace(first.String) + " " + strings.TrimSpace(last.String))
	if name == "" {
		return nil
	}
	return &name
}
