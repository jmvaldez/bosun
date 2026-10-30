package hook

import "os"

// appendLine writes one whole line with a single O_APPEND write, which is how
// concurrent hooks avoid interleaving.
func appendLine(dir string, line []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dir+"/events.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
