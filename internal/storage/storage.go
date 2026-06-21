package storage

// storage.go
// =============================================================================
// Storage — the abstraction over the object store that holds receipt files.
//
// Why an interface instead of using the GCS client directly?
//   - The service layer should depend on *what* we do with files (put bytes,
//     sign a short-lived download URL, delete) — not on the concrete Google SDK.
//     That keeps the business logic readable and means a future move to S3 /
//     Azure Blob is "write one new implementation of this interface", not a
//     rewrite of the service.
//   - It documents, in one place, the entire surface area the rest of the app
//     uses to talk to blob storage.
//
// The golden rule for this feature — what lives WHERE:
//   - The file BYTES live in the object store (GCS) — see gcs.go.
//   - The file METADATA (name, size, content type, the storage *key*, which
//     expense it belongs to, who uploaded it) lives in PostgreSQL, in the
//     expense_attachments table.
// We never put file bytes in the database, and we never store a signed URL in
// the database (those are short-lived and generated on demand).
// =============================================================================

import (
	"context"
	"io"
	"time"
)

// Storage is the set of operations the application needs from blob storage.
// The only implementation is gcsStorage (gcs.go); tests exercise that same
// implementation against the dev bucket.
type Storage interface {
	// Upload writes r to the object identified by key, tagging it with
	// contentType (so a later download serves the right MIME type). key is a
	// path-like object name we choose server-side, e.g.
	// "orgs/<org>/expenses/<expense>/<uuid>.pdf".
	Upload(ctx context.Context, key, contentType string, r io.Reader) error

	// SignedDownloadURL returns a time-limited HTTPS URL that grants read access
	// to the object at key for ttl. The bucket itself stays private; this URL is
	// the only way a client gets the bytes, and it expires. We generate these on
	// demand and never persist them.
	SignedDownloadURL(ctx context.Context, key string, ttl time.Duration) (string, error)

	// Delete removes the object at key. Used to clean up after a failed metadata
	// write (orphan prevention) and when an attachment is deleted. Deleting a
	// key that does not exist is treated as success (idempotent cleanup).
	Delete(ctx context.Context, key string) error

	// Download opens the object at key for reading; the caller MUST Close the
	// returned reader. The OCR worker uses this to re-read a receipt's bytes from
	// storage: the original upload's reader is tied to the HTTP request, which is
	// long gone by the time the (async, background) OCR runs. Unlike Delete, a
	// missing object here is a genuine error — we expected to read it.
	Download(ctx context.Context, key string) (io.ReadCloser, error)

	// Bucket returns the name of the bucket objects are stored in. We record it
	// in each attachment's storage_bucket column so a row always knows which
	// bucket its object lives in, even if the configured bucket changes later.
	Bucket() string
}
