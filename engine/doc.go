// Package engine implements Agent execution, bounded tool-call loops,
// streaming, per-Agent Tool scopes, and per-run options. Runs on the same
// stateful Agent are serialized; use distinct Agent and Memory instances for
// session isolation.
package engine
