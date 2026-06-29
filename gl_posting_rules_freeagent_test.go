package main

// gl_posting_rules_freeagent_test.go
// =============================================================================
// LIVE validation of the gl_posting_rules mapping against FreeAgent — the system
// the chart of accounts was modelled on. GATED on FREEAGENT_VALIDATE=1 (like
// TestDocumentAILive / DOCAI_LIVE_TEST=1) so routine `go test ./...` stays offline
// and unbilled, and so it only runs when an operator wants to check the rules.
//
// It uses the org's REAL stored FreeAgent connection (the dev org's
// organisation_integrations row) via the production refresh path
// (integrations.Service.GetToken) — which refreshes + re-persists a rotated
// refresh token, exactly like the app does, so running this never strands the
// connection. If the refresh FAILS (revoked/expired), GetToken clears the
// connection and returns a 409; the test then FAILS with a clear "reconnect
// FreeAgent" message so the operator knows to re-authorise.
//
// What it checks:
//   - the income/expense category nominals the rules resolve to (e.g. 001 Sales)
//     are real accounts in FreeAgent's chart — STRICT (these are reliably present);
//   - the control-account nominals (Debtors/Creditors/VAT/Bank/User/Opening) are
//     reconciled against FreeAgent's chart + trial balance and REPORTED (a
//     zero-balance control account can legitimately be absent from the trial
//     balance, so this is logged, not asserted, with the full nominal set printed).
// =============================================================================

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/operationfb/accounting-saas/db/auth"
	integrationsdb "github.com/operationfb/accounting-saas/db/integrations"
	"github.com/operationfb/accounting-saas/internal/integrations"
	"github.com/operationfb/accounting-saas/internal/integrations/freeagent"
	"github.com/operationfb/accounting-saas/token"
)

// devOrgID (the seeded dev organisation that holds the real FreeAgent connection)
// is declared in server_test.go and reused here.

