package main

import "testing"

func TestResolveHostPort(t *testing.T) {
	cases := []struct {
		name     string
		httpAddr string
		wantHost string
		wantPort string
		wantErr  bool
	}{
		{"bare port", ":8080", "localhost", "8080", false},
		{"wildcard ipv4 host", "0.0.0.0:8080", "localhost", "8080", false},
		{"wildcard ipv6 host", "[::]:8080", "localhost", "8080", false},
		{"explicit loopback", "127.0.0.1:9090", "127.0.0.1", "9090", false},
		{"explicit hostname", "example.internal:8080", "example.internal", "8080", false},
		{"no port", "localhost", "", "", true},
		{"empty", "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, port, err := resolveHostPort(tc.httpAddr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveHostPort(%q) = %q, %q, <nil>, want error", tc.httpAddr, host, port)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveHostPort(%q) returned unexpected error: %v", tc.httpAddr, err)
			}
			if host != tc.wantHost || port != tc.wantPort {
				t.Errorf("resolveHostPort(%q) = %q, %q, want %q, %q", tc.httpAddr, host, port, tc.wantHost, tc.wantPort)
			}
		})
	}
}

func TestBaseURL(t *testing.T) {
	got, err := baseURL(":8080")
	if err != nil {
		t.Fatalf("baseURL returned unexpected error: %v", err)
	}
	if want := "http://localhost:8080"; got != want {
		t.Errorf("baseURL(%q) = %q, want %q", ":8080", got, want)
	}

	if _, err := baseURL("not-an-addr"); err == nil {
		t.Error("expected an error for a malformed HTTP_ADDR, got nil")
	}
}
