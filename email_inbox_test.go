package main

// email_inbox_test.go
// =============================================================================
// Integration tests for the email-to-expense channel (EmailInboxService + the
// Mailgun webhook handler).
//
// Following the repo's rules: real Postgres + real GCS, and we fake ONLY the
// external services — Mailgun (we build InboundEmail directly / POST a signed
// multipart payload) and Gotenberg (fakeHTMLRenderer). Tests that actually
// capture a file (and so hit GCS) call requireGCS; routing/auth/address tests
// don't capture and run without GCS.
// =============================================================================

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// =============================================================================
// FAKES + HELPERS
// =============================================================================

// fakeHTMLRenderer stands in for Gotenberg. It returns canned PDF bytes (so the
// capture pipeline sees a valid application/pdf) and records the last HTML.
type fakeHTMLRenderer struct {
	pdf      []byte
	lastHTML string
	calls    int
	err      error
}

func (f *fakeHTMLRenderer) RenderPDF(_ context.Context, html string) ([]byte, error) {
	f.calls++
	f.lastHTML = html
	if f.err != nil {
		return nil, f.err
	}
	return f.pdf, nil
}

// inboundFile builds an InboundAttachment whose Open() yields a fresh reader.
func inboundFile(filename string, data []byte) InboundAttachment {
	return InboundAttachment{
		Filename: filename,
		Size:     int64(len(data)),
		Open:     func() (io.ReadSeeker, error) { return bytes.NewReader(data), nil },
	}
}

// newMessageID returns a unique Message-Id and registers cleanup of its event row
// (covers events whose org is NULL, which the org cleanup wouldn't catch).
func newMessageID(t *testing.T, ts *testServer) string {
	t.Helper()
	id := "<" + uuid.NewString() + "@test.local>"
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(),
			`DELETE FROM inbound_email_events WHERE provider_message_id = $1`, id)
	})
	return id
}

// setInboxLocalPart pins a known inbox address local part on a membership, so
// routing tests don't depend on the address generator.
func setInboxLocalPart(t *testing.T, ts *testServer, orgID, userID, localPart string) {
	t.Helper()
	if _, err := ts.pool.Exec(context.Background(),
		`UPDATE organisation_memberships SET inbox_local_part = $3 WHERE organisation_id = $1 AND user_id = $2`,
		orgID, userID, localPart); err != nil {
		t.Fatalf("setInboxLocalPart: %v", err)
	}
}

// cleanupOrgCaptures removes any drafts/attachments (and their GCS objects) plus
// inbound-email events an ingest created for an org. Register it right AFTER
// newOrgWithOwner so it runs FIRST (LIFO) — before the org row is deleted.
func cleanupOrgCaptures(t *testing.T, ts *testServer, orgID, ownerID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		rows, err := ts.pool.Query(ctx,
			`SELECT expense_id::text, id::text FROM expense_attachments WHERE organisation_id = $1`, orgID)
		if err == nil {
			type pair struct{ exp, att string }
			var ps []pair
			for rows.Next() {
				var e, a string
				_ = rows.Scan(&e, &a)
				ps = append(ps, pair{e, a})
			}
			rows.Close()
			// DeleteAttachment removes the metadata row AND the GCS object.
			for _, p := range ps {
				_ = ts.server.attachmentService.DeleteAttachment(ctx, mustUUID(t, ownerID), mustUUID(t, orgID), p.exp, p.att)
			}
		}
		_, _ = ts.pool.Exec(ctx, `DELETE FROM expenses WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM inbound_email_events WHERE organisation_id = $1`, orgID)
	})
}

// eventByMessageID reads the audit row's terminal state for assertions.
func eventByMessageID(t *testing.T, ts *testServer, msgID string) (status string, drafts int, sender string) {
	t.Helper()
	if err := ts.pool.QueryRow(context.Background(),
		`SELECT status, drafts_created, sender FROM inbound_email_events WHERE provider_message_id = $1`, msgID).
		Scan(&status, &drafts, &sender); err != nil {
		t.Fatalf("eventByMessageID(%s): %v", msgID, err)
	}
	return status, drafts, sender
}

