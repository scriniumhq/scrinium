// Package ejector implements the Scrinium ejector agent:
// content-addressable materialisation of artifacts to a local cache.
//
// It is a plugin behind the agent registry (ADR-68): a blank import of
// this package registers its factory via register.go, after which the
// assembler builds it through agent.Build. The agent embeds
// agent.BaseState and satisfies the agent.Agent contract.
package ejector
