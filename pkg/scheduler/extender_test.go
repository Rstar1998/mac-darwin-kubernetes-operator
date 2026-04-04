// Package scheduler_test contains unit tests for the scheduler extender.
package scheduler_test

import (
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	schedulerpkg "github.com/gpu-operator-mac/apple-gpu-operator/pkg/scheduler"
)

// scoreNode is tested via the exported Extender; we extract the score logic
// by constructing nodes with specific annotations/labels.

func newNode(name string, annotations, labels map[string]string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
			Labels:      labels,
		},
	}
}

func TestSchedulerExtender_ScoreOrdering(t *testing.T) {
	// A cool, idle node should score higher than a hot, busy node.
	coolIdle := newNode("cool-idle",
		map[string]string{
			"apple.com/thermal-state": "Nominal",
			"apple.com/gpu-util-pct": "5",
		},
		map[string]string{"apple.com/gpu-slots": "4"},
	)

	hotBusy := newNode("hot-busy",
		map[string]string{
			"apple.com/thermal-state": "Serious",
			"apple.com/gpu-util-pct": "90",
		},
		map[string]string{"apple.com/gpu-slots": "4"},
	)

	ext := schedulerpkg.NewExtender(logr.Discard(), nil)
	scoreCool := ext.ScoreNodePublic(&coolIdle)
	scoreHot  := ext.ScoreNodePublic(&hotBusy)

	if scoreCool <= scoreHot {
		t.Errorf("expected cool-idle (%d) to score higher than hot-busy (%d)",
			scoreCool, scoreHot)
	}
}

func TestSchedulerExtender_CriticalThermal(t *testing.T) {
	// A Critical node should have a very low score.
	critical := newNode("critical",
		map[string]string{
			"apple.com/thermal-state": "Critical",
			"apple.com/gpu-util-pct": "50",
		},
		map[string]string{"apple.com/gpu-slots": "4"},
	)
	ext := schedulerpkg.NewExtender(logr.Discard(), nil)
	score := ext.ScoreNodePublic(&critical)
	if score > 30 {
		t.Errorf("critical node score should be low, got %d", score)
	}
}

func TestSchedulerExtender_ScoreRange(t *testing.T) {
	// Scores must always be in [0, 100].
	nodes := []corev1.Node{
		newNode("n1", map[string]string{"apple.com/thermal-state": "Nominal", "apple.com/gpu-util-pct": "0"}, map[string]string{"apple.com/gpu-slots": "4"}),
		newNode("n2", map[string]string{"apple.com/thermal-state": "Critical", "apple.com/gpu-util-pct": "100"}, map[string]string{"apple.com/gpu-slots": "0"}),
		newNode("n3", map[string]string{}, map[string]string{}),
	}
	ext := schedulerpkg.NewExtender(logr.Discard(), nil)
	for _, node := range nodes {
		n := node
		s := ext.ScoreNodePublic(&n)
		if s < 0 || s > 100 {
			t.Errorf("node %s score %d out of [0,100]", node.Name, s)
		}
	}
}
