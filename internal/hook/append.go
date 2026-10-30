package hook

import (
	"os"

	"github.com/jmvaldez/bosun/internal/paths"
)

// appendLine writes one whole line with a single O_APPEND write, which is how
// concurrent hooks avoid interleaving.
func appendLine(p paths.Paths, id string, line []byte) error {
	if err := os.MkdirAll(p.Session(id), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p.Events(id), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
