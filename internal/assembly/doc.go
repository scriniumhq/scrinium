// Package composer turns a declarative YAML/JSON description of a
// Scrinium deployment into a assembly.Assembly. It is the
// recommended path for most users; the alternative is assembling the
// engine primitives by hand (see engine/store, engine/projection, …).
//
//	rt, err := composer.LoadYAML(ctx, yamlBytes)
//	if err != nil { return err }
//	defer rt.Close()
//	rt.Store().Put(ctx, artifact, opts)
//
// The config describes user intent — which stores, which policies, how
// to link them — not the engine's internal structure (wrappers,
// agents, pipeline stages), which composer derives from the policies.
// Full schema: 3. Reference/10 Composer.md.
//
// Secrets (passphrases, credentials, TLS material) are SecretRefs of
// the form "<scheme>:<value>", resolved at load time by the
// composer/secretref subpackage (file:/env:/plain: built in, custom
// schemes registerable).
//
// Extensions — drivers, indexes, pipeline stages, agents —
// register through the Register* functions, typically from an init()
// in a side-effect import.
//
// # Status (chunk R10)
//
// This chunk lands the declarative surface: the typed Config tree,
// SecretRef resolution, the five extension registries, defaulting,
// validation, Explain, and assembly of the single-store path the
// engine fully supports today (open/init/open-or-init, optional
// passphrase encryption in sealed/paranoid mode).
//
// Features whose components are not wired yet are parsed, defaulted,
// and validated, but rejected at build with errs.ErrNotImplemented and
// a pointer to the landing chunk: multistore (M4/S1), bundling
// (M4/S4), chunking (M5/C3), explicit pipelines and user
// agents (R11), and encrypted initialisation through composer
// (recovery-kit handoff, R11). The returned assembly.Assembly is itself
// the R10 seed of the fuller runtime (R11/R12): Store/Index/View and
// Close are live; Run and the surface/agent lookups are stubs.
package assembly
