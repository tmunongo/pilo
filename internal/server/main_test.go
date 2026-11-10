package server_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Some handler tests exercise deployments using example repository names.
// Apply this to the whole package, including tests with their own setup and
// background deployments that can outlive an individual test.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pilo-server-tests-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "no-gitconfig"))
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("GIT_TERMINAL_PROMPT", "0")
	os.Setenv("GIT_CONFIG_COUNT", "3")
	for i, host := range []string{"github.com", "gitlab.com", "codeberg.org"} {
		index := fmt.Sprint(i)
		os.Setenv("GIT_CONFIG_KEY_"+index, "url.file://"+filepath.Join(dir, "missing-repos")+"/.insteadOf")
		os.Setenv("GIT_CONFIG_VALUE_"+index, "https://"+host+"/")
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
