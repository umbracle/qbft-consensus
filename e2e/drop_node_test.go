package e2e

import "testing"

func TestE2EDropNode(t *testing.T) {
	// after a successful start one of the nodes is dropped
	c := newCluster(t, 3)
	c.all().start()
	c.all().waitForLiveness(t)

	// drop a node
	node := c.get("node-1")
	node.drop()

	// the network should still be alive
	c.all().waitForLiveness(t)

	// reconnect the node and wait for it to be
	// live again (connect with the chain)
	node.start()
	node.waitForLiveness(t)
}
