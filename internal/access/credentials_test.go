package access

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialProvisionAndPrivateLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "credentials.json")
	if err := Provision(path, []string{dashboardID, agentID}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("credentials are not private")
	}
	credentials, err := LoadCredentials(path)
	if err != nil || len(credentials) != 2 {
		t.Fatal("cannot load credential digests")
	}
	if err := Provision(path, []string{dashboardID}); err == nil {
		t.Fatal("credentials were overwritten")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("existing credentials changed")
	}
}
func TestCredentialInputFailuresDoNotLeak(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		mode       os.FileMode
	}{
		{"malformed", `{"credential":"sensitive-value`, 0600},
		{"unknown fields", `{"version":1,"clients":[],"secret":"sensitive-value"}`, 0600},
		{"public permissions", `{}`, 0644},
		{"invalid version", `{"version":2,"clients":[]}`, 0600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credentials.json")
			if err := os.WriteFile(path, []byte(tc.body), tc.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadCredentials(path); err == nil || strings.Contains(err.Error(), "sensitive-value") {
				t.Fatal("invalid credentials accepted or exposed")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "missing")
	if tokens, err := LoadCredentials(path); err != nil || len(tokens) != 0 {
		t.Fatal("missing credentials should leave API disabled")
	}
	if err := Provision(path, []string{"untrusted-client"}); err == nil {
		t.Fatal("invalid client ID accepted")
	}
}
func TestRejectDuplicateTokensAndSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	token := secret()
	body, _ := json.Marshal(credentialFile{Version: 1, Clients: []Credential{{ClientID: dashboardID, Credential: token}, {ClientID: agentID, Credential: token}}})
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(path); err == nil {
		t.Fatal("duplicate tokens accepted")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(link); err == nil {
		t.Fatal("credential symlink accepted")
	}
}
