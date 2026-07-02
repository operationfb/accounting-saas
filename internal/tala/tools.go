package tala

// tools.go
// =============================================================================
// The tool registry Tala exposes to the model.
//
// Each Tool wraps an EXISTING domain service read method. The executor is handed
// the caller's userID and orgID FROM THE TOKEN — never from the model input — so
// no tool schema ever carries an organisation/user id. This is the multi-tenant
// security boundary: the model can only ever see the current org's data, and the
// underlying service still runs its own authorisation (a member without access
// simply gets an error the model relays).
//
// Read tools return the service's DTO marshalled to JSON. propose_ tools (a
// separate file) return a proposal for the user to confirm; they never mutate.
// =============================================================================

import (
	"context"
	"encoding/json"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/google/uuid"

	"github.com/operationfb/accounting-saas/internal/banking"
	"github.com/operationfb/accounting-saas/internal/bills"
	"github.com/operationfb/accounting-saas/internal/expenses"
	"github.com/operationfb/accounting-saas/internal/invoices"
	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/internal/overview"
	"github.com/operationfb/accounting-saas/internal/reports"
	"github.com/operationfb/accounting-saas/internal/vat"
)

// toolResult is what a tool executor returns. Content is the text fed back to the
// model as the tool_result; Proposal is non-nil only for propose_ tools.
type toolResult struct {
	Content  string
	Proposal *ProposedAction
}

// Tool is one capability exposed to the model. Exec receives the token-derived
// userID/orgID plus the model-supplied JSON input (never an org id).
type Tool struct {
	Name        string
	Description string
	Properties  map[string]any // JSON-schema "properties" (may be empty for no-input tools)
	Required    []string
	Exec        func(ctx context.Context, userID, orgID uuid.UUID, input json.RawMessage) (toolResult, error)
}

// toParam renders the Tool as the SDK's tool definition.
func (t Tool) toParam() anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Name:        t.Name,
		Description: anthropic.String(t.Description),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: t.Properties,
			Required:   t.Required,
		},
	}}
}

// jsonResult marshals a service DTO into a tool_result payload.
func jsonResult(v any) (toolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return toolResult{}, kernel.ErrInternal(err)
	}
	return toolResult{Content: string(b)}, nil
}

