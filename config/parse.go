package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"gopkg.in/yaml.v3"
)

// Strict decoding (R-b, config review): an unknown key is an error,
// not a silent no-op. A declarative file states operator intent —
// a typo (`retenton:`) or a removed key (the old
// perStageVerification) must be said out loud, or the intent silently
// diverges from reality.
func DecodeYAML(data []byte, c *Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // empty document → zero Config, same as before
		}
		return err
	}
	return nil
}

func DecodeJSON(data []byte, c *Config) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(c)
}
