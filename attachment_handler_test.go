package main

// attachment_handler_test.go
// =============================================================================
// HTTP-layer integration tests for the attachment endpoints.
//
// attachment_service_test.go covers the SERVICE directly; this file drives the
// real Gin router (ts.server.router.ServeHTTP) so the HTTP plumbing is exercised
// too: multipart parsing, the request-body size cap (http.MaxBytesReader), route
// wiring, response envelopes, and kernel.AppError → HTTP status mapping.
//
// Like the service tests these need the real GCS dev bucket (requireGCS) and the
// shared dev DB (newTestServer); they skip cleanly when GCS_BUCKET is unset and
// clean up every object/row they create.
//
// Run: go test ./... -run TestAttachmentHandler -v
// =============================================================================

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	attachments "github.com/operationfb/accounting-saas/internal/attachments"
	expenses "github.com/operationfb/accounting-saas/internal/expenses"
)

// =============================================================================
// HELPERS
// =============================================================================

// multipartFile builds a multipart/form-data body with one "file" part (the
// given filename + bytes) plus any extra text fields. It returns the body and
// the matching Content-Type header (which carries the boundary).
func multipartFile(t *testing.T, filename string, data []byte, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

// uploadRequest fires a multipart upload through the router with the given auth
// header (empty = none) and returns the recorder, without asserting anything.
func uploadRequest(t *testing.T, ts *testServer, authHeader, expenseID, filename string, data []byte, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := multipartFile(t, filename, data, fields)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses/"+expenseID+"/attachments", body)
	req.Header.Set("Content-Type", contentType)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// decodeAttachment unwraps a {"attachment": {...}} envelope.
func decodeAttachment(t *testing.T, rec *httptest.ResponseRecorder) expenses.AttachmentResponse {
	t.Helper()
	var resp struct {
		Attachment expenses.AttachmentResponse `json:"attachment"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode attachment: %v — body: %s", err, rec.Body.String())
	}
	return resp.Attachment
}

// cleanupAttachment registers t.Cleanup that removes an attachment (row + GCS
// object) as the dev owner. Cleanup is best-effort: deleting an already-deleted
// attachment just returns a not-found error, which we ignore.
func cleanupAttachment(t *testing.T, ts *testServer, expenseID, attachmentID string) {
	t.Cleanup(func() {
		_ = ts.attachmentService.DeleteAttachment(
			context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID), expenseID, attachmentID)
	})
}

// uploadOK uploads as the dev owner, asserts 201, registers cleanup, and returns
// the decoded attachment.
func uploadOK(t *testing.T, ts *testServer, authHeader, expenseID, filename string, data []byte) expenses.AttachmentResponse {
	t.Helper()
	rec := uploadRequest(t, ts, authHeader, expenseID, filename, data, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload %s: expected 201, got %d — body: %s", filename, rec.Code, rec.Body.String())
	}
	att := decodeAttachment(t, rec)
	cleanupAttachment(t, ts, expenseID, att.ID)
	return att
}

// =============================================================================
// HAPPY PATH: UPLOAD + LIST
// =============================================================================

// TestAttachmentHandler_UploadAndList covers POST (multipart, with description)
// and GET list through the router.
func TestAttachmentHandler_UploadAndList(t *testing.T) {
	t.Parallel()
	requireGCS(t)
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })
	// Isolate under a throwaway org so this test is parallel-safe (shadows the shared dev seed).
	devOrgID, devUserID := newOrgWithOwner(t, ts)

	expenseID := createExpenseAs(t, ts, devUserID, devOrgID)
	authHeader := bearer(t, ts, devUserID, devOrgID)

	// POST a PDF with a description field in the same multipart body.
	rec := uploadRequest(t, ts, authHeader, expenseID, "receipt.pdf", samplePDF(),
		map[string]string{"description": "Jan taxi"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: expected 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
	att := decodeAttachment(t, rec)
	cleanupAttachment(t, ts, expenseID, att.ID)

	if !att.IsPrimary {
		t.Error("the first uploaded file must be primary")
	}
	if att.ContentType != "application/pdf" {
		t.Errorf("content_type: got %q, want application/pdf", att.ContentType)
	}
	if att.FileName != "receipt.pdf" {
		t.Errorf("file_name: got %q, want receipt.pdf", att.FileName)
	}
	if att.Description == nil || *att.Description != "Jan taxi" {
		t.Errorf("description: got %v, want \"Jan taxi\"", att.Description)
	}

	// GET the list — it should contain exactly the file we just uploaded.
	listRec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/expenses/"+expenseID+"/attachments", nil)
	req.Header.Set("Authorization", authHeader)
	ts.server.router.ServeHTTP(listRec, req)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d — body: %s", listRec.Code, listRec.Body.String())
	}
	var listResp struct {
		Attachments []expenses.AttachmentResponse `json:"attachments"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Attachments) != 1 || listResp.Attachments[0].ID != att.ID {
		t.Errorf("list: expected exactly the uploaded attachment, got %d items: %+v", len(listResp.Attachments), listResp.Attachments)
	}
}

// =============================================================================
// SET PRIMARY + DELETE
// =============================================================================

// TestAttachmentHandler_SetPrimaryAndDelete covers PATCH .../primary and DELETE.
func TestAttachmentHandler_SetPrimaryAndDelete(t *testing.T) {
	t.Parallel()
	requireGCS(t)
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })
	// Isolate under a throwaway org so this test is parallel-safe (shadows the shared dev seed).
	devOrgID, devUserID := newOrgWithOwner(t, ts)

	expenseID := createExpenseAs(t, ts, devUserID, devOrgID)
	authHeader := bearer(t, ts, devUserID, devOrgID)

	a1 := uploadOK(t, ts, authHeader, expenseID, "a1.pdf", samplePDF())
	a2 := uploadOK(t, ts, authHeader, expenseID, "a2.png", samplePNG())
	if !a1.IsPrimary || a2.IsPrimary {
		t.Fatalf("expected a1 primary and a2 not; got a1=%v a2=%v", a1.IsPrimary, a2.IsPrimary)
	}

	// PATCH: make a2 the primary.
	patchRec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPatch, "/api/v1/expenses/"+expenseID+"/attachments/"+a2.ID+"/primary", nil)
	req.Header.Set("Authorization", authHeader)
	ts.server.router.ServeHTTP(patchRec, req)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("set primary: expected 200, got %d — body: %s", patchRec.Code, patchRec.Body.String())
	}
	updated := decodeAttachment(t, patchRec)
	if !updated.IsPrimary || updated.ID != a2.ID {
		t.Errorf("set primary: expected a2 flagged primary, got id=%s primary=%v", updated.ID, updated.IsPrimary)
	}

	// DELETE a2 → 204 (a1 is promoted back to primary; both are cleaned up anyway).
	delRec := httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodDelete, "/api/v1/expenses/"+expenseID+"/attachments/"+a2.ID, nil)
	req.Header.Set("Authorization", authHeader)
	ts.server.router.ServeHTTP(delRec, req)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d — body: %s", delRec.Code, delRec.Body.String())
	}
}

