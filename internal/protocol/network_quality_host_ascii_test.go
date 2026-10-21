package protocol

import "testing"

// Case folding must not turn a non-ASCII name into an ASCII one: only ASCII is accepted (design 5.2).
func TestQualityHostRejectsNonASCIIBeforeCaseFolding(t *testing.T) {
	for _, r := range []rune{0x212A, 0x0130, 0x017F, 0x00C9, 0xFF21} {
		for _, host := range []string{string(r) + ".example.com", "a" + string(r) + "b.example.com", "example.com" + string(r)} {
			if canonical, _, code := NormalizeQualityHost(host); code != QualityErrInvalidHost {
				t.Fatalf("%+q accepted as %q (code %q)", host, canonical, code)
			}
		}
	}
	for host, want := range map[string]string{"EXAMPLE.Com.": "example.com", "xn--bcher-kva.com": "xn--bcher-kva.com"} {
		if got, _, code := NormalizeQualityHost(host); code != "" || got != want {
			t.Fatalf("%q: %q %q", host, got, code)
		}
	}
}
