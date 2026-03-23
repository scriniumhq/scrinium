package core

// ArchiveStatus defines if a Payload is stored as-is or compressed.
type ArchiveStatus string

const (
	ArchiveStatusSkip     ArchiveStatus = "skip"
	ArchiveStatusArchived ArchiveStatus = "archived"
)

// ArchiveInfo holds compression details for transparent decompression.
type ArchiveInfo struct {
	Status       ArchiveStatus
	Hash         string
	Size         int64
	ChunkSize    int
	ChunkOffsets []int64
}

// BlobManifest is the immutable WORM representation of a physical payload in Scrinium.
type BlobManifest struct {
	Revision     string
	PayloadHash  string
	SizeBytes    int64
	Entropy      float64
	Archive      ArchiveInfo
	ManifestHash string
}
