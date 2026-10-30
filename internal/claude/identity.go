package claude

import (
	"encoding/json"
	"os"
	"time"

	"github.com/jmvaldez/bosun/internal/atomicfile"
)

// IdentityTTL bounds how often refresh asks claude who is logged in.
const IdentityTTL = time.Hour

// IdentityFile is identity.json.
type IdentityFile struct {
	V          int       `json:"v"`
	AccountKey string    `json:"account_key"`
	FetchedAt  time.Time `json:"fetched_at"`
}

// ReadIdentity loads identity.json; ok is false when it is missing or corrupt.
func ReadIdentity(path string) (IdentityFile, bool) {
	var id IdentityFile
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &id) != nil || id.AccountKey == "" {
		return IdentityFile{}, false
	}
	return id, true
}

// WriteIdentity caches key as the logged-in account, stamped with now.
func WriteIdentity(path, key string, now time.Time) error {
	b, err := json.Marshal(IdentityFile{V: 1, AccountKey: key, FetchedAt: now.UTC()})
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}
