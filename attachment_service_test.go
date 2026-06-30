package main

// attachment_service_test.go
// =============================================================================
// Integration tests for AttachmentService.
//
// Like the rest of the suite these hit REAL infrastructure — a real PostgreSQL
// (DATABASE_URL) AND the real Google Cloud Storage dev bucket (GCS_BUCKET).
// There is no fake/emulator: the same code path runs here and in production.
//
// To run them you need:
//   1. DATABASE_URL set (shared dev DB, schema applied) — as the other tests.
//   2. GCS_BUCKET set to the dev bucket, and GCP credentials that can read/write
//      it (ADC via `gcloud auth application-default login`, or
//      GOOGLE_APPLICATION_CREDENTIALS). Signed-URL generation additionally needs
//      a service-account *signer* (the download test skips if it can't sign).
//
// When GCS_BUCKET is unset every test here skips (see requireGCS), so
// `go test ./...` still passes on a machine without GCS access.
//
// Each test writes objects under a unique per-expense prefix and removes them in
// t.Cleanup so the shared bucket is left clean.
// =============================================================================

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	gcs "cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	auth "github.com/operationfb/accounting-saas/db/auth"
	dbcategories "github.com/operationfb/accounting-saas/db/categories"
	dbexpenses "github.com/operationfb/accounting-saas/db/expenses"
	attachments "github.com/operationfb/accounting-saas/internal/attachments"
	expenses "github.com/operationfb/accounting-saas/internal/expenses"
	kernel "github.com/operationfb/accounting-saas/internal/kernel"
	storage "github.com/operationfb/accounting-saas/internal/storage"
)

// =============================================================================
// HELPERS
// =============================================================================

// requireGCS skips the test unless the GCS dev bucket is configured.
func requireGCS(t *testing.T) {
	t.Helper()
	// Load .env up front: these tests call requireGCS BEFORE newTestServer (which
	// is what otherwise loads .env), and `go test -run TestAttachment` runs only
	// these tests — so without this the GCS_BUCKET defined in .env wouldn't be
	// visible yet and every test would skip. godotenv.Load is idempotent and
	// never overrides a variable already set in the real environment.
	_ = godotenv.Load()
	if os.Getenv("GCS_BUCKET") == "" {
		t.Skip("GCS_BUCKET not set — skipping attachment tests (they use the real dev bucket)")
	}
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	u, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("bad uuid %q: %v", s, err)
	}
	return u
}

// Sample file blobs. Only the leading bytes matter: http.DetectContentType
// recognises each by signature, and any bytes round-trip through GCS. They are
// deliberately NOT full valid documents — we're testing storage, not parsers.
func samplePDF() []byte { return []byte("%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF\n") }
func samplePNG() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, make([]byte, 32)...)
}
func sampleJPEG() []byte {
	return append([]byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F'}, make([]byte, 32)...)
}
func sampleText() []byte { return []byte("just some plain text, definitely not a receipt file") }

// expensePrefix is the storage-key prefix the service uses for one expense.
func expensePrefix(orgID, expenseID string) string {
	return "orgs/" + orgID + "/expenses/" + expenseID + "/"
}

// gcsObjectsUnder lists object names under a prefix in the dev bucket, using a
// raw client so a test can assert what really exists in storage.
func gcsObjectsUnder(t *testing.T, prefix string) []string {
	t.Helper()
	ctx := context.Background()
	client, err := gcs.NewClient(ctx)
	if err != nil {
		t.Fatalf("gcs client: %v", err)
	}
	defer client.Close()

	it := client.Bucket(os.Getenv("GCS_BUCKET")).Objects(ctx, &gcs.Query{Prefix: prefix})
	var keys []string
	for {
		obj, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			t.Fatalf("list objects under %q: %v", prefix, err)
		}
		keys = append(keys, obj.Name)
	}
	return keys
}

