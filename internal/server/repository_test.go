package server

import "testing"

func TestParseRepositoryHosts(t *testing.T) {
	for _, tc := range []struct{ input, host, owner, name string }{
		{"alice/site", "github.com", "alice", "site"},
		{"https://codeberg.org/alice/site.git", "codeberg.org", "alice", "site"},
		{"https://gitlab.com/group/subgroup/site", "gitlab.com", "group/subgroup", "site"},
		{"git@github.com:alice/site.git", "github.com", "alice", "site"},
	} {
		host, owner, name, err := parseRepo(tc.input)
		if err != nil || host != tc.host || owner != tc.owner || name != tc.name {
			t.Errorf("parseRepo(%q) = %q %q %q %v", tc.input, host, owner, name, err)
		}
	}
	for _, input := range []string{"https://evil.example/a/b", "https://user:secret@github.com/a/b", "https://github.com/a/../b", "http://github.com/a/b"} {
		if _, _, _, err := parseRepo(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}