// draftCountForOrg counts needs_review skeleton drafts for an org.
func draftCountForOrg(t *testing.T, ts *testServer, orgID string) int {
	t.Helper()
	var n int
	if err := ts.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM expenses WHERE organisation_id = $1 AND needs_review = TRUE`, orgID).Scan(&n); err != nil {
		t.Fatalf("draftCountForOrg: %v", err)
	}
	return n
}

// soleDraftClaimant returns the (user_id, created_by_user_id) of the single draft
// expected for an org.
func soleDraftClaimant(t *testing.T, ts *testServer, orgID string) (userID, createdBy string) {
	t.Helper()
	if err := ts.pool.QueryRow(context.Background(),
		`SELECT user_id::text, created_by_user_id::text FROM expenses WHERE organisation_id = $1`, orgID).
		Scan(&userID, &createdBy); err != nil {
		t.Fatalf("soleDraftClaimant: %v", err)
	}
	return userID, createdBy
}

// ownerEmailFor / memberEmailFor mirror the email format the fixture helpers use,
// so a test can use a real member's address as the email From without a query.
func ownerEmailFor(userID string) string  { return "owner-" + userID + "@test.local" }
func memberEmailFor(userID string) string { return "member-" + userID + "@test.local" }

// seedSundries inserts the '6021' Sundries placeholder category that
// CaptureFromReceipt files a skeleton draft under. Ephemeral orgs (unlike the
// seeded dev org) have no categories, so capture tests must seed this one.
func seedSundries(t *testing.T, ts *testServer, orgID string) {
	t.Helper()
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO expense_categories (organisation_id, nominal_code, name, category_group)
		 VALUES ($1, '6021', 'Sundries', 'ADMIN')`, orgID); err != nil {
		t.Fatalf("seedSundries: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(),
			`DELETE FROM expense_categories WHERE organisation_id = $1`, orgID)
	})
}

// newCaptureOrg sets up an ephemeral org + active owner ready for capture: a
// seeded Sundries category, a known inbox local part, and full cleanup (drafts,
// attachments, GCS objects, events, category, org). Returns (orgID, ownerID, localPart).
func newCaptureOrg(t *testing.T, ts *testServer) (orgID, ownerID, localPart string) {
	t.Helper()
	orgID, ownerID = newOrgWithOwner(t, ts)
	seedSundries(t, ts, orgID)
	cleanupOrgCaptures(t, ts, orgID, ownerID)
	localPart = "alpha-" + orgID[:8]
	setInboxLocalPart(t, ts, orgID, ownerID, localPart)
	return orgID, ownerID, localPart
}

// =============================================================================
// ROUTING + AUTH (no capture → no GCS needed)
// =============================================================================

