package qbft

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"testing"
	"time"
)

type simTransport func(from, to NodeID) time.Duration

func TestIntegration(t *testing.T) {
	simTransport := func(from, to NodeID) time.Duration {
		// create a random time duration between 500 and 4500 milliseconds
		// to simulate the network latency
		return time.Duration(500+rand.Intn(2500)) * time.Millisecond
	}

	c := newIntegrationCluster(3, simTransport)

	var done bool

	var steps int
	for steps = 0; steps < 10000 && !done; steps++ {
		c.runStep()
		done = c.isDone()
	}

	if !done {
		t.Fatal("not done")
	}
	t.Logf("Num of steps: %d", steps)
}

type integrationCluster struct {
	nodes        map[NodeID]*integrationNode
	messages     []*integrationMessage
	timestamp    uint64
	simTransport simTransport
	valSet       ValidatorSet
}

func newIntegrationCluster(num int, transport simTransport) *integrationCluster {
	ic := &integrationCluster{
		nodes:        map[NodeID]*integrationNode{},
		messages:     []*integrationMessage{},
		timestamp:    0,
		simTransport: transport,
	}

	ids := []NodeID{}
	for i := 0; i < num; i++ {
		ids = append(ids, NodeID(fmt.Sprintf("node-%d", i)))
	}

	accPool := newTesterAccountPool()
	for _, id := range ids {
		accPool.add(string(id))
	}
	valSet := accPool.validatorSet()
	ic.valSet = valSet

	for _, id := range ids {
		node := &integrationNode{
			id:     id,
			height: 0,
			valSet: valSet,
		}
		qbft := New(id,
			WithSigner(&mockSigner{node: id}),
			WithTransport(ic),
			WithRoundTimeout(5*time.Second),
			WithTimer(&integrationTimer{node: id, ic: ic}),
			WithLogger(log.New(os.Stdout, string(id)+": ", 0)),
		)
		node.qbft = qbft

		ic.nodes[id] = node
	}

	// set the backend now once everything has been initialized
	for _, node := range ic.nodes {
		node.qbft.SetBackend(node)
	}

	// emit the first proposal since that one does not have any
	// external event that dispatches the action
	proposer := valSet.CalculateProposer(0)
	if err := ic.nodes[proposer].qbft.buildInitialProposal(); err != nil {
		panic(err)
	}

	return ic
}

func (i *integrationCluster) SetTimeout(id NodeID, timeout time.Duration) {
	msg := &integrationMessage{
		timestamp: i.timestamp + uint64(timeout.Milliseconds()),
		msg:       nil,
		receiver:  id,
	}
	i.pushMessage(msg)
}

func (ic *integrationCluster) popMessage() *integrationMessage {
	if len(ic.messages) == 0 {
		return nil
	}

	var pop *integrationMessage
	pop, ic.messages = ic.messages[0], ic.messages[1:]

	return pop
}

func (ic *integrationCluster) pushMessage(msg *integrationMessage) {
	fmt.Printf("Add message to=%s timestamp=%d (%v)\n", msg.receiver, msg.timestamp, msg)

	// append the message and sort the queue
	ic.messages = append(ic.messages, msg)

	sort.Slice(ic.messages, func(i, j int) bool {
		return ic.messages[i].timestamp < ic.messages[j].timestamp
	})
}

type integrationTimer struct {
	node NodeID
	ic   *integrationCluster
}

func (i *integrationTimer) TimeCh() <-chan time.Time {
	panic("not required")
}

func (i *integrationTimer) SetTimeout(n time.Duration) {
	i.ic.SetTimeout(i.node, n)
}

func (i *integrationCluster) Recv() chan *QBFTMessageWithRecipient {
	panic("not required")
}

func (i *integrationCluster) Send(rawMsg *QBFTMessageWithRecipient) error {
	// send the message to every queue
	for _, node := range i.nodes {
		if node.id == rawMsg.Sender {
			continue
		}

		latency := time.Duration(0)
		if i.simTransport != nil {
			latency = i.simTransport(rawMsg.Sender, node.id)
		}

		msg := &integrationMessage{
			timestamp: i.timestamp + uint64(latency.Milliseconds()),
			msg:       rawMsg,
			receiver:  node.id,
		}
		i.pushMessage(msg)
	}

	return nil
}

func (i *integrationCluster) isDone() bool {
	num := 0
	for _, node := range i.nodes {
		if node.sealedProposal != nil {
			num++
		}
	}
	quorum, _ := getQuorumNumbers(i.valSet.VotingPower())
	return num >= int(quorum)
}

func (i *integrationCluster) runStep() {
	msg := i.popMessage()
	if msg == nil {
		// simulation is done?
		return
	}

	/*
		if msg.msg != nil {
			fmt.Printf("Run step (MSG): receiver=%s timestamp=%d type=%s round=%d %d (%v)\n", msg.receiver, msg.timestamp, msg.msg.Message.typ(), msg.msg.Message.Round(), i.nodes[msg.receiver].qbft.state.round, msg)
		} else {
			fmt.Printf("Run step (TIMEOUT): receiver=%s timestamp=%d %d (%v)\n", msg.receiver, msg.timestamp, i.nodes[msg.receiver].qbft.state.round, msg)
		}
	*/

	// all the messages generated during this interation
	// will take as reference this timestamp
	i.timestamp = msg.timestamp

	node := i.nodes[msg.receiver]
	if node.sealedProposal != nil {
		return
	}

	node.qbft.handleMessage(msg.msg)
}

type integrationMessage struct {
	timestamp uint64
	msg       *QBFTMessageWithRecipient
	receiver  NodeID
}

type integrationNode struct {
	qbft           *QBFT
	id             NodeID
	height         uint64
	valSet         ValidatorSet
	sealedProposal *SealedProposal
}

func (i *integrationNode) Height() uint64 {
	return i.height
}

func (i *integrationNode) ValidatorSet() ValidatorSet {
	return i.valSet
}

func (i *integrationNode) Insert(p *SealedProposal) error {
	i.sealedProposal = p
	return nil
}

func (i *integrationNode) BuildProposal(round uint64) (*Block, []byte, error) {
	block := &Block{
		RoundNumber: round,
		Height:      i.height,
		Body:        []byte{0x1, 0x2},
		Proposer:    i.id,
	}
	return block, nil, nil
}

func (i *integrationNode) ValidateProposal(*Proposal) error {
	return nil
}
