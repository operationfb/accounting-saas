package main

// html_renderer.go
// =============================================================================
// HTMLRenderer — converts an HTML email body to a PDF so it can flow through the
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
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// HTMLRenderer converts a full HTML document to PDF bytes.
type HTMLRenderer interface {
	RenderPDF(ctx context.Context, html string) ([]byte, error)
}

// gotenbergRenderer renders via a Gotenberg service. baseURL is the service root
// (the GOTENBERG_URL config); RenderPDF posts to its Chromium HTML route.
type gotenbergRenderer struct {
	baseURL string
	client  *http.Client
}

// newGotenbergRenderer builds the renderer. The 30s timeout bounds a slow render
// so a stuck Gotenberg can't hang an inbound-email request indefinitely.
func newGotenbergRenderer(baseURL string) *gotenbergRenderer {
	return &gotenbergRenderer{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
	}
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
