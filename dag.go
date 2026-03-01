package workflow

import "fmt"

// detectCycle returns a description of the first dependency cycle found, or "" if acyclic.
// Uses DFS with three-color marking (white/gray/black).
func detectCycle(steps []Step) string {
	deps := make(map[string][]string, len(steps))
	for _, s := range steps {
		deps[s.ID] = s.DependsOn
	}

	const (
		white = 0 // unvisited
		gray  = 1 // in progress
		black = 2 // done
	)
	color := make(map[string]int, len(steps))

	var dfs func(id string) string
	dfs = func(id string) string {
		color[id] = gray
		for _, dep := range deps[id] {
			if color[dep] == gray {
				return fmt.Sprintf("%s → %s", id, dep)
			}
			if color[dep] == white {
				if cycle := dfs(dep); cycle != "" {
					return cycle
				}
			}
		}
		color[id] = black
		return ""
	}

	for _, s := range steps {
		if color[s.ID] == white {
			if cycle := dfs(s.ID); cycle != "" {
				return cycle
			}
		}
	}
	return ""
}
