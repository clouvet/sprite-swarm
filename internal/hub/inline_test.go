package hub

import "testing"

func TestInlineableText(t *testing.T) {
	for _, mt := range []string{"text/plain", "text/markdown", "text/csv", "application/json", "application/jsonl"} {
		if !inlineableText(mt) {
			t.Errorf("%q should be inlineable", mt)
		}
	}
	for _, mt := range []string{"image/png", "application/pdf", "application/vnd.ms-excel", ""} {
		if inlineableText(mt) {
			t.Errorf("%q should NOT be inlineable", mt)
		}
	}
}
