package notifications

import "strings"

import "testing"

func TestSMTPMessagePlainWhenNoImage(t *testing.T) {
	msg := smtpMessage("printer@example.com", []string{"you@example.com"}, "Print finished", "success", "Your print is done.", nil)
	if strings.Contains(msg, "multipart/mixed") {
		t.Fatal("expected a plain text/plain message with no attachments")
	}
	if !strings.Contains(msg, "Content-Type: text/plain") {
		t.Fatal("expected text/plain content type")
	}
	if !strings.Contains(msg, "Your print is done.") {
		t.Fatal("body missing")
	}
}

func TestSMTPMessageEmbedsImageAttachment(t *testing.T) {
	// "QUJDREVG" is base64("ABCDEF").
	att := []string{"data:image/jpeg;base64,QUJDREVG"}
	msg := smtpMessage("printer@example.com", []string{"you@example.com"}, "Print finished", "success", "Photo of your finished print attached.", att)

	for _, want := range []string{
		"Content-Type: multipart/mixed; boundary=",
		"Content-Type: text/plain; charset=UTF-8",
		"Photo of your finished print attached.",
		"Content-Type: image/jpeg",
		"Content-Transfer-Encoding: base64",
		"Content-Disposition: inline; filename=\"snapshot-1.jpeg\"",
		"QUJDREVG",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("multipart message missing %q\n---\n%s", want, msg)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(msg), "--") {
		t.Fatal("multipart message must end with a closing boundary")
	}
}

func TestSMTPMessageSkipsNonDataURIAttachment(t *testing.T) {
	msg := smtpMessage("printer@example.com", []string{"you@example.com"}, "Print finished", "success", "body", []string{"https://example.com/snap.jpg"})
	if strings.Contains(msg, "multipart/mixed") {
		t.Fatal("a non-data-URI attachment cannot be embedded and must not produce a multipart email")
	}
}

func TestWrapBase64At76(t *testing.T) {
	long := strings.Repeat("A", 200)
	wrapped := wrapBase64(long)
	for _, line := range strings.Split(wrapped, "\r\n") {
		if len(line) > 76 {
			t.Fatalf("line exceeds 76 chars: %d", len(line))
		}
	}
	if strings.ReplaceAll(wrapped, "\r\n", "") != long {
		t.Fatal("wrapBase64 altered the payload")
	}
}
