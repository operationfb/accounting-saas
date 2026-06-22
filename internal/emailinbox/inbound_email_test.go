package emailinbox

import "testing"

// TestInlineAttachmentNames is a pure unit test for the content-id-map parser.
// It lives in-package (the helper it exercises, inlineAttachmentNames, is
// unexported) — moved here from the root email_inbox_test.go in the Stage 3
// extraction, mirroring how internal/ocr keeps its pure-unit tests.
func TestInlineAttachmentNames(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"single inline", `{"<logo@x>":"attachment-2"}`, []string{"attachment-2"}},
		{"two inline", `{"<a>":"attachment-1","<b>":"attachment-3"}`, []string{"attachment-1", "attachment-3"}},
		{"empty string", ``, nil},
		{"empty object", `{}`, nil},
		{"garbage", `not json`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := inlineAttachmentNames(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("size: got %d %v, want %d %v", len(got), got, len(c.want), c.want)
			}
			for _, w := range c.want {
				if !got[w] {
					t.Errorf("missing %q in %v", w, got)
				}
			}
		})
	}
}
