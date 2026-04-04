// export_test.go exposes internal methods for testing via the _test package.
// This pattern (Exported*) is idiomatic Go for testing unexported methods
// without breaking the package boundary.
package scheduler

import corev1 "k8s.io/api/core/v1"

// ScoreNodePublic exposes the private scoreNode method for unit tests.
func (e *Extender) ScoreNodePublic(node *corev1.Node) int64 {
	return e.scoreNode(node)
}
