package tokenutil

import "testing"

func TestSplitCamelCase(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"ReadFile", []string{"Read", "File"}},
		{"HTTPServer", []string{"HTTP", "Server"}},
		{"parseJSON", []string{"parse", "JSON"}},
		{"simple", []string{"simple"}},
		{"ABC", []string{"ABC"}},
		{"HTMLParser", []string{"HTML", "Parser"}},
		{"ReadFileContents", []string{"Read", "File", "Contents"}},
		{"", nil},
		{"a", []string{"a"}},
		{"getHTTPSConnection", []string{"get", "HTTPS", "Connection"}},
	}

	for _, tt := range tests {
		got := SplitCamelCase(tt.input)
		if len(got) != len(tt.expected) {
			t.Errorf("SplitCamelCase(%q) = %v, want %v", tt.input, got, tt.expected)
			continue
		}
		for i := range got {
			if got[i] != tt.expected[i] {
				t.Errorf("SplitCamelCase(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.expected[i])
			}
		}
	}
}
