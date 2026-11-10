package config

import "testing"

func TestPublicBindingRequiresDistinctStrongSecrets(t *testing.T) {
	for _, tc := range []struct {
		name, bind, sign, refresh string
		valid                     bool
	}{
		{"local_default", "127.0.0.1:8080", "", "", true},
		{"ipv6_local", "[::1]:8080", "", "", true},
		{"public_defaults", "0.0.0.0:8080", "", "", false},
		{"hostname_defaults", "office:8080", "", "", false},
		{"short", "0.0.0.0:8080", "short", "also-short", false},
		{"same", "0.0.0.0:8080", "aA0123456789abcdefghijklmnopqrstu", "aA0123456789abcdefghijklmnopqrstu", false},
		{"strong", "0.0.0.0:8080", "aA0123456789abcdefghijklmnopqrstu", "bB9876543210zyxwvutsrqponmlkjihgf", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LINKNESTX_BIND_ADDRESS", tc.bind)
			t.Setenv("LINKNESTX_JWT_SIGNING_KEY", tc.sign)
			t.Setenv("LINKNESTX_JWT_REFRESH_KEY", tc.refresh)
			_, err := Load()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}