// strField pulls a single string field out of the model's tool input.
func strField(input json.RawMessage, key string) string {
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// readTools builds the read-only tool set over the existing domain services.
// Nothing here mutates; each executor is a thin org-scoped wrapper.
func readTools(
	exp *expenses.Service,
	inv *invoices.Service,
	bill *bills.Service,
	bank *banking.Service,
	vatSvc *vat.Service,
	rep *reports.Service,
	ov *overview.Service,
) []Tool {
	noInput := map[string]any{}

	return []Tool{
		// ---- Expenses ----
		{
			Name:        "list_expenses",
			Description: "List the organisation's expenses. Owners/admins see all expenses; other members see only their own. Each row includes amount, category, date, supplier, approval status and needs_review flag.",
			Properties:  noInput,
			Exec: func(ctx context.Context, u, o uuid.UUID, _ json.RawMessage) (toolResult, error) {
				rows, err := exp.ListExpenses(ctx, u, o)
				if err != nil {
					return toolResult{}, err
				}
				return jsonResult(rows)
			},
		},
		{
			Name:        "list_expense_categories",
			Description: "List the spending categories (chart-of-accounts nominal codes) an expense can be filed under. Use to answer 'what categories exist' or to suggest a category.",
			Properties:  noInput,
			Exec: func(ctx context.Context, u, o uuid.UUID, _ json.RawMessage) (toolResult, error) {
				rows, err := exp.ListExpenseCategories(ctx, u, o)
				if err != nil {
					return toolResult{}, err
				}
				return jsonResult(rows)
			},
		},
		// ---- Invoices (money in) ----
		{
			Name:        "list_invoices",
			Description: "List all sales invoices the organisation has issued (money in), newest first, with their status and totals.",
			Properties:  noInput,
			Exec: func(ctx context.Context, u, o uuid.UUID, _ json.RawMessage) (toolResult, error) {
				rows, err := inv.ListInvoices(ctx, u, o)
				if err != nil {
					return toolResult{}, err
				}
				return jsonResult(rows)
			},
		},
		{
			Name:        "list_outstanding_invoices",
			Description: "List sales invoices that have been sent but are not yet fully paid — the money customers still owe. Use for receivables and overdue questions.",
			Properties:  noInput,
			Exec: func(ctx context.Context, u, o uuid.UUID, _ json.RawMessage) (toolResult, error) {
				rows, err := inv.ListOutstandingInvoices(ctx, u, o)
				if err != nil {
					return toolResult{}, err
				}
				return jsonResult(rows)
			},
		},
		// ---- Bills (money out) ----
		{
			Name:        "list_bills",
			Description: "List the supplier bills (money out) the organisation has recorded, with their status and totals.",
			Properties:  noInput,
			Exec: func(ctx context.Context, u, o uuid.UUID, _ json.RawMessage) (toolResult, error) {
				rows, err := bill.ListBills(ctx, u, o)
				if err != nil {
					return toolResult{}, err
				}
				return jsonResult(rows)
			},
		},
		{
			Name:        "list_outstanding_bills",
			Description: "List bills that are not yet fully paid — money the organisation still owes suppliers. Use for payables questions and to decide what to pay.",
			Properties:  noInput,
			Exec: func(ctx context.Context, u, o uuid.UUID, _ json.RawMessage) (toolResult, error) {
				rows, err := bill.ListOutstandingBills(ctx, u, o)
				if err != nil {
					return toolResult{}, err
				}
				return jsonResult(rows)
			},
		},
		// ---- Banking / cash ----
		{
			Name:        "list_bank_accounts",
			Description: "List the organisation's bank accounts with their current balances.",
			Properties:  noInput,
			Exec: func(ctx context.Context, u, o uuid.UUID, _ json.RawMessage) (toolResult, error) {
				rows, err := bank.ListBankAccounts(ctx, u, o)
				if err != nil {
					return toolResult{}, err
				}
				return jsonResult(rows)
			},
		},
		{
			Name:        "bank_account_transactions",
			Description: "List the transactions for one bank account, including which are explained/reconciled. Provide the account_id (get it from list_bank_accounts first).",
			Properties: map[string]any{
				"account_id": map[string]any{"type": "string", "description": "The UUID of the bank account (from list_bank_accounts)."},
			},
			Required: []string{"account_id"},
			Exec: func(ctx context.Context, u, o uuid.UUID, input json.RawMessage) (toolResult, error) {
				id := strField(input, "account_id")
				if id == "" {
					return toolResult{}, kernel.ErrValidation("account_id is required", nil)
				}
				res, err := bank.ListTransactions(ctx, u, o, id)
				if err != nil {
					return toolResult{}, err
				}
				return jsonResult(res)
			},
		},
		{
			Name:        "cash_position",
			Description: "The banking overview: total cash across the organisation's accounts. Use for 'how much cash do we have' questions.",
			Properties:  noInput,
			Exec: func(ctx context.Context, u, o uuid.UUID, _ json.RawMessage) (toolResult, error) {
				res, err := ov.Banking(ctx, u, o)
				if err != nil {
					return toolResult{}, err
				}
				return jsonResult(res)
			},
		},
		{
			Name:        "cashflow_overview",
			Description: "The cash-flow overview: money in vs money out over recent periods. Use for trend and near-term cash questions.",
			Properties:  noInput,
			Exec: func(ctx context.Context, u, o uuid.UUID, _ json.RawMessage) (toolResult, error) {
				res, err := ov.Cashflow(ctx, u, o)
				if err != nil {
					return toolResult{}, err
				}
				return jsonResult(res)
			},
		},
		{
			Name:        "invoice_timeline",
			Description: "The invoice timeline: upcoming and overdue invoice amounts by date. Use for receivables forecasting.",
			Properties:  noInput,
			Exec: func(ctx context.Context, u, o uuid.UUID, _ json.RawMessage) (toolResult, error) {
				res, err := ov.InvoiceTimeline(ctx, u, o)
				if err != nil {
					return toolResult{}, err
				}
				return jsonResult(res)
			},
		},
		// ---- VAT & accounts ----
		{
			Name:        "vat_periods",
			Description: "List the organisation's VAT return periods with their status (open/filed). Use to see which VAT returns exist.",
			Properties:  noInput,
			Exec: func(ctx context.Context, u, o uuid.UUID, _ json.RawMessage) (toolResult, error) {
				rows, err := vatSvc.ListPeriods(ctx, u, o)
				if err != nil {
					return toolResult{}, err
				}
				return jsonResult(rows)
			},
		},
		{
			Name:        "vat_return",
			Description: "Get the VAT return figures (the 9 boxes) for one period. Provide the period_key (get it from vat_periods first).",
			Properties: map[string]any{
				"period_key": map[string]any{"type": "string", "description": "The period key (e.g. the period-end date) from vat_periods."},
			},
			Required: []string{"period_key"},
			Exec: func(ctx context.Context, u, o uuid.UUID, input json.RawMessage) (toolResult, error) {
				key := strField(input, "period_key")
				if key == "" {
					return toolResult{}, kernel.ErrValidation("period_key is required", nil)
				}
				res, err := vatSvc.GetReturn(ctx, u, o, key)
				if err != nil {
					return toolResult{}, err
				}
				return jsonResult(res)
			},
		},
		{
			Name:        "trial_balance",
			Description: "The trial balance as of today: every nominal account with its balance. Use for accounting questions and to see the overall financial position.",
			Properties:  noInput,
			Exec: func(ctx context.Context, u, o uuid.UUID, _ json.RawMessage) (toolResult, error) {
				res, err := rep.TrialBalance(ctx, u, o, time.Now().UTC())
				if err != nil {
					return toolResult{}, err
				}
				return jsonResult(res)
			},
		},
		{
			Name:        "account_transactions",
			Description: "List the ledger transactions for one nominal account, up to today. Provide the nominal code (find codes on the trial_balance).",
			Properties: map[string]any{
				"nominal": map[string]any{"type": "string", "description": "The nominal account code (e.g. '251'), from the trial balance."},
			},
			Required: []string{"nominal"},
			Exec: func(ctx context.Context, u, o uuid.UUID, input json.RawMessage) (toolResult, error) {
				nominal := strField(input, "nominal")
				if nominal == "" {
					return toolResult{}, kernel.ErrValidation("nominal is required", nil)
				}
				res, err := rep.AccountTransactions(ctx, u, o, nominal, nil, time.Now().UTC(), false)
				if err != nil {
					return toolResult{}, err
				}
				return jsonResult(res)
			},
		},
	}
}
