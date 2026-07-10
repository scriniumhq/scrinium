package assembly

import (
	"fmt"

	"scrinium.dev/engine/customindex"
	"scrinium.dev/extension"
	"scrinium.dev/present"
)

// extensions.go — the extension axis of assembly: installExtensions unions
// each extension's index/view/wrapper/agent contributions; wireExtensionEnv
// hands each stateful extension its scoped SystemStore once the store is open.

// installExtensions installs every extension (process-wide registry plus
// per-build WithExtension) as a whole, applying each part at its axis:
// custom indexes register into the index host now (so the first
// IndexManifest dispatches into them), provided views and schema
// presenters are unioned (a duplicate Root or schema key is rejected),
// wrapper factories and paired agents are collected for later phases. The
// assembler special-cases no extension; the projection seams below come
// from whichever extensions provide those views (ADR-63/88/98).
func (bs *buildState) installExtensions() error {
	bs.exts = append(globalRegistry.extensionList(), bs.opts.extensions...)
	for _, e := range bs.exts {
		bs.loadedExts = append(bs.loadedExts, e.Descriptor())
		if ci, ok := e.CustomIndex(); ok {
			if host, ok := bs.idx.(customindex.Host); ok {
				if err := host.CustomIndexes().Register(bs.ctx, ci); err != nil {
					return fmt.Errorf("scrinium: extension %q custom index: %w", e.Descriptor().Name, err)
				}
			}
			if vp, ok := ci.(customindex.ViewProvider); ok {
				for _, pv := range vp.ProvidedViews() {
					if _, dup := bs.providedViews[pv.Root]; dup {
						return fmt.Errorf("scrinium: view %q provided by more than one extension", pv.Root)
					}
					bs.providedViews[pv.Root] = pv
				}
			}
			if sp, ok := ci.(present.SchemaPresenter); ok {
				for _, sc := range sp.PresentedSchemas() {
					if _, dup := bs.presenters[sc.Key]; dup {
						return fmt.Errorf("scrinium: schema %q presented by more than one extension", sc.Key)
					}
					bs.presenters[sc.Key] = sc
				}
			}
		}
		if f, ok := e.Wrapper(); ok {
			bs.wrapFactories = append(bs.wrapFactories, f)
		}
		bs.extAgents = append(bs.extAgents, e.Agents()...)
	}
	return nil
}

// wireExtensionEnv hands each extension that keeps durable state a scoped
// SystemStore — its own "extension.<name>." slice of System() (ADR-100/101).
// A self-validating extension installs its veto on that scope (ADR-108).
// Extensions without durable state do not implement Receiver and are
// skipped, mirroring the index/wrapper/agent axes.
func (bs *buildState) wireExtensionEnv() error {
	for _, e := range bs.exts {
		r, ok := e.(extension.Receiver)
		if !ok {
			continue
		}
		var scopedOpts []extension.ScopedOption
		if val, ok := e.(extension.SystemArtifactValidator); ok {
			scopedOpts = append(scopedOpts, extension.WithValidator(val))
		}
		scoped, serr := extension.NewScopedSystemStore(e.Descriptor().Name, bs.st.System(), scopedOpts...)
		if serr != nil {
			return fmt.Errorf("scrinium: extension %q scoped system store: %w", e.Descriptor().Name, serr)
		}
		if uerr := r.UseEnv(extension.Env{SystemStore: scoped}); uerr != nil {
			return fmt.Errorf("scrinium: extension %q env: %w", e.Descriptor().Name, uerr)
		}
	}
	return nil
}