// uploadAs uploads one file through the service and registers cleanup that
// removes the row and its GCS object afterwards.
func uploadAs(t *testing.T, ts *testServer, callerID, orgID, expenseID, filename string, data []byte, desc *string) *expenses.AttachmentResponse {
	t.Helper()
	resp, err := ts.attachmentService.UploadAttachment(
		context.Background(), mustUUID(t, callerID), mustUUID(t, orgID),
		expenseID, filename, int64(len(data)), bytes.NewReader(data), desc,
	)
	if err != nil {
		t.Fatalf("uploadAs(%s): %v", filename, err)
	}
	t.Cleanup(func() {
		_ = ts.attachmentService.DeleteAttachment(
			context.Background(), mustUUID(t, callerID), mustUUID(t, orgID), expenseID, resp.ID,
		)
	})
	return resp
}

// newOrgWithOwner inserts an ephemeral organisation plus an active owner, with
// cleanup. Used to prove cross-tenant isolation with a genuine second tenant.
func newOrgWithOwner(t *testing.T, ts *testServer) (orgID, userID string) {
	t.Helper()
	ctx := context.Background()
	orgID = uuid.NewString()
	userID = uuid.NewString()
	email := "owner-" + userID + "@test.local"

	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO organisations (id, name) VALUES ($1, $2)`, orgID, "Test Org "+orgID); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO users (id, email, first_name, last_name, is_active, email_verified_at)
		 VALUES ($1, $2, 'Other', 'Owner', TRUE, now())`, userID, email); err != nil {
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

// assertAppCode fails unless err is an *kernel.AppError with the wanted code.
func assertAppCode(t *testing.T, err error, want kernel.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error with code %q, got nil", want)
	}
	if got := kernel.AsAppError(err).Code; got != want {
		t.Fatalf("error code: got %q, want %q (err: %v)", got, want, err)
	}
}

// assertPrimary checks the expense has exactly one primary attachment, with the
// expected id.
func assertPrimary(t *testing.T, ts *testServer, callerID, orgID, expenseID, wantPrimaryID string) {
	t.Helper()
	list, err := ts.attachmentService.ListAttachments(
		context.Background(), mustUUID(t, callerID), mustUUID(t, orgID), expenseID)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	var primaries []string
	for _, a := range list {
		if a.IsPrimary {
			primaries = append(primaries, a.ID)
		}
	}
	if len(primaries) != 1 || primaries[0] != wantPrimaryID {
		t.Fatalf("expected exactly one primary = %s, got %v", wantPrimaryID, primaries)
	}
}