func TestEmailInboxRouting(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()
	ctx := context.Background()

	t.Run("unknown address is ignored", func(t *testing.T) {
		msg := newMessageID(t, ts)
		out, err := ts.emailInboxService.Ingest(ctx, &InboundEmail{
			MessageID: msg,
			Recipient: "nobody-" + uuid.NewString() + "@" + testInboxDomain,
			From:      "someone@example.com",
			Subject:   "Receipt",
		})
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if out.Status != "ignored_unknown_address" {
			t.Errorf("status: got %q, want ignored_unknown_address", out.Status)
		}
		if status, _, _ := eventByMessageID(t, ts, msg); status != "ignored_unknown_address" {
			t.Errorf("event status: got %q", status)
		}
	})

	t.Run("recipient off our domain is ignored", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		setInboxLocalPart(t, ts, orgID, ownerID, "alpha-"+orgID[:8])
		msg := newMessageID(t, ts)
		out, err := ts.emailInboxService.Ingest(ctx, &InboundEmail{
			MessageID: msg,
			Recipient: "alpha@some-other-domain.com",
			From:      ownerEmailFor(ownerID),
		})
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if out.Status != "ignored_unknown_address" {
			t.Errorf("status: got %q, want ignored_unknown_address", out.Status)
		}
	})

	t.Run("sender who is not a member is rejected", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		local := "alpha-" + orgID[:8]
		setInboxLocalPart(t, ts, orgID, ownerID, local)
		msg := newMessageID(t, ts)
		out, err := ts.emailInboxService.Ingest(ctx, &InboundEmail{
			MessageID: msg,
			Recipient: local + "@" + testInboxDomain,
			From:      "outsider@example.com", // not a member of this org
		})
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if out.Status != "ignored_sender_not_member" {
			t.Errorf("status: got %q, want ignored_sender_not_member", out.Status)
		}
	})

	t.Run("cross-tenant: org A member can't file into org B's inbox", func(t *testing.T) {
		orgA, ownerA := newOrgWithOwner(t, ts)
		orgB, ownerB := newOrgWithOwner(t, ts)
		localB := "beta-" + orgB[:8]
		setInboxLocalPart(t, ts, orgB, ownerB, localB)

		msg := newMessageID(t, ts)
		// ownerA is a real, active member — but of org A, not org B.
		out, err := ts.emailInboxService.Ingest(ctx, &InboundEmail{
			MessageID: msg,
			Recipient: localB + "@" + testInboxDomain,
			From:      ownerEmailFor(ownerA),
		})
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if out.Status != "ignored_sender_not_member" {
			t.Errorf("status: got %q, want ignored_sender_not_member (cross-tenant must be rejected)", out.Status)
		}
		if n := draftCountForOrg(t, ts, orgB); n != 0 {
			t.Errorf("org B drafts: got %d, want 0", n)
		}
		_ = orgA
	})

	t.Run("no attachment and no body is ignored", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		local := "alpha-" + orgID[:8]
		setInboxLocalPart(t, ts, orgID, ownerID, local)
		msg := newMessageID(t, ts)
		out, err := ts.emailInboxService.Ingest(ctx, &InboundEmail{
			MessageID: msg,
			Recipient: local + "@" + testInboxDomain,
			From:      ownerEmailFor(ownerID),
			// no attachments, no body
		})
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if out.Status != "ignored_no_attachments" {
			t.Errorf("status: got %q, want ignored_no_attachments", out.Status)
		}
	})

	t.Run("missing Message-Id is rejected (so Mailgun retries, never duplicates)", func(t *testing.T) {
		_, err := ts.emailInboxService.Ingest(ctx, &InboundEmail{
			Recipient: "alpha@" + testInboxDomain,
			From:      "x@example.com",
		})
		assertAppCode(t, err, ErrCodeValidation)
	})
}

// =============================================================================
// CAPTURE (real GCS)
// =============================================================================

