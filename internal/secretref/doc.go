// Package secretref resolves the SecretRef strings that appear in a
// Scrinium configuration — passphrases, S3 credentials, TLS material —
// from their "<scheme>:<value>" form into raw bytes at load time.
//
// Built-in schemes:
//
//   - file:<path>   reads the file, trims trailing whitespace.
//   - env:<name>    os.Getenv(name); empty value is an error.
//   - plain:<value> the value verbatim. Tests only; masked in logs.
//
// Hosts register custom schemes (vault:, gsm:, …) through Register in
// an init(), after which the scheme works in YAML/JSON like the
// built-ins. See 3. Reference/10 Declarative Configuration.md §10.3.
package secretref
