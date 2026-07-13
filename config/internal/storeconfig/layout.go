package storeconfig

// Physical layout parameters: how paths, blobs and manifests are laid
// out and aligned on disk.

// PathTopology is the topology of paths inside a Location. Immutable.
type PathTopology string

const (
	PathTopologyFlat    PathTopology = "Flat"
	PathTopologySharded PathTopology = "Sharded"
)

// BlobStorage is the blob placement strategy.
type BlobStorage string

const (
	BlobStorageTarget BlobStorage = "Target"
	BlobStorageInline BlobStorage = "Inline"
)

// ManifestEncoding is the on-disk serialisation format of a manifest.
type ManifestEncoding string

const (
	ManifestEncodingJSON   ManifestEncoding = "JSON"
	ManifestEncodingBinary ManifestEncoding = "Binary"
)

// PackAlignmentPolicy is the alignment policy for blobs inside a pack.
type PackAlignmentPolicy int

const (
	PackAlignmentAuto PackAlignmentPolicy = -1
	PackAlignmentNone PackAlignmentPolicy = 0
	PackAlignment512  PackAlignmentPolicy = 512
	PackAlignment4096 PackAlignmentPolicy = 4096
)
