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
