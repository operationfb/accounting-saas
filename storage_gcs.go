package main

// storage_gcs.go
// =============================================================================
// gcsStorage — the Google Cloud Storage implementation of Storage.
//
// Credentials come from Application Default Credentials (ADC), so no key file is
// ever committed to the repo:
//   - locally:      `gcloud auth application-default login`, or a service-account
//                   key file pointed to by GOOGLE_APPLICATION_CREDENTIALS.
//   - on Cloud Run: the attached service account, automatically.
//
// One sharp edge worth knowing: generating signed URLs (SignedDownloadURL)
// requires credentials that can *sign* — i.e. a service account (a key file, or
// the IAM SignBlob API available to the Cloud Run service account). Plain user
// ADC from `gcloud auth application-default login` cannot sign on its own, so
// the download endpoint needs a service account in dev as well as prod.
// =============================================================================

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"cloud.google.com/go/storage"
)

// gcsStorage stores receipt files in a single Google Cloud Storage bucket.
type gcsStorage struct {
	client *storage.Client
	bucket string
}

// newGCSStorage builds a gcsStorage for the named bucket using ADC.
func newGCSStorage(ctx context.Context, bucket string) (*gcsStorage, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create GCS client: %w", err)
	}
	return &gcsStorage{client: client, bucket: bucket}, nil
}

// Bucket returns the configured bucket name.
func (g *gcsStorage) Bucket() string { return g.bucket }

// Upload streams r into the bucket at key, setting its Content-Type. The GCS
// writer buffers and uploads on Close; nothing is durably written until Close
// returns without error, so we surface Close errors to the caller.
func (g *gcsStorage) Upload(ctx context.Context, key, contentType string, r io.Reader) error {
	w := g.client.Bucket(g.bucket).Object(key).NewWriter(ctx)
	w.ContentType = contentType
	// ChunkSize=0 uses a single-request upload instead of GCS resumable uploads.
	// Resumable requires extra round-trips (initiate → upload-in-chunks → finalise)
	// which add meaningful latency for the small receipt files (≤20 MiB) we store.
	// If we ever raise the cap above ~100 MiB, switch back to a non-zero ChunkSize
	// so partial progress isn't lost on network interruption.
	w.ChunkSize = 0

	if _, err := io.Copy(w, r); err != nil {
		// Abort the in-flight upload; don't leave a half-written object.
		_ = w.Close()
		return fmt.Errorf("write object %q: %w", key, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("finalise object %q: %w", key, err)
	}
	return nil
}

// SignedDownloadURL returns a V4-signed GET URL valid for ttl. The ctx is part
// of the interface for future use; the underlying signer does not need it.
func (g *gcsStorage) SignedDownloadURL(_ context.Context, key string, ttl time.Duration) (string, error) {
	opts := &storage.SignedURLOptions{
		Scheme:  storage.SigningSchemeV4,
		Method:  "GET",
		Expires: time.Now().Add(ttl),
	}
	url, err := g.client.Bucket(g.bucket).SignedURL(key, opts)
	if err != nil {
		return "", fmt.Errorf("sign download URL for %q: %w", key, err)
	}
	return url, nil
}

// Download opens the object at key for streaming reads. The returned ReadCloser
// must be closed by the caller. A missing object surfaces as an error (the OCR
// worker expected the file to be there), unlike Delete which tolerates absence.
func (g *gcsStorage) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := g.client.Bucket(g.bucket).Object(key).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("open object %q for read: %w", key, err)
	}
	return r, nil
}

// Delete removes the object at key. A missing object is treated as success so
// that cleanup (orphan removal, attachment deletion) is idempotent.
func (g *gcsStorage) Delete(ctx context.Context, key string) error {
	err := g.client.Bucket(g.bucket).Object(key).Delete(ctx)
	if err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("delete object %q: %w", key, err)
	}
	return nil
}