func TestEmailInboxCapture(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()
	ctx := context.Background()

	t.Run("happy path: member emails their own inbox", func(t *testing.T) {
		orgID, ownerID, local := newCaptureOrg(t, ts)

		msg := newMessageID(t, ts)
		out, err := ts.emailInboxService.Ingest(ctx, &InboundEmail{
			MessageID:   msg,
			Recipient:   local + "@" + testInboxDomain,
			From:        ownerEmailFor(ownerID),
			Subject:     "Lunch receipt",
			Attachments: []InboundAttachment{inboundFile("receipt.pdf", samplePDF())},
		})
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if out.Status != "processed" || out.DraftsCreated != 1 {
			t.Fatalf("outcome: got %+v, want processed/1 draft", out)
		}
		if n := draftCountForOrg(t, ts, orgID); n != 1 {
			t.Fatalf("draft count: got %d, want 1", n)
		}
		// Claimant + recorder are both the inbox owner (v1 attribution).
		uid, createdBy := soleDraftClaimant(t, ts, orgID)
		if uid != ownerID || createdBy != ownerID {
			t.Errorf("claimant/creator: got user=%s creator=%s, want both %s", uid, createdBy, ownerID)
		}
		if status, drafts, sender := eventByMessageID(t, ts, msg); status != "processed" || drafts != 1 || sender != ownerEmailFor(ownerID) {
			t.Errorf("event: got status=%s drafts=%d sender=%s", status, drafts, sender)
		}
	})

	t.Run("on behalf: a colleague forwards into another member's inbox", func(t *testing.T) {
		orgID, ownerID, local := newCaptureOrg(t, ts)
		colleague := newMemberUser(t, ts, orgID) // active member of the same org

		msg := newMessageID(t, ts)
		out, err := ts.emailInboxService.Ingest(ctx, &InboundEmail{
			MessageID:   msg,
			Recipient:   local + "@" + testInboxDomain, // the OWNER's inbox
			From:        memberEmailFor(colleague),     // sent by the colleague
			Attachments: []InboundAttachment{inboundFile("receipt.pdf", samplePDF())},
		})
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if out.Status != "processed" {
			t.Fatalf("status: got %q, want processed", out.Status)
		}
		// The expense is the INBOX OWNER's, regardless of who sent it.
		uid, _ := soleDraftClaimant(t, ts, orgID)
		if uid != ownerID {
			t.Errorf("claimant: got %s, want the inbox owner %s", uid, ownerID)
		}
		// The actual submitter is preserved in the audit row.
		if _, _, sender := eventByMessageID(t, ts, msg); sender != memberEmailFor(colleague) {
			t.Errorf("event sender: got %s, want %s", sender, memberEmailFor(colleague))
		}
	})

	t.Run("multiple attachments create multiple drafts", func(t *testing.T) {
		orgID, ownerID, local := newCaptureOrg(t, ts)

		msg := newMessageID(t, ts)
		out, err := ts.emailInboxService.Ingest(ctx, &InboundEmail{
			MessageID: msg,
			Recipient: local + "@" + testInboxDomain,
			From:      ownerEmailFor(ownerID),
			Attachments: []InboundAttachment{
				inboundFile("a.pdf", samplePDF()),
				inboundFile("b.jpg", sampleJPEG()),
			},
		})
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if out.DraftsCreated != 2 {
			t.Errorf("drafts: got %d, want 2", out.DraftsCreated)
		}
		if n := draftCountForOrg(t, ts, orgID); n != 2 {
			t.Errorf("draft count: got %d, want 2", n)
		}
	})

	t.Run("unsupported file is skipped, valid one still captured", func(t *testing.T) {
		orgID, ownerID, local := newCaptureOrg(t, ts)

		msg := newMessageID(t, ts)
		out, err := ts.emailInboxService.Ingest(ctx, &InboundEmail{
			MessageID: msg,
			Recipient: local + "@" + testInboxDomain,
			From:      ownerEmailFor(ownerID),
			Attachments: []InboundAttachment{
				inboundFile("note.txt", sampleText()), // not a receipt → skipped
				inboundFile("receipt.pdf", samplePDF()),
			},
		})
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if out.Status != "processed" || out.DraftsCreated != 1 {
			t.Errorf("outcome: got %+v, want processed/1", out)
		}
		if n := draftCountForOrg(t, ts, orgID); n != 1 {
			t.Errorf("draft count: got %d, want 1 (only the PDF should be captured)", n)
		}
	})

	t.Run("HTML-body receipt is rendered to a PDF and captured", func(t *testing.T) {
		orgID, ownerID, local := newCaptureOrg(t, ts)

		msg := newMessageID(t, ts)
		out, err := ts.emailInboxService.Ingest(ctx, &InboundEmail{
			MessageID: msg,
			Recipient: local + "@" + testInboxDomain,
			From:      ownerEmailFor(ownerID),
			Subject:   "Your Uber receipt",
			BodyHTML:  "<html><body><h1>Receipt</h1><p>£12.34</p></body></html>",
			// no file attachments
		})
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if out.Status != "processed" || out.DraftsCreated != 1 {
			t.Fatalf("outcome: got %+v, want processed/1 (rendered body)", out)
		}
		// The captured file is the rendered body.
		var fileName string
		if err := ts.pool.QueryRow(ctx,
			`SELECT file_name FROM expense_attachments WHERE organisation_id = $1`, orgID).Scan(&fileName); err != nil {
			t.Fatalf("read attachment: %v", err)
		}
		if fileName != "email-body.pdf" {
			t.Errorf("file name: got %q, want email-body.pdf", fileName)
		}
	})

	t.Run("duplicate delivery (Mailgun retry) creates no second draft", func(t *testing.T) {
		orgID, ownerID, local := newCaptureOrg(t, ts)

		email := &InboundEmail{
			MessageID:   newMessageID(t, ts),
			Recipient:   local + "@" + testInboxDomain,
			From:        ownerEmailFor(ownerID),
			Attachments: []InboundAttachment{inboundFile("receipt.pdf", samplePDF())},
		}
		if _, err := ts.emailInboxService.Ingest(ctx, email); err != nil {
			t.Fatalf("first Ingest: %v", err)
		}
		out, err := ts.emailInboxService.Ingest(ctx, email) // same Message-Id again
		if err != nil {
			t.Fatalf("second Ingest: %v", err)
		}
		if !out.Duplicate {
			t.Errorf("second delivery should be flagged Duplicate, got %+v", out)
		}
		if n := draftCountForOrg(t, ts, orgID); n != 1 {
			t.Errorf("draft count after retry: got %d, want 1", n)
		}
	})

	t.Run("same file re-sent as a NEW email (distinct Message-Id) is deduped", func(t *testing.T) {
		orgID, ownerID, local := newCaptureOrg(t, ts)
		send := func() (IngestOutcome, error) {
			return ts.emailInboxService.Ingest(ctx, &InboundEmail{
				MessageID:   newMessageID(t, ts), // a DIFFERENT Message-Id each send
				Recipient:   local + "@" + testInboxDomain,
				From:        ownerEmailFor(ownerID),
				Attachments: []InboundAttachment{inboundFile("receipt.pdf", samplePDF())},
			})
		}

		first, err := send()
		if err != nil {
			t.Fatalf("first send: %v", err)
		}
		if first.Status != "processed" || first.DraftsCreated != 1 {
			t.Fatalf("first send: got %+v, want processed/1", first)
		}

		second, err := send() // identical file bytes, fresh Message-Id
		if err != nil {
			t.Fatalf("second send: %v", err)
		}
		if second.Status != "ignored_duplicate" || second.DraftsCreated != 0 {
			t.Errorf("second send: got %+v, want ignored_duplicate/0", second)
		}
		if n := draftCountForOrg(t, ts, orgID); n != 1 {
			t.Errorf("draft count after re-send: got %d, want 1 (no duplicate)", n)
		}
	})

	t.Run("a DIFFERENT file to the same inbox is not deduped", func(t *testing.T) {
		orgID, ownerID, local := newCaptureOrg(t, ts)

		if _, err := ts.emailInboxService.Ingest(ctx, &InboundEmail{
			MessageID:   newMessageID(t, ts),
			Recipient:   local + "@" + testInboxDomain,
			From:        ownerEmailFor(ownerID),
			Attachments: []InboundAttachment{inboundFile("a.pdf", samplePDF())},
		}); err != nil {
			t.Fatalf("first send: %v", err)
		}

		out, err := ts.emailInboxService.Ingest(ctx, &InboundEmail{
			MessageID:   newMessageID(t, ts),
			Recipient:   local + "@" + testInboxDomain,
			From:        ownerEmailFor(ownerID),
			Attachments: []InboundAttachment{inboundFile("b.png", samplePNG())}, // different bytes
		})
		if err != nil {
			t.Fatalf("second send: %v", err)
		}
		if out.Status != "processed" || out.DraftsCreated != 1 {
			t.Errorf("different file: got %+v, want processed/1", out)
		}
		if n := draftCountForOrg(t, ts, orgID); n != 2 {
			t.Errorf("draft count: got %d, want 2 (two distinct files)", n)
		}
	})
}

