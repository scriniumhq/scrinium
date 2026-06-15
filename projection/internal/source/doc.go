// Package source defines the contracts a projection reads from: the
// artifact Provider, the optional Metadata metadata provider, the path
// Resolver a host plugs in to map manifests onto a tree, and the Kind
// of backing store. It is a leaf — only domain and engine/store — so
// the view, the resolvers (fsmeta), and the index custom indexes
// (fsindex) depend on these contracts without depending on each
// other.
package source