// attachmentRowCount counts metadata rows for an expense via the (open) test pool.
func attachmentRowCount(t *testing.T, ts *testServer, expenseID string) int {
	t.Helper()
	var n int
	if err := ts.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM expense_attachments WHERE expense_id = $1`, expenseID).Scan(&n); err != nil {
		t.Fatalf("count attachments: %v", err)
	}
	return n
}

// =============================================================================
// HAPPY PATH + PRIMARY
// =============================================================================

// TestAttachmentUpload_HappyPath uploads a PDF then a PNG and checks the
// metadata, the first-file-is-primary rule, and that the bytes landed in GCS.
func TestAttachmentUpload_HappyPath(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()

	expenseID := createExpenseAs(t, ts, devUserID, devOrgID)

	pdf := uploadAs(t, ts, devUserID, devOrgID, expenseID, "receipt.pdf", samplePDF(), nil)
	if pdf.ContentType != "application/pdf" {
		t.Errorf("content_type: got %q, want application/pdf", pdf.ContentType)
	}
	if want := int32(len(samplePDF())); pdf.FileSizeBytes != want {
		t.Errorf("file_size_bytes: got %d, want %d", pdf.FileSizeBytes, want)
	}
	if pdf.FileName != "receipt.pdf" {
		t.Errorf("file_name: got %q, want receipt.pdf", pdf.FileName)
	}
	if !pdf.IsPrimary {
		t.Error("the first uploaded file must be primary")
	}

	png := uploadAs(t, ts, devUserID, devOrgID, expenseID, "receipt.png", samplePNG(), nil)
	if png.IsPrimary {
		t.Error("a second uploaded file must not be primary")
	}

	if keys := gcsObjectsUnder(t, expensePrefix(devOrgID, expenseID)); len(keys) != 2 {
		t.Errorf("expected 2 objects in GCS under the expense prefix, got %d: %v", len(keys), keys)
	}
}

// TestPrimaryAttachmentForPush covers the receipt fetch behind the FreeAgent push:
// it returns the PRIMARY attachment's real bytes + metadata, skips an oversized
// file via the metadata guard (before any download), and is org-scoped.
func TestPrimaryAttachmentForPush(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()
	ctx := context.Background()

	// bigEnough exceeds the tiny sample files, so the size guard is a no-op for the
	// happy path. A literal keeps this test off the freeagent package's constant.
	const bigEnough int64 = 10_000_000

	expenseID := createExpenseAs(t, ts, devUserID, devOrgID)
	// PDF first → it becomes the primary; the PNG is a secondary file.
	_ = uploadAs(t, ts, devUserID, devOrgID, expenseID, "receipt.pdf", samplePDF(), nil)
	_ = uploadAs(t, ts, devUserID, devOrgID, expenseID, "extra.png", samplePNG(), nil)

	org := mustUUID(t, devOrgID)
	exp := mustUUID(t, expenseID)

	t.Run("returns the PRIMARY attachment's bytes + metadata", func(t *testing.T) {
		data, name, ctype, found, err := ts.attachmentService.PrimaryAttachmentForPush(ctx, org, exp, bigEnough)
		if err != nil {
			t.Fatalf("PrimaryAttachmentForPush: %v", err)
		}
		if !found {
			t.Fatal("expected found=true")
		}
		if name != "receipt.pdf" || ctype != "application/pdf" {
			t.Errorf("metadata: got %q/%q, want receipt.pdf/application/pdf (the primary)", name, ctype)
		}
		if !bytes.Equal(data, samplePDF()) {
			t.Errorf("bytes did not round-trip from GCS: got %q", data)
		}
	})

	t.Run("oversized → skipped (found=false, no error, no download)", func(t *testing.T) {
		// maxBytes smaller than the file → the metadata guard skips it before download.
		data, _, _, found, err := ts.attachmentService.PrimaryAttachmentForPush(ctx, org, exp, 1)
		if err != nil {
			t.Fatalf("oversized: unexpected error: %v", err)
		}
		if found || data != nil {
			t.Errorf("oversized: expected found=false/nil, got found=%v len=%d", found, len(data))
		}
	})

	t.Run("no attachment → found=false", func(t *testing.T) {
		empty := createExpenseAs(t, ts, devUserID, devOrgID)
		_, _, _, found, err := ts.attachmentService.PrimaryAttachmentForPush(ctx, org, mustUUID(t, empty), bigEnough)
		if err != nil || found {
			t.Errorf("no attachment: expected found=false/nil err, got found=%v err=%v", found, err)
		}
	})

	t.Run("cross-tenant org → found=false (isolation)", func(t *testing.T) {
		otherOrg, _ := newOrgWithOwner(t, ts)
		_, _, _, found, err := ts.attachmentService.PrimaryAttachmentForPush(ctx, mustUUID(t, otherOrg), exp, bigEnough)
		if err != nil || found {
			t.Errorf("cross-tenant: expected found=false/nil err, got found=%v err=%v", found, err)
		}
	})
}

// TestAttachmentPrimary_SetAndPromote covers changing the primary and the
// promote-on-delete rule.
func TestAttachmentPrimary_SetAndPromote(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()

	expenseID := createExpenseAs(t, ts, devUserID, devOrgID)
	a1 := uploadAs(t, ts, devUserID, devOrgID, expenseID, "a1.pdf", samplePDF(), nil)
	a2 := uploadAs(t, ts, devUserID, devOrgID, expenseID, "a2.png", samplePNG(), nil)
	a3 := uploadAs(t, ts, devUserID, devOrgID, expenseID, "a3.jpg", sampleJPEG(), nil)

	if !a1.IsPrimary || a2.IsPrimary || a3.IsPrimary {
		t.Fatalf("expected only a1 primary; got a1=%v a2=%v a3=%v", a1.IsPrimary, a2.IsPrimary, a3.IsPrimary)
	}

	// Switch the primary to a2.
	updated, err := ts.attachmentService.SetPrimary(
		context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID), expenseID, a2.ID)
	if err != nil {
		t.Fatalf("SetPrimary: %v", err)
	}
	if !updated.IsPrimary {
		t.Error("SetPrimary should return the attachment flagged primary")
	}
	assertPrimary(t, ts, devUserID, devOrgID, expenseID, a2.ID)

	// Delete the primary (a2) → the oldest remaining (a1) is promoted.
	if err := ts.attachmentService.DeleteAttachment(
		context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID), expenseID, a2.ID); err != nil {
		t.Fatalf("DeleteAttachment: %v", err)
	}
	assertPrimary(t, ts, devUserID, devOrgID, expenseID, a1.ID)
}

// TestAttachmentList_OptionalEmpty confirms attachments are optional: an expense
// with no files lists cleanly as empty.
func TestAttachmentList_OptionalEmpty(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()

	expenseID := createExpenseAs(t, ts, devUserID, devOrgID)
	list, err := ts.attachmentService.ListAttachments(
		context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID), expenseID)
	if err != nil {
		t.Fatalf("ListAttachments on an expense with no files: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected an empty attachment list, got %d", len(list))
	}
}

// =============================================================================
// VALIDATION
// =============================================================================

// TestAttachmentUpload_RejectsBadType covers the content-type allowlist and the
// magic-byte sniff that defeats a spoofed extension. Neither writes to GCS.
func TestAttachmentUpload_RejectsBadType(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()

	expenseID := createExpenseAs(t, ts, devUserID, devOrgID)
	caller, org := mustUUID(t, devUserID), mustUUID(t, devOrgID)

	// Plain text is not an allowed receipt type.
	_, err := ts.attachmentService.UploadAttachment(
		context.Background(), caller, org, expenseID, "notes.txt",
		int64(len(sampleText())), bytes.NewReader(sampleText()), nil)
	assertAppCode(t, err, kernel.ErrCodeUnsupportedMediaType)

	// A spoofed extension (text bytes named .pdf) is caught by the sniff.
	_, err = ts.attachmentService.UploadAttachment(
		context.Background(), caller, org, expenseID, "evil.pdf",
		int64(len(sampleText())), bytes.NewReader(sampleText()), nil)
	assertAppCode(t, err, kernel.ErrCodeUnsupportedMediaType)

	if keys := gcsObjectsUnder(t, expensePrefix(devOrgID, expenseID)); len(keys) != 0 {
		t.Errorf("rejected uploads must not write to GCS, found %v", keys)
	}
}

// TestAttachmentUpload_RejectsTooLarge uses a service with a tiny cap so we can
// trip the limit without building a 20 MiB file.
func TestAttachmentUpload_RejectsTooLarge(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()

	expenseID := createExpenseAs(t, ts, devUserID, devOrgID)

	// 8-byte cap, sharing the configured real storage.
	tiny := attachments.NewService(ts.pool, dbexpenses.New(ts.pool), auth.New(ts.pool), dbcategories.New(ts.pool),
		ts.storage, nil, 8, 0)

	data := samplePDF() // comfortably larger than 8 bytes
	_, err := tiny.UploadAttachment(
		context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID),
		expenseID, "big.pdf", int64(len(data)), bytes.NewReader(data), nil)
	assertAppCode(t, err, kernel.ErrCodePayloadTooLarge)

	if keys := gcsObjectsUnder(t, expensePrefix(devOrgID, expenseID)); len(keys) != 0 {
		t.Errorf("an oversized upload must not write to GCS, found %v", keys)
	}
}

// =============================================================================
// TENANCY + AUTHORISATION
// =============================================================================

// TestAttachment_MultiTenantScoping proves a genuine member of another
// organisation cannot list, download, or delete this org's attachment.
func TestAttachment_MultiTenantScoping(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()

	expenseA := createExpenseAs(t, ts, devUserID, devOrgID)
	att := uploadAs(t, ts, devUserID, devOrgID, expenseA, "a.pdf", samplePDF(), nil)

	orgB, userB := newOrgWithOwner(t, ts)
	callerB, tenantB := mustUUID(t, userB), mustUUID(t, orgB)

	// The parent expense isn't in org B, so every access is a 404 for them.
	_, err := ts.attachmentService.ListAttachments(context.Background(), callerB, tenantB, expenseA)
	assertAppCode(t, err, kernel.ErrCodeNotFound)

	_, err = ts.attachmentService.GetDownloadURL(context.Background(), callerB, tenantB, expenseA, att.ID)
	assertAppCode(t, err, kernel.ErrCodeNotFound)

	err = ts.attachmentService.DeleteAttachment(context.Background(), callerB, tenantB, expenseA, att.ID)
	assertAppCode(t, err, kernel.ErrCodeNotFound)

	// ...and it survives for the real owner.
	assertPrimary(t, ts, devUserID, devOrgID, expenseA, att.ID)
}

// TestAttachment_Authorization proves a plain member who is neither the claimant
// nor an admin cannot read or write another user's attachments.
func TestAttachment_Authorization(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()

	expenseID := createExpenseAs(t, ts, devUserID, devOrgID)
	att := uploadAs(t, ts, devUserID, devOrgID, expenseID, "a.pdf", samplePDF(), nil)

	memberID := newMemberUser(t, ts, devOrgID)
	member, org := mustUUID(t, memberID), mustUUID(t, devOrgID)

	_, err := ts.attachmentService.ListAttachments(context.Background(), member, org, expenseID)
	assertAppCode(t, err, kernel.ErrCodeForbidden)

	_, err = ts.attachmentService.UploadAttachment(
		context.Background(), member, org, expenseID, "sneaky.pdf",
		int64(len(samplePDF())), bytes.NewReader(samplePDF()), nil)
	assertAppCode(t, err, kernel.ErrCodeForbidden)

	_, err = ts.attachmentService.GetDownloadURL(context.Background(), member, org, expenseID, att.ID)
	assertAppCode(t, err, kernel.ErrCodeForbidden)
}

// =============================================================================
// FAILURE HANDLING (CONNECTION LOSS + ORPHAN CLEANUP)
// =============================================================================

// TestAttachment_ConnectionLoss checks the service fails gracefully — with no
// stray metadata row — when the context is cancelled or the DB is unreachable.
func TestAttachment_ConnectionLoss(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()

	expenseID := createExpenseAs(t, ts, devUserID, devOrgID)

	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // already cancelled before the call
		_, err := ts.attachmentService.UploadAttachment(
			ctx, mustUUID(t, devUserID), mustUUID(t, devOrgID),
			expenseID, "x.pdf", int64(len(samplePDF())), bytes.NewReader(samplePDF()), nil)
		if err == nil {
			t.Fatal("expected an error when the context is already cancelled")
		}
		if n := attachmentRowCount(t, ts, expenseID); n != 0 {
			t.Errorf("expected 0 attachment rows after a cancelled upload, got %d", n)
		}
	})

	t.Run("database unreachable", func(t *testing.T) {
		badPool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Fatalf("open pool: %v", err)
		}
		badPool.Close() // every query now fails
		badSvc := attachments.NewService(badPool, dbexpenses.New(badPool), auth.New(badPool), dbcategories.New(badPool),
			ts.storage, nil, 0, 0)

		_, err = badSvc.UploadAttachment(
			context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID),
			expenseID, "x.pdf", int64(len(samplePDF())), bytes.NewReader(samplePDF()), nil)
		if err == nil {
			t.Fatal("expected an error when the database is unreachable")
		}
		if n := attachmentRowCount(t, ts, expenseID); n != 0 {
			t.Errorf("expected 0 attachment rows after a failed upload, got %d", n)
		}
	})
}

// poolClosingStorage uploads for real, then closes the DB pool — reproducing
// "the file landed in GCS but the metadata write then failed", which is exactly
// the orphan scenario the service must clean up after.
type poolClosingStorage struct {
	inner storage.Storage
	pool  *pgxpool.Pool
}

func (p *poolClosingStorage) Upload(ctx context.Context, key, contentType string, r io.Reader) error {
	if err := p.inner.Upload(ctx, key, contentType, r); err != nil {
		return err
	}
	p.pool.Close()
	return nil
}
func (p *poolClosingStorage) SignedDownloadURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return p.inner.SignedDownloadURL(ctx, key, ttl)
}
func (p *poolClosingStorage) Delete(ctx context.Context, key string) error {
	return p.inner.Delete(ctx, key)
}
func (p *poolClosingStorage) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	return p.inner.Download(ctx, key)
}
func (p *poolClosingStorage) Bucket() string { return p.inner.Bucket() }

// TestAttachment_OrphanCleanup verifies that when the metadata write fails after
// the bytes are uploaded, the orphaned object is removed from GCS.
func TestAttachment_OrphanCleanup(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()

	expenseID := createExpenseAs(t, ts, devUserID, devOrgID)

	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close) // Close is idempotent; the spy may already have closed it

	spy := &poolClosingStorage{inner: ts.storage, pool: pool}
	svc := attachments.NewService(pool, dbexpenses.New(pool), auth.New(pool), dbcategories.New(pool), spy, nil, 0, 0)

	_, err = svc.UploadAttachment(
		context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID),
		expenseID, "x.pdf", int64(len(samplePDF())), bytes.NewReader(samplePDF()), nil)
	if err == nil {
		t.Fatal("expected the upload to fail when the metadata write is severed")
	}
	if n := attachmentRowCount(t, ts, expenseID); n != 0 {
		t.Errorf("expected 0 attachment rows, got %d", n)
	}
	if keys := gcsObjectsUnder(t, expensePrefix(devOrgID, expenseID)); len(keys) != 0 {
		t.Errorf("orphaned object(s) left in GCS after a failed metadata write: %v", keys)
	}
}

// =============================================================================
// DOWNLOAD URL
// =============================================================================

// TestAttachment_DownloadURL checks a signed URL is produced and fetches the
// exact bytes, and that an unknown id is a 404. It skips if the dev credentials
// cannot sign (signing needs a service account, not plain user ADC).
func TestAttachment_DownloadURL(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()

	expenseID := createExpenseAs(t, ts, devUserID, devOrgID)
	att := uploadAs(t, ts, devUserID, devOrgID, expenseID, "r.pdf", samplePDF(), nil)

	url, err := ts.attachmentService.GetDownloadURL(
		context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID), expenseID, att.ID)
	if err != nil {
		t.Skipf("could not generate a signed URL (needs a service-account signer): %v", err)
	}
	if url == "" {
		t.Fatal("expected a non-empty signed URL")
	}

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET signed url: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed url returned %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, samplePDF()) {
		t.Error("downloaded bytes do not match the uploaded file")
	}

	// Unknown attachment id → 404.
	_, err = ts.attachmentService.GetDownloadURL(
		context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID), expenseID, uuid.NewString())
	assertAppCode(t, err, kernel.ErrCodeNotFound)
}
