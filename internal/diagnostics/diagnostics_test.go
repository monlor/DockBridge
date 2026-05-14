package diagnostics

import (
	"strings"
	"testing"
)

func TestRedactSecretLikeValues(t *testing.T) {
	r := NewRedactor()
	got := r.String("token=abc123 password=hunter2 /Users/me/.ssh/id_rsa normal")
	for _, forbidden := range []string{"abc123", "hunter2", "id_rsa"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("redacted string leaked %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "normal") {
		t.Fatalf("redacted string lost normal content: %s", got)
	}
}

func TestReporterRedactsDiagnosticFields(t *testing.T) {
	reporter := NewReporter(NewRedactor())
	reporter.Add("sync", map[string]string{
		"source": "/Users/me/project",
		"secret": "password=abc",
	})

	out := reporter.String()
	if strings.Contains(out, "abc") {
		t.Fatalf("diagnostics leaked secret: %s", out)
	}
	if !strings.Contains(out, "source=/Users/me/project") {
		t.Fatalf("diagnostics missing normal field: %s", out)
	}
}
