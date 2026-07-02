package tala

// propose.go
// =============================================================================
// Guarded-write tools. These NEVER touch the database. They validate the fields
// the model gathered and return a ProposedAction — a description of a change the
// user must explicitly confirm. The SPA renders it as a card; on Confirm it calls
// the existing domain endpoint (POST /api/v1/expenses, POST /expenses/:id/status),
// which does the real, already-authorised, DB-constrained work.
//
// Keeping mutations OUT of the agent loop is the safety model: a model mistake or
// a prompt injection can, at worst, propose something the user then declines.
// =============================================================================

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/money"
)

// proposeTools are pure — they need no service handles (execution happens later,
// on the user's confirmation, through the existing endpoints).
func proposeTools() []Tool {
	return []Tool{
		{
			Name:        "propose_create_expense",
			Description: "Prepare a NEW expense for the user to confirm. This does NOT create the expense — it shows the user a confirmation card. Gather the amount and a description; the date defaults to today and the currency to GBP. The user picks the final category on the card. After calling this, tell the user to review and click Confirm.",
			Properties: map[string]any{
				"description":   map[string]any{"type": "string", "description": "What the expense was for, e.g. 'Team lunch'."},
				"gross_value":   map[string]any{"type": "string", "description": "The total amount paid, in pounds, e.g. '42.50'."},
				"dated_on":      map[string]any{"type": "string", "description": "Date of the expense, YYYY-MM-DD. Defaults to today if omitted."},
				"currency":      map[string]any{"type": "string", "description": "3-letter currency code. Defaults to GBP."},
				"supplier_name": map[string]any{"type": "string", "description": "Who was paid, if known."},
				"category_hint": map[string]any{"type": "string", "description": "A suggested category name if the user indicated one; the user confirms the final category."},
			},
			Required: []string{"description", "gross_value"},
			Exec:     execProposeCreateExpense,
		},
		{
			Name:        "propose_approve_expense",
			Description: "Prepare to APPROVE a submitted expense, for the user to confirm. This does NOT approve it — it shows a confirmation card. Provide the expense's id (find it first with list_expenses). Only submitted expenses can be approved, and only by owners/admins; the confirmation step enforces this.",
			Properties: map[string]any{
				"expense_id": map[string]any{"type": "string", "description": "The UUID of the expense to approve."},
			},
			Required: []string{"expense_id"},
			Exec:     execProposeApproveExpense,
		},
	}
}

func execProposeCreateExpense(_ context.Context, _, _ uuid.UUID, input json.RawMessage) (toolResult, error) {
	var in struct {
		Description  string `json:"description"`
		GrossValue   string `json:"gross_value"`
		DatedOn      string `json:"dated_on"`
		Currency     string `json:"currency"`
		SupplierName string `json:"supplier_name"`
		CategoryHint string `json:"category_hint"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return toolResult{}, kernel.ErrValidation("could not read the expense details", err)
	}

	desc := strings.TrimSpace(in.Description)
	if desc == "" {
		return toolResult{}, kernel.ErrValidation("description is required", nil)
	}
	gross := strings.TrimSpace(in.GrossValue)
	// Validate the amount with the canonical pounds→pence rule (rejects nonsense
	// like "twelve quid" before it ever reaches the user's confirmation card).
	if _, err := money.PoundsToMinor(gross); err != nil {
		return toolResult{}, kernel.ErrValidation(fmt.Sprintf("gross_value %q is not a valid amount — use pounds like 42.50", in.GrossValue), err)
	}
	dated := strings.TrimSpace(in.DatedOn)
	if dated == "" {
		dated = time.Now().UTC().Format("2006-01-02")
	}
	currency := strings.ToUpper(strings.TrimSpace(in.Currency))
	if currency == "" {
		currency = "GBP"
	}

	// The payload carries what Tala gathered; the SPA adds the user-chosen
	// category_id before POSTing to /api/v1/expenses.
	payload, _ := json.Marshal(map[string]any{
		"description":   desc,
		"gross_value":   gross,
		"dated_on":      dated,
		"currency":      currency,
		"supplier_name": strings.TrimSpace(in.SupplierName),
		"category_hint": strings.TrimSpace(in.CategoryHint),
	})

	return toolResult{
		Content: "A create-expense proposal has been prepared and shown to the user for confirmation. Do not claim the expense was created — ask the user to review the category and click Confirm.",
		Proposal: &ProposedAction{
			Kind:    "create_expense",
			Title:   "Create expense",
			Summary: fmt.Sprintf("Create an expense of %s %s for %q, dated %s.", currency, gross, desc, dated),
			Payload: payload,
		},
	}, nil
}

func execProposeApproveExpense(_ context.Context, _, _ uuid.UUID, input json.RawMessage) (toolResult, error) {
	var in struct {
		ExpenseID string `json:"expense_id"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return toolResult{}, kernel.ErrValidation("could not read the expense id", err)
	}
	id, err := uuid.Parse(strings.TrimSpace(in.ExpenseID))
	if err != nil {
		return toolResult{}, kernel.ErrValidation(fmt.Sprintf("%q is not a valid expense id", in.ExpenseID), err)
	}

	payload, _ := json.Marshal(map[string]any{"expense_id": id.String()})
	return toolResult{
		Content: "An approve-expense proposal has been prepared and shown to the user for confirmation. Do not claim it was approved — ask the user to click Confirm.",
		Proposal: &ProposedAction{
			Kind:    "approve_expense",
			Title:   "Approve expense",
			Summary: fmt.Sprintf("Approve expense %s.", id.String()),
			Payload: payload,
		},
	}, nil
}
