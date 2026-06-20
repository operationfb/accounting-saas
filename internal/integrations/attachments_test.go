package integrations

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// fakeAttachmentFetcher is a canned AttachmentFetcher — lets us unit-test
// AttachmentForPush (base64 + the found/204 logic) with no DB or GCS.
type fakeAttachmentFetcher struct {
	data        []byte
	fileName    string
	contentType string
	found       bool
	err         error
	gotMaxBytes int64 // records the size cap the Service passed through
}

func (f *fakeAttachmentFetcher) PrimaryAttachmentForPush(_ context.Context, _, _ uuid.UUID, maxBytes int64) ([]byte, string, string, bool, error) {
	f.gotMaxBytes = maxBytes
	return f.data, f.fileName, f.contentType, f.found, f.err
}

func TestAttachmentForPush(t *testing.T) {
	ctx := context.Background()
	org, exp := uuid.New(), uuid.New()

	t.Run("found → base64 with metadata, cap passed through", func(t *testing.T) {
		raw := []byte("%PDF-1.7 fake receipt bytes \x00\x01\x02") // includes non-text bytes
		fake := &fakeAttachmentFetcher{data: raw, fileName: "receipt.pdf", contentType: "application/pdf", found: true}
		svc := &Service{attachments: fake, maxAttachmentBytes: 5_000_000}

		resp, found, err := svc.AttachmentForPush(ctx, org, exp)
		if err != nil || !found {
			t.Fatalf("expected found, got found=%v err=%v", found, err)
		}
		// base64 must round-trip the EXACT bytes (asserted explicitly, like money).
		decoded, derr := base64.StdEncoding.DecodeString(resp.Data)
		if derr != nil {
			t.Fatalf("decode base64: %v", derr)
		}
		if string(decoded) != string(raw) {
			t.Errorf("round-trip: got %q, want %q", decoded, raw)
		}
		if resp.FileName != "receipt.pdf" || resp.ContentType != "application/pdf" {
			t.Errorf("metadata: got %q/%q", resp.FileName, resp.ContentType)
		}
		if fake.gotMaxBytes != 5_000_000 {
			t.Errorf("size cap pass-through: got %d, want 5000000", fake.gotMaxBytes)
		}
	})

	t.Run("not found → nil,false,nil (handler 204s)", func(t *testing.T) {
		svc := &Service{attachments: &fakeAttachmentFetcher{found: false}, maxAttachmentBytes: 5_000_000}
		resp, found, err := svc.AttachmentForPush(ctx, org, exp)
		if resp != nil || found || err != nil {
			t.Errorf("expected (nil,false,nil), got (%v,%v,%v)", resp, found, err)
		}
	})

	t.Run("nil fetcher → nil,false,nil (attachment push not wired)", func(t *testing.T) {
		svc := &Service{attachments: nil}
		resp, found, err := svc.AttachmentForPush(ctx, org, exp)
		if resp != nil || found || err != nil {
			t.Errorf("expected (nil,false,nil), got (%v,%v,%v)", resp, found, err)
		}
	})

	t.Run("fetcher error → wrapped error, not found", func(t *testing.T) {
		svc := &Service{attachments: &fakeAttachmentFetcher{err: errors.New("storage boom")}, maxAttachmentBytes: 5_000_000}
		resp, found, err := svc.AttachmentForPush(ctx, org, exp)
		if err == nil || found || resp != nil {
			t.Fatalf("expected an error with nil resp, got resp=%v found=%v err=%v", resp, found, err)
		}
	})
}
