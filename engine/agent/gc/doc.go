// Package gc implements the Scrinium gc agent:
// garbage collection of unreferenced blobs.
//
// It is a plugin behind the agent registry (ADR-68): a blank import of
// this package registers its factory via register.go, after which the
// assembler builds it through agent.Build. The agent embeds
// agent.BaseState and satisfies the agent.Agent contract.
package gc
