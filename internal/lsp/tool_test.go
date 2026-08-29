package lspclient

import "testing"

func TestMatchWorkspaceGlobSupportsRecursiveDoubleStar(t *testing.T) {
	tests := []struct {
		pattern string
		value   string
		want    bool
	}{
		{pattern: "**/*.go", value: "main.go", want: true},
		{pattern: "**/*.go", value: "internal/lsp/tool.go", want: true},
		{pattern: "src/**/test_*.go", value: "src/test_unit.go", want: true},
		{pattern: "src/**/test_*.go", value: "src/pkg/test_unit.go", want: true},
		{pattern: "*.go", value: "internal/tool.go", want: false},
		{pattern: "**/*.go", value: "README.md", want: false},
	}
	for _, test := range tests {
		got, err := matchWorkspaceGlob(test.pattern, test.value)
		if err != nil {
			t.Fatalf("matchWorkspaceGlob(%q, %q) returned error: %v", test.pattern, test.value, err)
		}
		if got != test.want {
			t.Errorf("matchWorkspaceGlob(%q, %q) = %v, want %v", test.pattern, test.value, got, test.want)
		}
	}
}

func TestMatchWorkspaceGlobRejectsMalformedPattern(t *testing.T) {
	if _, err := matchWorkspaceGlob("**/[", "file.go"); err == nil {
		t.Fatal("malformed workspace glob was accepted")
	}
}
