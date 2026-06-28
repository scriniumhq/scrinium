// CustomIndex is the contract host-side index custom indexes
// satisfy. A custom index lives inside a StoreIndex backend,
// shares its transactions, and exposes its own read API to the
// host.
//
// Two-paragraph mental model:
//
// (1) Subscriptions. CustomIndexes declare which mutations they
// care about via Subscribe. The backend dispatches matching
// events into Apply WITHIN the same transaction as the main
// index write — so a custom index cannot drift from the main
// index state. A failure in Apply rolls the whole transaction
// back, including the main write.
//
// (2) Storage. CustomIndexes own no SQL, no DB handles, no
// migration code: they put bytes into a backend-agnostic
// Substrate keyed by (table, key). The backend translates
// to its own substrate. Tables are namespace-prefixed by
// custom index Name to prevent collisions between custom indexes.
//
// Contract spec: 3. Reference/09 CustomIndex and Search.md.
package customindex