// =============================================================================
// DOWNLOAD URL
// =============================================================================

// TestAttachmentHandler_Download covers GET .../download. It skips the URL
// assertion when the dev credentials can't sign (signing needs a service
// account), surfaced here as a 500 from the handler.
func TestAttachmentHandler_Download(t *testing.T) {
	t.Parallel()
	requireGCS(t)
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })
	// Isolate under a throwaway org so this test is parallel-safe (shadows the shared dev seed).
	devOrgID, devUserID := newOrgWithOwner(t, ts)

	expenseID := createExpenseAs(t, ts, devUserID, devOrgID)
	authHeader := bearer(t, ts, devUserID, devOrgID)
	att := uploadOK(t, ts, authHeader, expenseID, "r.pdf", samplePDF())

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet,
		"/api/v1/expenses/"+expenseID+"/attachments/"+att.ID+"/download", nil)
	req.Header.Set("Authorization", authHeader)
	ts.server.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusInternalServerError {
		t.Skipf("download URL needs a service-account signer: %s", rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("download: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	var dl struct {
		DownloadURL string `json:"download_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &dl); err != nil {
		t.Fatalf("decode download: %v", err)
	}
	if dl.DownloadURL == "" {
		t.Error("expected a non-empty download_url")
	}
}

// =============================================================================
// ERROR MAPPING THROUGH THE HANDLER
// =============================================================================

// TestAttachmentHandler_Errors checks the HTTP status mapping for the common
// failure modes, all driven through the router.
func TestAttachmentHandler_Errors(t *testing.T) {
	t.Parallel()
	requireGCS(t)
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })
	// Isolate under a throwaway org so this test is parallel-safe (shadows the shared dev seed).
	devOrgID, devUserID := newOrgWithOwner(t, ts)

	expenseID := createExpenseAs(t, ts, devUserID, devOrgID)
	authHeader := bearer(t, ts, devUserID, devOrgID)
	uploadURL := "/api/v1/expenses/" + expenseID + "/attachments"

	t.Run("missing file field returns 400", func(t *testing.T) {
		// A multipart body with only a text field — no "file" part.
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		_ = w.WriteField("description", "no file attached")
		_ = w.Close()

		rec := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, uploadURL, &buf)
		req.Header.Set("Content-Type", w.FormDataContentType())
		req.Header.Set("Authorization", authHeader)
		ts.server.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("oversized body returns 400", func(t *testing.T) {
		// Just over the hard request-body cap so http.MaxBytesReader trips during
		// multipart parsing, before the service ever sees the file.
		big := make([]byte, attachments.MaxUploadRequestBytes+1024)
		copy(big, samplePDF()) // a valid signature up front; size trips first anyway
		rec := uploadRequest(t, ts, authHeader, expenseID, "big.pdf", big, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unsupported media type returns 415", func(t *testing.T) {
		rec := uploadRequest(t, ts, authHeader, expenseID, "notes.txt", sampleText(), nil)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("expected 415, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("another organisation returns 404", func(t *testing.T) {
		orgB, userB := newOrgWithOwner(t, ts)
		rec := uploadRequest(t, ts, bearer(t, ts, userB, orgB), expenseID, "x.pdf", samplePDF(), nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("no token returns 401", func(t *testing.T) {
		rec := uploadRequest(t, ts, "", expenseID, "x.pdf", samplePDF(), nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}
