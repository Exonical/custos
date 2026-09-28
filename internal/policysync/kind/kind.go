// Package kind holds the policy-sync workqueue kind so producers that
// policysync itself depends on can enqueue without an import cycle.
package kind

// PolicySync is the workqueue kind consumed by the policy drift worker.
const PolicySync = "policy.sync"
