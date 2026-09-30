package main

import "testing"

func TestScopeKeeps(t *testing.T) {
	owners := map[string]bool{"validio-internal": true, "validio-io": true}
	cases := []struct {
		scope string
		repo  string
		want  bool
	}{
		{"", "validio-internal/infra", true},
		{"", "polarn/flux", true},
		{"work", "validio-internal/infra", true},
		{"work", "Validio-IO/platform", true},
		{"work", "polarn/flux", false},
		{"work", "validio-internal-fork/x", false},
		{"personal", "polarn/flux", true},
		{"personal", "golang/go", true},
		{"personal", "validio-io/platform", false},
	}
	for _, c := range cases {
		if got := (scope{name: c.scope, owners: owners}).keeps(c.repo); got != c.want {
			t.Errorf("scope %q keeps(%q) = %v, want %v", c.scope, c.repo, got, c.want)
		}
	}
}
