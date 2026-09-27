package admission

import "math"

// EstimateCost estimates reservation hours from frozen resources.
func EstimateCost(r ResolvedResources) map[string]float64 {
	hours := float64(r.WalltimeSeconds) / 3600
	cpusPerTask := max(r.CPUsPerTask, 1)
	tasks := r.Tasks
	if r.Nodes > 0 && r.TasksPerNode > 0 {
		tasks = max(tasks, r.Nodes*r.TasksPerNode)
	}
	tasks = max(tasks, r.Nodes, 1)
	cpus := tasks * cpusPerTask
	nodes := r.Nodes
	if nodes < 1 {
		nodes = 1
	}
	mult := 1
	if a := r.Array; a != nil {
		step := a.Step
		if step < 1 {
			step = 1
		}
		if a.End >= a.Start {
			mult = (a.End-a.Start)/step + 1
		}
	}
	return map[string]float64{"cpu_hours": float64(cpus*mult) * hours, "gpu_hours": float64(r.GPUCount*mult) * hours, "node_hours": float64(nodes*mult) * hours}
}

// CostMilli rounds a cost to allocation numeric(18,3) precision.
func CostMilli(v float64) float64 { return math.Round(v*1000) / 1000 }
