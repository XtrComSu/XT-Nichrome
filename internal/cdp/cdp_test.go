package cdp

import (
	"strings"
	"testing"
)

func TestProtocolErrorFormattingAndRingBuffer(t *testing.T) {
	pe1 := &ProtocolError{Code: -32000, Message: "Node is not an Element"}
	if got := pe1.Error(); got != "cdp -32000: Node is not an Element" {
		t.Fatalf("unexpected ProtocolError.Error(): %q", got)
	}
	pe2 := &ProtocolError{Code: -32602, Message: "Invalid parameters", Data: "ref missing"}
	if got := pe2.Error(); !strings.Contains(got, "ref missing") {
		t.Fatalf("unexpected ProtocolError.Error() with data: %q", got)
	}

	rb := newRingBuffer(16)
	_, _ = rb.Write([]byte("0123456789abcdefXYZ"))
	if got := rb.String(); got != "3456789abcdefXYZ" {
		t.Fatalf("ringBuffer overflow got %q, want 3456789abcdefXYZ", got)
	}
}
