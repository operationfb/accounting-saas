package tala

// system.go
// =============================================================================
// The system prompt is kept in a sibling markdown file (tala_system.md) and
// embedded at build time. Keeping it as prose in its own file makes it easy to
// edit and review, and — because it is a stable, frozen prefix — it is a good
// candidate for prompt caching (see the CacheControl breakpoint in service.go).
// =============================================================================

import _ "embed"

//go:embed tala_system.md
var systemPrompt string
