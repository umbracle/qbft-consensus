package qbft

import (
	"time"
)

type state struct {
	// validators represent the current validator set
	validators ValidatorSet

	round  uint64
	quorum uint64

	// timeoutCh is closed when the timeout for the round has expired
	timeoutTimer *time.Timer

	latestPC                    []SignedContainer
	acceptedPB                  *Block
	commitSent                  bool
	latestPreparedProposedBlock *Block

	// list of messages received and processed
	preparedMessages    messageSetByRound[PrepareMessage]
	proposalMessages    messageSetByRound[ProposalMessage]
	commitMessages      messageSetByRound[CommitMessage]
	roundChangeMessages messageSetByRound[RoundChangeMessage]
}

func newState() *state {
	return &state{}
}

func (s *state) setTimer(timeout time.Duration) {
	s.timeoutTimer.Reset(timeout)
}

func (s *state) resetState(validators ValidatorSet) {
	s.validators = validators

	s.round = 0
	s.quorum, _ = getQuorumNumbers(s.validators.VotingPower())

	s.timeoutTimer = time.NewTimer(0)
	s.timeoutTimer.Stop()

	s.latestPC = []SignedContainer{}
	s.acceptedPB = nil
	s.commitSent = false
	s.latestPreparedProposedBlock = nil

	// initialize message sets
	s.proposalMessages = make(messageSetByRound[ProposalMessage])
	s.preparedMessages = make(messageSetByRound[PrepareMessage])
	s.commitMessages = make(messageSetByRound[CommitMessage])
	s.roundChangeMessages = make(messageSetByRound[RoundChangeMessage])
}

func (s *state) SetRound(newRound uint64) {
	s.round = newRound
}

func (s *state) addMessage(m QBFTMessageWithRecipient) bool {
	votingPower, ok := s.validators.Exists(m.Sender)
	if !ok {
		return false
	}

	var inserted bool

	from := m.Sender
	if msg := m.Message.RoundChangeMessage; msg != nil {
		inserted = s.roundChangeMessages.atRound(msg.Payload.UnsignedPayload.RoundChange.Round).addMessage(*msg, from, votingPower)
	} else if msg := m.Message.PrepareMessage; msg != nil {
		inserted = s.preparedMessages.atRound(msg.Payload.UnsignedPayload.Prepare.Round).addMessage(*msg, from, votingPower)
	} else if msg := m.Message.ProposalMessage; msg != nil {
		inserted = s.proposalMessages.atRound(msg.Payload.UnsignedPayload.Proposal.Round).addMessage(*msg, from, votingPower)
	} else if msg := m.Message.CommitMessage; msg != nil {
		inserted = s.commitMessages.atRound(msg.Payload.UnsignedPayload.Commit.Round).addMessage(*msg, from, votingPower)
	}

	return inserted
}

type messageSetByRound[T any] map[uint64]*messageSet[T]

func (m *messageSetByRound[T]) atRound(round uint64) *messageSet[T] {
	if _, exists := (*m)[round]; !exists {
		(*m)[round] = &messageSet[T]{
			messageMap: make(map[NodeID]T),
		}
	}
	return (*m)[round]
}

type messageSet[T any] struct {
	messageMap             map[NodeID]T
	accumulatedVotingPower uint64
}

func (m *messageSet[T]) length() uint64 {
	return uint64(len(m.messageMap))
}

func (m *messageSet[T]) addMessage(message T, from NodeID, votingPower uint64) bool {
	if _, exists := m.messageMap[from]; exists {
		return false
	}
	m.messageMap[from] = message
	m.accumulatedVotingPower += votingPower
	return true
}

func (m *messageSet[T]) isQuorum(quorum uint64) bool {
	return m.accumulatedVotingPower >= quorum
}
