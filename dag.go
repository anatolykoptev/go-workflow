package workflow

import "fmt"

const (
	colorWhite = 0 // unvisited
	colorGray  = 1 // in progress
	colorBlack = 2 // done
)

type cycleDetector struct {
	deps  map[string][]string
	color map[string]int
}

// detectCycle returns a description of the first dependency cycle found, or "" if acyclic.
// Uses DFS with three-color marking (white/gray/black).
func detectCycle(steps []Step) string {
	cd := cycleDetector{
		deps:  make(map[string][]string, len(steps)),
		color: make(map[string]int, len(steps)),
	}
	for _, s := range steps {
		cd.deps[s.ID] = s.DependsOn
	}
	for _, s := range steps {
		if cd.color[s.ID] == colorWhite {
			if cycle := cd.visit(s.ID); cycle != "" {
				return cycle
			}
		}
	}
	return ""
}

func (cd *cycleDetector) visit(id string) string {
	cd.color[id] = colorGray
	for _, dep := range cd.deps[id] {
		switch cd.color[dep] {
		case colorGray:
			return fmt.Sprintf("%s → %s", id, dep)
		case colorWhite:
			if cycle := cd.visit(dep); cycle != "" {
				return cycle
			}
		}
	}
	cd.color[id] = colorBlack
	return ""
}
