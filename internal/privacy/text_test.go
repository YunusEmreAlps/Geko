package privacy

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzText(f *testing.F) {
	f.Add("password=fixture", uint16(20))
	f.Add("line\r\n🇹🇷\u202e", uint16(17))
	f.Add(string([]byte{0xff, 0x00, 'a'}), uint16(16))
	f.Fuzz(func(t *testing.T, input string, size uint16) {
		limit := 16 + int(size)%4096
		got := Text(input, limit)
		if !utf8.ValidString(got) || len(got) > limit {
			t.Fatalf("invalid bounded text: %q", got)
		}
		if strings.HasPrefix(input, "password=") && got != "[redacted]" {
			t.Fatal("recognized sensitive text was retained")
		}
	})
}

func TestTextMasksPersonalData(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"no account for alice.smith+test@example.co.uk", "no account for [email]"},
		{"card 4111 1111 1111 1111 declined", "card [card] declined"},
		{"card 4111-1111-1111-1111", "card [card]"},
		{"pan=5555555555554444;", "pan=[card];"},
		{"iban TR33 0006 1005 1978 6457 8413 26 for order 42", "iban [iban] for order 42"},
		{"DE89370400440532013000 FAILED", "[iban] FAILED"},
		{"citizen 10000000146 not found", "citizen [tckn] not found"},
		// Invalid check digits, timestamps and IDs stay readable.
		{"order 4111111111111112", "order 4111111111111112"},
		{"at 1731600000000000000 ns", "at 1731600000000000000 ns"},
		{"citizen 10000000147", "citizen 10000000147"},
		{"TR33 0006 1005 1978 6457 8413 27", "TR33 0006 1005 1978 6457 8413 27"},
		{"trace 4bf92f3577b34da6a3ce929d0e0e4736", "trace 4bf92f3577b34da6a3ce929d0e0e4736"},
		{"user@localhost", "user@localhost"},
	} {
		if got := Text(tc.input, 1024); got != tc.want {
			t.Errorf("Text(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestTextMasksBeforeTruncating(t *testing.T) {
	if got := Text("card 4111111111111111", 12); got != "card [card]" {
		t.Fatalf("got %q", got)
	}
}
