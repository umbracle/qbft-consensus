package e2e

import (
	"testing"
)

func TestE2ENoIssue(t *testing.T) {
	c := newCluster(t, 3)
	c.all().start()
	c.all().waitForLiveness(t)
}
