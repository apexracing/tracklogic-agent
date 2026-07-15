// Package workflow composes engine Agents into validated teams and stateful
// workflows. Configuration is expected to be immutable after registration or
// first use; each Workflow run receives isolated concurrent-safe State.
package workflow
