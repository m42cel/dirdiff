package ui

import (
	"fmt"
	"testing"

	"github.com/m42cel/dirdiff/internal/diffmodel"
	"github.com/m42cel/dirdiff/internal/tree"
)

// benchTree builds a branch^depth tree of entirely equal files — the
// worst case for the filter, since no row anywhere matches "different"
// and the old subtree walk had to visit every node before it could say
// so.
func benchTree(branch, depth int) (*tree.Node, int) {
	root := &tree.Node{Type: diffmodel.Dir, Presence: diffmodel.Both, Listed: true}
	n := 0
	var fill func(p *tree.Node, d int)
	fill = func(p *tree.Node, d int) {
		for i := 0; i < branch; i++ {
			c := &tree.Node{
				Name:     fmt.Sprintf("entry-%04d", i),
				Presence: diffmodel.Both,
				Result:   diffmodel.Same,
			}
			if d > 0 {
				c.Type = diffmodel.Dir
				c.Listed = true
			}
			tree.AddChild(p, c)
			n++
			if d > 0 {
				fill(c, d-1)
			}
		}
	}
	fill(root, depth)
	return root, n
}

// BenchmarkFilterChildren guards the fix for the "high CPU while a
// filter is applied" bug: the cost of deciding what's visible must not
// depend on how many nodes are below the current directory, since the
// UI re-decides it for every visible row on every render — including
// the spinner tick, forever, on a fully scanned idle tree.
func BenchmarkFilterChildren(b *testing.B) {
	for _, depth := range []int{3, 4, 5} {
		root, n := benchTree(10, depth)
		b.Run(fmt.Sprintf("nodes-%d", n), func(b *testing.B) {
			filter := FilterSet{FilterDifferent: true}
			for i := 0; i < b.N; i++ {
				_ = filterChildren(root.Children, filter)
			}
		})
	}
}
