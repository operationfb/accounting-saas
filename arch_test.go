package main

// arch_test.go
// =============================================================================
// ARCHITECTURE GUARDRAILS — run under plain `go test ./...` (no CI required).
//
// The codebase is migrating to a modular monolith: the shared kernel lives in
// internal/kernel, domains will live in internal/<domain>, and the repo-root
// package main is WIRING-ONLY. These tests keep it that way.
//
//   TestRootPackageIsWiringOnly — fails if a .go file appears in the repo root
//     that isn't on the allowlist below. New domain or shared code must go in
//     internal/<domain> or internal/kernel, NOT the root. As files migrate into
//     internal/, REMOVE them from the allowlist — it only ever shrinks (→ empty,
//     at which point main moves to cmd/server and this flips to "no root .go").
//
//   TestKernelHasNoDomainImports — keeps internal/kernel foundational: it must
//     not import any other internal/ package.
//
//   TestIntegrationsCoreHasNoProviderImports — keeps the shared
//     internal/integrations package provider-agnostic: it must not import any
//     provider sub-package (freeagent, future xero, …). Providers depend on the
//     core, never the reverse.
// =============================================================================

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rootAllowlist is the set of .go files permitted in the repo-root package main.
// The modular-monolith migration is COMPLETE: all domains live in internal/<domain>
// and all shared infrastructure in internal/kernel, so the only non-test root files
// are the wiring (main.go, server.go) + this guardrail. The rest of the list is the
// integration test files, which stay in root (package main) because they drive the
// assembled server via the newTestServer harness. New code belongs in
// internal/<domain> or internal/kernel — never the repo root.
var rootAllowlist = map[string]bool{
	"arch_test.go":                 true,
	"attachment_handler_test.go":   true,
	"attachment_service_test.go":   true,
	"banking_service_test.go":      true,
	"banking_test.go":              true,
	"bill_service_test.go":         true,
	"contact_service_test.go":      true,
	"email_inbox_test.go":          true,
	"events_test.go":               true,
	"expense_status_test.go":       true,
	"inbound_email.go":             true,
	"integration_internal_test.go": true,
	"integration_service_test.go":  true,
	"invoice_service_test.go":      true,
	"main.go":                      true,
	"member_service_test.go":       true,
	"ocr_service_test.go":          true,
	"organisation_service_test.go": true,
	"reconcile_test.go":            true,
	"server.go":                    true,
	"server_test.go":               true,
	"supplier_category_test.go":    true,
	"user_service_test.go":         true,
	"vat_hmrc_account_test.go":     true,
	"vat_rates_test.go":            true,
	"vat_return_test.go":           true,
	"vat_settings_test.go":         true,
}

func TestRootPackageIsWiringOnly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read root dir: %v", err)
	}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") {
			continue
		}
		if !rootAllowlist[n] {
			t.Errorf("new .go file in root package main: %q\n"+
				"  → put new code in internal/<domain> or internal/kernel, not the repo root (see CLAUDE.md).\n"+
				"  → if it genuinely belongs in root wiring, add it to rootAllowlist in arch_test.go.", n)
		}
	}
}

func TestKernelHasNoDomainImports(t *testing.T) {
	const (
		internalPrefix = "github.com/operationfb/accounting-saas/internal/"
		kernelPrefix   = "github.com/operationfb/accounting-saas/internal/kernel"
	)
	fset := token.NewFileSet()
	err := filepath.WalkDir("internal/kernel", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(p, internalPrefix) && !strings.HasPrefix(p, kernelPrefix) {
				t.Errorf("%s imports %q — internal/kernel must not depend on any domain package (keep the kernel foundational)", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/kernel: %v", err)
	}
}

// TestIntegrationsCoreHasNoProviderImports keeps the shared internal/integrations
// package provider-agnostic: it must NOT import any provider sub-package (e.g.
// internal/integrations/freeagent). Providers depend on the core, never the
// reverse — that dependency direction is what makes the core reusable for Xero etc.
func TestIntegrationsCoreHasNoProviderImports(t *testing.T) {
	const (
		coreDir         = "internal/integrations"
		providersPrefix = "github.com/operationfb/accounting-saas/internal/integrations/"
	)
	fset := token.NewFileSet()
	entries, err := os.ReadDir(coreDir) // top-level only — not the provider sub-dirs
	if err != nil {
		t.Fatalf("read %s: %v", coreDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(coreDir, e.Name()), nil, parser.ImportsOnly)
		if perr != nil {
			t.Fatalf("parse %s: %v", e.Name(), perr)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(p, providersPrefix) {
				t.Errorf("%s/%s imports %q — the shared integrations package must not depend on a provider sub-package", coreDir, e.Name(), p)
			}
		}
	}
}