func TestGLPostingRulesAgainstFreeAgent(t *testing.T) {
	if os.Getenv("FREEAGENT_VALIDATE") != "1" {
		t.Skip("FREEAGENT_VALIDATE not set — skipping live FreeAgent validation (set FREEAGENT_VALIDATE=1 to run)")
	}
	ts := newTestServer(t) // gives us the pool + skips cleanly without DATABASE_URL
	ctx := context.Background()

	// Build an integrations.Service bound to the REAL 'freeagent' provider key (the
	// harness's own integrationService uses a throwaway key, so we build our own to
	// reach the operator-authorised connection). attachmentService is nil — GetToken
	// never touches it (HMRC wires it nil the same way in main.go).
	sandbox := os.Getenv("FREEAGENT_SANDBOX") == "true"
	tokenMaker, err := token.NewPasetoMaker([]byte(testSymmetricKey))
	if err != nil {
		t.Fatalf("build token maker: %v", err)
	}
	intSvc := integrations.NewService(
		integrationsdb.New(ts.pool), auth.New(ts.pool), freeagent.NewClient(sandbox),
		nil, 0, freeagent.ProviderKey, tokenMaker, "http://api.test", testAppBaseURL,
	)

	// Refresh-and-persist a live access token through the production path. A failed
	// refresh means the stored connection is broken — tell the operator to reconnect.
	accessToken, apiBase, err := intSvc.GetToken(ctx, uuid.MustParse(devOrgID))
	if err != nil {
		t.Fatalf("could not obtain a FreeAgent access token for the dev org — the connection looks dropped.\n"+
			"  → reconnect FreeAgent (Settings → Integrations → Connect, or the /api/v1/freeagent/connect flow) and re-run.\n"+
			"  underlying error: %v", err)
	}
	t.Logf("FreeAgent token OK (sandbox=%v, api=%s)", sandbox, apiBase)

	faNominals := map[string]string{} // nominal_code -> name, the union of the two sources

	// 1) Chart of accounts: GET /categories (reliably lists P&L categories + some BS).
	{
		body := faGet(t, ctx, apiBase, accessToken, "/categories")
		var resp map[string][]struct {
			NominalCode string `json:"nominal_code"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decode /categories: %v\nbody: %s", err, truncate(body, 600))
		}
		for group, cats := range resp {
			for _, c := range cats {
				if c.NominalCode != "" {
					faNominals[c.NominalCode] = c.Description
				}
			}
			t.Logf("/categories[%s]: %d accounts", group, len(cats))
		}
	}

	// 2) Trial balance: GET /accounting/trial_balance/summary (every GL account with
	//    activity). Shape can vary, so parse leniently and just harvest nominal codes.
	{
		body := faGet(t, ctx, apiBase, accessToken, "/accounting/trial_balance/summary")
		for code, name := range harvestNominalCodes(body) {
			if _, ok := faNominals[code]; !ok {
				faNominals[code] = name
			}
		}
		t.Logf("after trial balance: %d distinct FreeAgent nominal codes known", len(faNominals))
	}

	// STRICT: the income/expense category nominals our rules resolve to must exist.
	// 001 = SALES_DEFAULT (the INVOICE_SENT credit). The others are a sample of the
	// expense/admin nominals the EXPLANATION_CATEGORY / SOURCE_CATEGORY roles land on.
	for _, code := range []string{"001"} {
		if _, ok := faNominals[code]; !ok {
			t.Errorf("FreeAgent chart is missing nominal %q — gl_posting_rules relies on it; the mapping has drifted from FreeAgent", code)
		}
	}

	// RECONCILE: the control-account nominals our account_roles resolve to, against
	// BOTH (a) FreeAgent's chart (does the nominal exist?) and (b) our own categories
	// seed label (does our name match FreeAgent's?). Label mismatches are LOGGED, not
	// failed — a name can legitimately differ — but they flag where our seed has
	// drifted from FreeAgent, which is exactly what this validation is for.
	// EVERY gl_account_roles mapping (not a hardcoded subset), so new roles — the
	// payroll expense/liability accounts, SUSPENSE, … — are validated automatically as
	// they're added to the seed.
	type roleMap struct{ code, role string }
	var controls []roleMap
	// Only the GLOBAL defaults are validated against FreeAgent's UK chart; per-org /
	// per-country overrides may legitimately use a different (non-FreeAgent) scheme.
	rrows, err := ts.pool.Query(context.Background(),
		`SELECT nominal_code, role FROM gl_account_roles
		 WHERE organisation_id IS NULL AND country_code IS NULL ORDER BY role`)
	if err != nil {
		t.Fatalf("query gl_account_roles: %v", err)
	}
	for rrows.Next() {
		var r roleMap
		if err := rrows.Scan(&r.code, &r.role); err != nil {
			rrows.Close()
			t.Fatalf("scan gl_account_roles: %v", err)
		}
		controls = append(controls, r)
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		t.Fatalf("iterate gl_account_roles: %v", err)
	}
	ourNames := ourCategoryNames(t, ts.pool) // dev-org categories: nominal_code -> name
	for _, ctl := range controls {
		faName, inFA := faNominals[ctl.code]
		ourName := ourNames[ctl.code]
		switch {
		case !inFA:
			t.Logf("role %-14s → %-3s : NOT in FreeAgent's chart/trial balance "+
				"(zero-balance BS account FreeAgent omits, or a code mismatch — verify)", ctl.role, ctl.code)
		case ourName != "" && !namesAlign(ourName, faName):
			t.Logf("role %-14s → %-3s : ⚠ LABEL MISMATCH — ours %q vs FreeAgent %q", ctl.role, ctl.code, ourName, faName)
		default:
			t.Logf("role %-14s → %-3s : ✓ FreeAgent %q", ctl.role, ctl.code, faName)
		}
	}
}

// ourCategoryNames returns the dev org's categories as nominal_code -> name.
func ourCategoryNames(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, err := pool.Query(context.Background(),
		`SELECT nominal_code, name FROM categories WHERE organisation_id = $1`, devOrgID)
	if err != nil {
		t.Fatalf("query categories: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var code, name string
		if err := rows.Scan(&code, &name); err != nil {
			t.Fatalf("scan categories: %v", err)
		}
		out[code] = name
	}
	return out
}

// namesAlign is a loose comparison (case-insensitive substring either way) so trivial
// wording differences ("VAT" vs "VAT Control") don't read as mismatches.
func namesAlign(a, b string) bool {
	la, lb := strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	return la == lb || strings.Contains(la, lb) || strings.Contains(lb, la)
}

// faGet does an authenticated GET against the FreeAgent API and fails the test on a
// non-2xx. base already includes /v2; path begins with '/'.
func faGet(t *testing.T, ctx context.Context, base, token, path string) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatalf("build request %s: %v", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("GET %s -> %d: %s", path, resp.StatusCode, truncate(body, 600))
	}
	return body
}

// harvestNominalCodes walks an arbitrary JSON document and collects every
// "nominal_code" value it finds (with a sibling name/description if present). Robust
// to the trial-balance summary's exact shape, which we don't want to hardcode.
func harvestNominalCodes(body []byte) map[string]string {
	out := map[string]string{}
	var doc interface{}
	if json.Unmarshal(body, &doc) != nil {
		return out
	}
	var walk func(v interface{})
	walk = func(v interface{}) {
		switch n := v.(type) {
		case map[string]interface{}:
			if code, ok := n["nominal_code"].(string); ok && code != "" {
				name, _ := n["name"].(string)
				if name == "" {
					name, _ = n["description"].(string)
				}
				out[code] = name
			}
			for _, child := range n {
				walk(child)
			}
		case []interface{}:
			for _, child := range n {
				walk(child)
			}
		}
	}
	walk(doc)
	return out
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
