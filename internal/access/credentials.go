package access

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"projectmap/internal/jsonutil"
	"regexp"
)

const maxCredentialFile = 128 << 10

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Credential struct {
	ClientID   string `json:"client_id"`
	Credential string `json:"credential"`
}
type credentialFile struct {
	Version int          `json:"version"`
	Clients []Credential `json:"clients"`
}

func secret() string {
	var value [32]byte
	_, _ = rand.Read(value[:])
	return base64.RawURLEncoding.EncodeToString(value[:])
}

// Provision writes first-run credentials, refusing to overwrite an existing file.
// Credentials are never returned to logs/stdout; the owner reads the private file.
func Provision(path string, clientIDs []string) error {
	entries := make([]Credential, 0, len(clientIDs))
	seen := map[string]bool{}
	for _, id := range clientIDs {
		if !uuidPattern.MatchString(id) || seen[id] {
			return errors.New("invalid credential client identities")
		}
		seen[id] = true
		entries = append(entries, Credential{ClientID: id, Credential: secret()})
	}
	if len(entries) == 0 {
		return errors.New("no clients to provision")
	}
	body, err := json.MarshalIndent(credentialFile{Version: 1, Clients: entries}, "", "  ")
	if err != nil {
		return errors.New("cannot encode credentials")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("cannot create credential directory")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("cannot create credential file; check permissions and ensure it does not already exist")
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(body); err != nil {
		return errors.New("cannot write credentials")
	}
	if err := file.Sync(); err != nil {
		return errors.New("cannot persist credentials")
	}
	if err := file.Close(); err != nil {
		return errors.New("cannot close credentials")
	}
	committed = true
	return nil
}

// LoadCredentials returns only token digests in memory. Missing credentials leave
// authenticated endpoints disabled, allowing health checks before provisioning.
func LoadCredentials(path string) (map[[32]byte]string, error) {
	out := map[[32]byte]string{}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxCredentialFile {
		return nil, errors.New("credential file must be a private regular file (mode 0600 or stricter)")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read credential file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0077 != 0 {
		return nil, errors.New("credential file changed while opening or is not private")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxCredentialFile+1))
	if err != nil || len(body) > maxCredentialFile {
		return nil, errors.New("cannot read credential file")
	}
	var decoded credentialFile
	if err := jsonutil.Decode(body, &decoded); err != nil || decoded.Version != 1 || len(decoded.Clients) == 0 {
		return nil, errors.New("invalid credential file")
	}
	clients := map[string]bool{}
	for _, entry := range decoded.Clients {
		bytes, err := base64.RawURLEncoding.DecodeString(entry.Credential)
		hash := sha256.Sum256([]byte(entry.Credential))
		_, duplicate := out[hash]
		if err != nil || len(bytes) != 32 || !uuidPattern.MatchString(entry.ClientID) || clients[entry.ClientID] || duplicate {
			return nil, errors.New("invalid credential file")
		}
		clients[entry.ClientID] = true
		out[hash] = entry.ClientID
	}
	return out, nil
}
