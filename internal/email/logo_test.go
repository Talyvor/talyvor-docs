package email

import (
	"strings"
	"testing"
)

// B57.83: Talyvor's own mark in every mail is the chosen logo's email PNG, 200 px wide on a light header;
// a workspace's own logo still wins.
func TestMailCarriesTheChosenLogo(t *testing.T) {
	s := NewSink(nil, nil).WithDefaultLogo("")
	plain := render(s.brandFor("", "", ""), `Dana asked you to approve "Runbook".`)
	for _, want := range []string{
		`<img src="https://docs.talyvor.com/talyvor-email-logo-on-light-400.png" alt="Talyvor" width="200" height="64"`,
		"background:#FFFFFF",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("unbranded mail lacks %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "email-logo.png") || strings.Contains(plain, "background:#060A12") {
		t.Fatalf("unbranded mail still carries today's header:\n%s", plain)
	}

	own := render(s.brandFor("Acme", "", "https://acme.example/logo.png"), "approve")
	if !strings.Contains(own, `<img src="https://acme.example/logo.png" alt="Acme" height="32"`) ||
		strings.Contains(own, "talyvor-email-logo") {
		t.Fatalf("a workspace's own logo does not win:\n%s", own)
	}
}
