package vfsmeta_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"scrinium.dev/domain/vfsmeta"
)

// FuzzDecode hardens the vfsmeta metadata decoder against arbitrary
// manifest-Ext bytes. Contract (TESTING.md category 1):
//   - Decode never panics on any input;
//   - anything it accepts (ok==true, no error) re-encodes and
//     re-decodes to byte-stable canonical output and a stable Path.
//
// Rejected input (err != nil) and foreign-schema input (ok==false) are
// both fine — the decoder is a probe, not a validator of the whole
// universe of JSON.
func FuzzDecode(f *testing.F) {
	if raw, err := vfsmeta.Embed(nil, vfsmeta.FileSystem{
		Path: "photos/2024/01/sunrise.jpg",
		MIME: "image/jpeg",
	}); err == nil {
		f.Add([]byte(raw))
	}
	f.Add([]byte(``))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"vfsmeta":{"version":1,"path":"a/b"}}`))
	f.Add([]byte(`{"vfsmeta":{"version":1,"path":"../escape"}}`))
	f.Add([]byte(`{"vfsmeta":{"version":1,"path":""}}`))
	f.Add([]byte(`{"vfsmeta":{"version":2,"path":"a"}}`))
	f.Add([]byte(`{"namespace":{"nsid":"ns-1"}}`))
	f.Add([]byte(`not json at all`))

	f.Fuzz(func(t *testing.T, data []byte) {
		fs, ok, err := vfsmeta.Decode(json.RawMessage(data))
		if err != nil || !ok {
			return // rejected or foreign schema — both acceptable
		}
		// Accepted payloads must round-trip to stable canonical bytes.
		raw1, err := vfsmeta.Embed(nil, fs)
		if err != nil {
			t.Fatalf("re-embed after successful Decode: %v", err)
		}
		fs2, ok2, err2 := vfsmeta.Decode(raw1)
		if err2 != nil || !ok2 {
			t.Fatalf("re-decode of own output failed: ok=%v err=%v", ok2, err2)
		}
		raw2, err := vfsmeta.Embed(nil, fs2)
		if err != nil {
			t.Fatalf("re-embed (round 2): %v", err)
		}
		if !bytes.Equal(raw1, raw2) {
			t.Fatalf("embed not stable across reparse:\n %s\n %s", raw1, raw2)
		}
		if fs2.Path != fs.Path {
			t.Fatalf("Path changed across reparse: %q -> %q", fs.Path, fs2.Path)
		}
	})
}