// =============================================================================
// WEBHOOK HANDLER (signature)
// =============================================================================

// mailgunSignature computes Mailgun's HMAC over timestamp+token with the test key.
func mailgunSignature(timestamp, token string) string {
	mac := hmac.New(sha256.New, []byte(testMailgunSigningKey))
	mac.Write([]byte(timestamp + token))
	return hex.EncodeToString(mac.Sum(nil))
}

// mailgunWebhookRequest builds a multipart POST to the inbound webhook with the
// given fields and (optionally) one attachment file.
func mailgunWebhookRequest(t *testing.T, fields map[string]string, attachment []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	if attachment != nil {
		fw, err := w.CreateFormFile("attachment-1", "receipt.pdf")
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		_, _ = fw.Write(attachment)
	}
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/mailgun/inbound", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func TestEmailInboxWebhookSignature(t *testing.T) {
	t.Run("invalid signature is rejected with 401 (no work done)", func(t *testing.T) {
		ts := newTestServer(t)
		defer ts.pool.Close()

		req := mailgunWebhookRequest(t, map[string]string{
			"timestamp":  "1700000000",
			"token":      "some-token",
			"signature":  "deadbeef", // wrong
			"recipient":  "alpha@" + testInboxDomain,
			"from":       "x@example.com",
			"Message-Id": newMessageID(t, ts),
		}, samplePDF())

		rec := httptest.NewRecorder()
		ts.server.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status: got %d, want 401 — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("valid signature is accepted and creates a draft", func(t *testing.T) {
		requireGCS(t)
		ts := newTestServer(t)
		defer ts.pool.Close()

		orgID, ownerID, local := newCaptureOrg(t, ts)

		const tsField, token = "1700000000", "real-token"
		msg := newMessageID(t, ts)
		req := mailgunWebhookRequest(t, map[string]string{
			"timestamp":  tsField,
			"token":      token,
			"signature":  mailgunSignature(tsField, token),
			"recipient":  local + "@" + testInboxDomain,
			"from":       ownerEmailFor(ownerID),
			"subject":    "Receipt",
			"Message-Id": msg,
		}, samplePDF())

		rec := httptest.NewRecorder()
		ts.server.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200 — body: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Status        string `json:"status"`
			DraftsCreated int    `json:"drafts_created"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp.Status != "processed" || resp.DraftsCreated != 1 {
			t.Errorf("body: got %+v, want processed/1", resp)
		}
		if n := draftCountForOrg(t, ts, orgID); n != 1 {
			t.Errorf("draft count: got %d, want 1", n)
		}
	})
}

// =============================================================================
// ADDRESS GENERATION (read-only, auto-created)
// =============================================================================

// newInboxOrgOwner inserts an org (name only, no slug) plus an active owner with
// the given name, so the address generator falls back to slugifying the name.
func newInboxOrgOwner(t *testing.T, ts *testServer, first, last, orgName string) (orgID, userID string) {
	t.Helper()
	ctx := context.Background()
	orgID = uuid.NewString()
	userID = uuid.NewString()
	if _, err := ts.pool.Exec(ctx, `INSERT INTO organisations (id, name) VALUES ($1, $2)`, orgID, orgName); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO users (id, email, first_name, last_name, is_active, email_verified_at)
		 VALUES ($1, $2, $3, $4, TRUE, now())`,
		userID, "owner-"+userID+"@test.local", first, last); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO organisation_memberships (organisation_id, user_id, role, status)
		 VALUES ($1, $2, 'owner', 'active')`, orgID, userID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(ctx, `DELETE FROM organisation_memberships WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM organisations WHERE id = $1`, orgID)
	})
	return orgID, userID
}

func TestEmailInboxAddress(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()
	ctx := context.Background()

	t.Run("generates a human-readable, accent-folded address and is idempotent", func(t *testing.T) {
		// Accented surname exercises the ASCII fold; "Acme Ltd" (no slug) exercises
		// the org-name fallback.
		orgID, ownerID := newInboxOrgOwner(t, ts, "Aydin", "Günal", "Acme Ltd")

		got, err := ts.emailInboxService.GetOrCreateInboxAddress(ctx, mustUUID(t, ownerID), mustUUID(t, orgID))
		if err != nil {
			t.Fatalf("GetOrCreateInboxAddress: %v", err)
		}
		want := "aydin.gunal.acme.ltd@" + testInboxDomain
		if got != want {
			t.Fatalf("address: got %q, want %q", got, want)
		}

		// Second call must return the SAME address (idempotent provisioning).
		again, err := ts.emailInboxService.GetOrCreateInboxAddress(ctx, mustUUID(t, ownerID), mustUUID(t, orgID))
		if err != nil {
			t.Fatalf("second GetOrCreateInboxAddress: %v", err)
		}
		if again != got {
			t.Errorf("address not stable: first %q, second %q", got, again)
		}

		// And it was persisted on the membership row.
		var stored string
		if err := ts.pool.QueryRow(ctx,
			`SELECT inbox_local_part FROM organisation_memberships WHERE organisation_id = $1 AND user_id = $2`,
			orgID, ownerID).Scan(&stored); err != nil {
			t.Fatalf("read stored local part: %v", err)
		}
		if stored != "aydin.gunal.acme.ltd" {
			t.Errorf("stored local part: got %q", stored)
		}
	})

	t.Run("GET /inbox-address returns the address for the caller", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/inbox-address", nil)
		req.Header.Set("Authorization", bearer(t, ts, ownerID, orgID))
		rec := httptest.NewRecorder()
		ts.server.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200 — body: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Enabled bool   `json:"enabled"`
			Address string `json:"address"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !resp.Enabled || resp.Address == "" {
			t.Fatalf("expected an enabled address, got %+v", resp)
		}
		if !bytes.HasSuffix([]byte(resp.Address), []byte("@"+testInboxDomain)) {
			t.Errorf("address %q should end with @%s", resp.Address, testInboxDomain)
		}
	})
}
