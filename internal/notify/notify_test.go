package notify

import "testing"

func TestQuoteEscapesForAppleScript(t *testing.T) {
	got := quote(`say "hi" \ there`)
	want := `"say \"hi\" \\ there"`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRecording(t *testing.T) {
	r := &Recording{}
	_ = r.Notify("title", "message")
	if len(r.Sent) != 1 || r.Sent[0] != "title: message" {
		t.Fatalf("recording: %v", r.Sent)
	}
}

func TestNewReturnsNonNil(t *testing.T) {
	if New() == nil {
		t.Fatal("New must never return nil, even on unsupported platforms")
	}
}
