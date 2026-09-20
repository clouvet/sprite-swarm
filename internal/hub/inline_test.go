package hub

import "testing"

// contentText must return the exact text a turn shows up as in the transcript, so a
// post-death replay dedups against it. A mismatch here is what caused an attachment
// turn to be re-delivered on every compaction (the "same message three times" bug).
func TestContentText(t *testing.T) {
	// Plain string content → itself.
	if got := contentText("just text"); got != "just text" {
		t.Errorf("string content = %q, want %q", got, "just text")
	}
	// Content-block array → the text block(s) only (images contribute nothing).
	blocks := []map[string]interface{}{
		{"type": "image", "source": map[string]interface{}{"data": "..."}},
		{"type": "text", "text": "look at these\n\n[Attached file \"a.txt\" saved at /x/a.txt — read it with your tools to use its contents.]"},
	}
	want := "look at these\n\n[Attached file \"a.txt\" saved at /x/a.txt — read it with your tools to use its contents.]"
	if got := contentText(blocks); got != want {
		t.Errorf("array content = %q, want %q", got, want)
	}
	// Unknown shape → empty (never panics).
	if got := contentText(42); got != "" {
		t.Errorf("unknown content = %q, want empty", got)
	}
}
