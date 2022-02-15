package main

import "testing"

// TestSanitizePortalURL is a unit test for the sanitizePortalURL helper
func TestSanitizePortalURL(t *testing.T) {
	cases := []struct {
		input  string
		output string
	}{
		{"https://siasky.net", "https://siasky.net"},
		{"https://siasky.net ", "https://siasky.net"},
		{" https://siasky.net ", "https://siasky.net"},
		{"https://siasky.net/", "https://siasky.net"},
		{"http://siasky.net", "https://siasky.net"},
		{"siasky.net", "https://siasky.net"},
	}

	// Test set cases to ensure known edge cases are always handled
	for _, test := range cases {
		res := sanitizePortalURL(test.input)
		if res != test.output {
			t.Fatalf("unexpected result, %v != %v", res, test.output)
		}
	}
}
