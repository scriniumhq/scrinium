package fs

import (
	"scrinium.dev/engine/customindex"
	"scrinium.dev/extension"
)

// NewExtension returns the fs extension as one whole, backed by a fresh
// fsindex CustomIndex. Equivalent to ExtensionFor(NewIndex()).
func NewExtension() extension.Extension { return ExtensionFor(NewIndex()) }

// ExtensionFor wraps an existing fsindex as an extension.Extension, so
// the same CustomIndex instance can be both installed via extension.Use
// and handed to the projection (which consults it as a metadata source).
func ExtensionFor(ci *CustomIndex) extension.Extension { return fsExtension{ci: ci} }

// fsExtension is the fs extension: today it occupies only the index
// axis (the fsindex CustomIndex). Its Ingester/Extractor agents attach
// here once the agent axis is wired into the umbrella.
type fsExtension struct{ ci *CustomIndex }

func (e fsExtension) Descriptor() extension.Descriptor {
	return extension.Descriptor{Name: "fs"}
}

func (e fsExtension) CustomIndex() (customindex.CustomIndex, bool) {
	return e.ci, true
}
