package e2e

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/umbracle/qbft-consensus"
)

var _ qbft.Backend = &nodeBackend{}

type node struct {
	logger  *log.Logger
	id      qbft.NodeID
	qbft    *qbft.QBFT
	nodes   []qbft.NodeID
	msgCh   chan *qbft.QBFTMessageWithRecipient
	sendMsg func(msg *qbft.QBFTMessageWithRecipient)
	history []*qbft.Block
	stopCh  chan struct{}
}

func (n *node) stop() {
	close(n.stopCh)
}

func (n *node) height() uint64 {
	return uint64(len(n.history))
}

func (n *node) deliverMessage(msg *qbft.QBFTMessageWithRecipient) {
	n.msgCh <- msg
}

func (n *node) Recv() chan *qbft.QBFTMessageWithRecipient {
	return n.msgCh
}

func (n *node) Send(msg *qbft.QBFTMessageWithRecipient) error {
	n.sendMsg(msg)
	return nil
}

type nodeBackend struct {
	id               qbft.NodeID
	height           uint64
	validatorSet     qbft.ValidatorSet
	insertedProposal *qbft.SealedProposal
}

func (n *nodeBackend) Height() uint64 {
	return n.height
}

func (n *nodeBackend) ValidatorSet() qbft.ValidatorSet {
	return n.validatorSet
}

func (n *nodeBackend) Insert(p *qbft.SealedProposal) error {
	n.insertedProposal = p
	return nil
}

func (n *nodeBackend) BuildProposal(round uint64) (*qbft.Block, []byte, error) {
	block := &qbft.Block{
		Height:      n.height,
		RoundNumber: round,
		Proposer:    n.id,
		Body:        []byte{0x1, 0x2, 0x3},
	}
	return block, []byte{0x4}, nil
}

func (n *nodeBackend) ValidateProposal(*qbft.Proposal) error {
	return nil
}

func (n *node) createBackend() *nodeBackend {
	fmt.Println("_ create backend _")
	fmt.Println(n.history)

	lastProposer := qbft.NodeID("")
	if len(n.history) > 0 {
		lastProposer = n.history[len(n.history)-1].Proposer
	}

	return &nodeBackend{
		id:           n.id,
		height:       n.height(),
		validatorSet: newValidatorSet(n.nodes, lastProposer),
	}
}

func (n *node) SignMessage(msg []byte) ([]byte, error) {
	return []byte(n.id), nil
}

func (n *node) RecoverSigner(msg, signature []byte) (qbft.NodeID, error) {
	return qbft.NodeID(signature), nil
}

func (n *node) start() {
	opts := []qbft.ConfigOption{
		qbft.WithSigner(n),
		qbft.WithLogger(n.logger),
		qbft.WithTransport(n),
	}
	n.qbft = qbft.New(n.id, opts...)
	go n.qbft.Start()

	for {
		b := n.createBackend()
		fmt.Printf("///////////////////// %d ///////////////////////////\n", b.height)

		n.qbft.SetBackend(b)
		resCh := n.qbft.Run()

		select {
		case <-resCh:
		case <-n.stopCh:
			n.qbft.Stop()
			return
		}

		// insert the block
		n.history = append(n.history, b.insertedProposal.Block)

		time.Sleep(1 * time.Second)
	}
}

type cluster struct {
	t     *testing.T
	nodes []*node
}

func newCluster(t *testing.T, num int) *cluster {
	// pre-generate ids for the nodes
	ids := []qbft.NodeID{}
	for i := 0; i < num; i++ {
		ids = append(ids, qbft.NodeID(fmt.Sprintf("node-%d", i)))
	}

	c := &cluster{
		t: t,
	}

	nodes := []*node{}
	for i := 0; i < num; i++ {
		logger := log.New(os.Stdout, string(ids[i]), log.Lshortfile)

		n := &node{
			logger:  logger,
			id:      ids[i],
			nodes:   ids,
			msgCh:   make(chan *qbft.QBFTMessageWithRecipient, 10),
			sendMsg: c.sendMessage,
			history: []*qbft.Block{},
			stopCh:  make(chan struct{}),
		}
		nodes = append(nodes, n)
	}

	c.nodes = nodes
	return c
}

func (c *cluster) sendMessage(msg *qbft.QBFTMessageWithRecipient) {
	// copy the object so that we do not pass pointers around the objects
	raw, _ := json.Marshal(msg)

	for _, n := range c.nodes {
		if n.id != msg.Sender {
			fmt.Println("- deliver message -")
			var localMsg *qbft.QBFTMessageWithRecipient
			if err := json.Unmarshal(raw, &localMsg); err != nil {
				panic(err)
			}

			n.deliverMessage(localMsg)
		}
	}
}

type nodeSet []*node

func (n nodeSet) start() {
	for _, node := range n {
		go node.start()
	}
}

func (n nodeSet) drop() {
	for _, node := range n {
		node.stop()
	}
}

func (n nodeSet) waitForLiveness(t *testing.T) {
	prevHeights := map[qbft.NodeID]uint64{}
	timeout := time.NewTimer(10 * time.Second)

	for {
		ready := false

		for _, node := range n {
			newHeight := node.height()
			prevHeight, ok := prevHeights[node.id]

			if !ok {
				prevHeights[node.id] = newHeight
				continue
			} else {
				if newHeight > prevHeight+1 {
					// fix, at least one is alive for now
					ready = true
				}
			}
		}

		if ready {
			return
		}

		select {
		case <-time.After(1 * time.Second):
		case <-timeout.C:
			t.Fatal("timeout without liveness")
		}
	}

}

func (c *cluster) all() nodeSet {
	return c.nodes
}

func (c *cluster) get(id ...string) nodeSet {
	nodes := nodeSet{}
	for _, n := range c.nodes {
		for _, i := range id {
			if n.id == qbft.NodeID(i) {
				nodes = append(nodes, n)
			}
		}
	}
	return nodes
}

var _ qbft.ValidatorSet = &validatorSet{}

func newValidatorSet(nodes []qbft.NodeID, lastProposer qbft.NodeID) qbft.ValidatorSet {
	return &validatorSet{
		nodes:        nodes,
		lastProposer: lastProposer,
	}
}

type validatorSet struct {
	nodes        []qbft.NodeID
	lastProposer qbft.NodeID
}

func (v *validatorSet) VotingPower() uint64 {
	return uint64(len(v.nodes))
}

func (v *validatorSet) Exists(from qbft.NodeID) (uint64, bool) {
	if v.index(from) != -1 {
		return 1, true
	}
	return 0, false
}

func (v *validatorSet) index(addr qbft.NodeID) int {
	for indx, i := range v.nodes {
		if i == addr {
			return indx
		}
	}
	return -1
}

func (v *validatorSet) CalculateProposer(round uint64) qbft.NodeID {
	seed := uint64(0)
	if v.lastProposer == "" {
		seed = round
	} else {
		offset := 0
		if indx := v.index(v.lastProposer); indx != -1 {
			offset = indx
		}
		seed = uint64(offset) + round + 1
	}

	pick := seed % uint64(len(v.nodes))
	return (v.nodes)[pick]
}
