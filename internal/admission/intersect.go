package admission

import "slices"

// IntersectPolicies returns the most restrictive combination of a and
// b: numeric limits are the min of the set values (0 = unlimited),
// allow-lists intersect when both are set (nil = any), booleans AND.
// Used to compute effective policy = tenant ∩ project.
func IntersectPolicies(a, b ResourcePolicy) ResourcePolicy {
	out := a
	minI := func(dst *int, v int) {
		if *dst == 0 || (v > 0 && v < *dst) {
			*dst = v
		}
	}
	min64 := func(dst *int64, v int64) {
		if *dst == 0 || (v > 0 && v < *dst) {
			*dst = v
		}
	}
	if out.MaxWalltime == 0 || (b.MaxWalltime > 0 && b.MaxWalltime < out.MaxWalltime) {
		out.MaxWalltime = b.MaxWalltime
	}
	minI(&out.MaxNodes, b.MaxNodes)
	minI(&out.MaxTasks, b.MaxTasks)
	minI(&out.MaxCPUsPerTask, b.MaxCPUsPerTask)
	min64(&out.MaxMemoryPerNodeMiB, b.MaxMemoryPerNodeMiB)
	minI(&out.MaxGPUsPerJob, b.MaxGPUsPerJob)
	minI(&out.MaxArraySize, b.MaxArraySize)
	out.AllowExclusive = a.AllowExclusive && b.AllowExclusive
	out.AllowedPartitions = intersectList(a.AllowedPartitions, b.AllowedPartitions)
	out.AllowedQoS = intersectList(a.AllowedQoS, b.AllowedQoS)
	out.AllowedGPUTypes = intersectList(a.AllowedGPUTypes, b.AllowedGPUTypes)
	return out
}

// intersectList intersects two allow-lists; nil means "any".
func intersectList(a, b []string) []string {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := []string{}
	for _, v := range a {
		if slices.Contains(b, v) {
			out = append(out, v)
		}
	}
	return out
}
