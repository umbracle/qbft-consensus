package qbft

type Backend interface {
	// Height returns the current height
	Height() uint64

	// ValidatorSet returns the validator set
	ValidatorSet() ValidatorSet

	// Insert inserts the sealed proposal
	Insert(p *SealedProposal) error

	// BuildProposal builds a proposal for the current round (used if proposer)
	BuildProposal(round uint64) (*Block, []byte, error)

	// ValidateProposal validates that the block proposal is valid (used if not proposer)
	ValidateProposal(*Proposal) error
}

type ValidatorSet interface {
	VotingPower() uint64
	Exists(from NodeID) (uint64, bool)
	CalculateProposer(round uint64) NodeID
}

type Transport interface {
	Recv() chan *QBFTMessageWithRecipient
	Send(msg *QBFTMessageWithRecipient) error
}
