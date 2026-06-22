package htmlrender

// htmlrender.go
// =============================================================================
// Renderer — converts an HTML email body to a PDF so it can flow through the
// SAME receipt pipeline as a file attachment (bytes → GCS → Document AI). Many
// receipts (Uber, Amazon, SaaS invoices) arrive as an HTML body with no file.
//
// Why an interface (mirroring Storage / DocumentExtractor)?
//   - The service depends on WHAT we do (render HTML → PDF), not on a specific
//     renderer. The one implementation, gotenbergRenderer, POSTs to a Gotenberg
//     service (https://gotenberg.dev); tests fake the interface.
//   - Rendering arbitrary email HTML is an SSRF surface, so Gotenberg runs as a
//     separate, network-isolated service rather than in-process.
// =============================================================================

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"google.golang.org/api/idtoken"
)

// Renderer converts a full HTML document to PDF bytes.
type Renderer interface {
	RenderPDF(ctx context.Context, html string) ([]byte, error)
}

// gotenbergRenderer renders via a Gotenberg service. baseURL is the service root
// (the GOTENBERG_URL config); RenderPDF posts to its Chromium HTML route.
type gotenbergRenderer struct {
	baseURL string
	client  *http.Client
}

// NewGotenberg builds the renderer.
//
// Auth: when Gotenberg runs behind an authenticated Cloud Run service, every
// request must carry a Google-signed OIDC ID token whose audience is the service
// URL. idtoken.NewClient returns an *http.Client that mints + attaches that token
// automatically — via the runtime service account on Cloud Run/GCE, or the
// GOOGLE_APPLICATION_CREDENTIALS service-account key locally. For a public or
// localhost Gotenberg the extra header is simply ignored, so this is safe either
// way. If no usable credentials are found we fall back to a plain client (e.g.
// local dev with no ADC pointing at an unauthenticated Gotenberg).
//
// The 60s timeout bounds a slow render (incl. a Cloud Run cold start) so a stuck
// Gotenberg can't hang an inbound-email request indefinitely.
func NewGotenberg(baseURL string) *gotenbergRenderer {
	base := strings.TrimRight(baseURL, "/")

	client, err := idtoken.NewClient(context.Background(), base)
	if err != nil {
		log.Printf("gotenberg: no ID-token credentials (%v) — calling %s without auth", err, base)
		client = &http.Client{}
	}
	client.Timeout = 60 * time.Second

	return &gotenbergRenderer{baseURL: base, client: client}
}

// RenderPDF sends the HTML to Gotenberg's /forms/chromium/convert/html route as a
// multipart form whose main file MUST be named index.html, and returns the PDF.
func (r *gotenbergRenderer) RenderPDF(ctx context.Context, html string) ([]byte, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	// Gotenberg requires the document to be uploaded as a file named index.html
	// under the "files" field.
	part, err := w.CreateFormFile("files", "index.html")
	if err != nil {
		return nil, fmt.Errorf("gotenberg: build form: %w", err)
	}
	if _, err := io.WriteString(part, html); err != nil {
		return nil, fmt.Errorf("gotenberg: write html: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("gotenberg: close form: %w", err)
	}

	url := r.baseURL + "/forms/chromium/convert/html"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return nil, fmt.Errorf("gotenberg: new request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gotenberg: do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Surface a little of Gotenberg's error body to aid debugging.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("gotenberg: status %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	pdf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gotenberg: read pdf: %w", err)
	}
	return pdf, nil
}
