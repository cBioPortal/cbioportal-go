package main

import "testing"

func TestDecodeDataURL(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"data:application/octet-stream;base64,U3R1ZHkgSUQJUGF0aWVudCBJRA==", "Study ID\tPatient ID"},
		{"data:text/plain,a%20b%0A", "a b\n"},
		{"data:,", ""},
	} {
		got, err := decodeDataURL(c.in)
		if err != nil || string(got) != c.want {
			t.Errorf("decodeDataURL(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"https://example.org/x", "data:text/plain;base64,%%%", "nocomma"} {
		if _, err := decodeDataURL(in); err == nil {
			t.Errorf("decodeDataURL(%q): no error", in)
		}
	}
}
