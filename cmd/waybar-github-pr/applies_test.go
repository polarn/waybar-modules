package main

import "testing"

// stripComment is the one piece of this module with a rule subtle enough to
// break silently: get the quoting wrong and every line holding a URL loses
// everything from the "//" of "https://", which reads as a change and marks
// half the estate as needing an apply.
func TestStripComment(t *testing.T) {
	cases := []struct{ in, want string }{
		{`  index = 2 # bump this`, `index = 2`},
		{`url = "https://example.com/x" # note`, `url = "https://example.com/x"`},
		{`url = "https://example.com/x"`, `url = "https://example.com/x"`},
		{`# whole line`, ``},
		{`  // slash comment`, ``},
		{`a = "hash # inside string"`, `a = "hash # inside string"`},
		{`a = "escaped \" quote" # tail`, `a = "escaped \" quote"`},
		{`   spaced    out   = 1   `, `spaced out = 1`},
	}
	for _, c := range cases {
		if got := stripComment(c.in); got != c.want {
			t.Errorf("stripComment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The two directions that matter: a comment-only edit must not ask for an
// apply, and a real edit must never be swallowed.
func TestSubstantive(t *testing.T) {
	cosmetic := `@@ -7,7 +7,7 @@
   base_cidr_block = local.base_cidr_block
-  index           = 2 # Remember to bump this: https://gitlab.com/validio/wiki
+  index           = 2 # Bump when adding a VPC: https://github.com/validio-internal/handbook
 
   availability_zones = ["us-east-1a"]`
	if substantive(cosmetic) {
		t.Error("comment-only patch reported as substantive")
	}

	real := `@@ -7,7 +7,7 @@
-  index           = 2
+  index           = 3`
	if !substantive(real) {
		t.Error("real change reported as cosmetic")
	}

	// Anything we cannot read confidently must fail towards "needs applying".
	if !substantive("") {
		t.Error("absent patch must be treated as substantive")
	}
	if !substantive("@@ -1 +1 @@\n-/* block */\n+/* other */") {
		t.Error("block comments must be treated as substantive")
	}
}

func TestRootsForFile(t *testing.T) {
	roots := []string{"github", "google/monitoring-prod", "google/monitoring-uat", "aws/prod"}

	// .github/ is CI config, not the `github` terraform root.
	if got := rootsForFile(".github/workflows/apply.yml", roots); len(got) != 0 {
		t.Errorf(".github/ matched %v, want none", got)
	}
	if got := rootsForFile("github/org.tf", roots); len(got) != 1 || got[0] != "github" {
		t.Errorf("github/org.tf -> %v", got)
	}
	// Shared dashboard templates fan out to every monitoring root that
	// renders them, mirroring .github/scripts/changed-roots.sh.
	if got := rootsForFile("google/_dashboards/x.json.tftpl", roots); len(got) != 2 {
		t.Errorf("_dashboards fan-out -> %v, want 2 monitoring roots", got)
	}
}

func TestParseRunRoot(t *testing.T) {
	if got := parseRunRoot("terraform apply google/monitoring-prod (@polarn)"); got != "google/monitoring-prod" {
		t.Errorf("got %q", got)
	}
	if got := parseRunRoot("something else entirely"); got != "" {
		t.Errorf("unmatched title should yield empty, got %q", got)
	}
}
