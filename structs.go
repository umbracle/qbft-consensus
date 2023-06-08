package qbft

type Block struct {
	RoundNumber uint64
	Height      uint64
	Proposer    NodeID
	Body        []byte
}

func (b *Block) Copy() *Block {
	bb := new(Block)
	*bb = *b
	bb.Body = append(bb.Body, b.Body...)
	return b
}

type NodeID string

// -- containers --

type Proposal struct {
	Height uint64
	Round  uint64
	Digest []byte
}

type Prepare struct {
	Height uint64
	Round  uint64
	Digest []byte
}

type Commit struct {
	Height     uint64
	Round      uint64
	CommitSeal []byte
	Digest     []byte
}

type RoundChange struct {
	Height              uint64
	Round               uint64
	PreparedCertificate []SignedContainer // Prepare + Proposal
}

type UnsignedPayload struct {
	RoundChange *RoundChange
	Commit      *Commit
	Prepare     *Prepare
	Proposal    *Proposal
}

type SignedContainer struct {
	UnsignedPayload UnsignedPayload
	Signature       []byte

	// sender field is derived from the signature
	// and the unsigned payload
	sender NodeID
}

// -- transport messages --

type ProposalMessage struct {
	Payload                SignedContainer // SignedProposal
	ProposedBlock          *Block
	RoundChangeCertificate []*RoundChangeMessage
}

type PrepareMessage struct {
	Payload SignedContainer // SignedPrepare
}

type CommitMessage struct {
	Payload SignedContainer // SignedCommit
}

type RoundChangeMessage struct {
	Payload             SignedContainer // SignedRoundChange
	LatestProposedBlock *Block
}

type NewBlockMessage struct {
	Block []byte
}

type QBFTMessagePayload struct {
	ProposalMessage    *ProposalMessage
	PrepareMessage     *PrepareMessage
	CommitMessage      *CommitMessage
	RoundChangeMessage *RoundChangeMessage
	NewBlockMessage    *NewBlockMessage
}

func (q *QBFTMessagePayload) Round() uint64 {
	if q.ProposalMessage != nil {
		return q.ProposalMessage.Payload.UnsignedPayload.Proposal.Round
	} else if q.PrepareMessage != nil {
		return q.PrepareMessage.Payload.UnsignedPayload.Prepare.Round
	} else if q.CommitMessage != nil {
		return q.CommitMessage.Payload.UnsignedPayload.Commit.Round
	} else if q.RoundChangeMessage != nil {
		return q.RoundChangeMessage.Payload.UnsignedPayload.RoundChange.Round
	} else if q.NewBlockMessage != nil {
		panic("TODO")
	}
	panic("TODO")
}

func (q *QBFTMessagePayload) Height() uint64 {
	if q.ProposalMessage != nil {
		return q.ProposalMessage.Payload.UnsignedPayload.Proposal.Height
	} else if q.PrepareMessage != nil {
		return q.PrepareMessage.Payload.UnsignedPayload.Prepare.Height
	} else if q.CommitMessage != nil {
		return q.CommitMessage.Payload.UnsignedPayload.Commit.Height
	} else if q.RoundChangeMessage != nil {
		return q.RoundChangeMessage.Payload.UnsignedPayload.RoundChange.Height
	} else if q.NewBlockMessage != nil {
		panic("TODO")
	}
	panic("TODO")
}

func (q *QBFTMessagePayload) typ() string {
	if q.ProposalMessage != nil {
		return "proposal"
	} else if q.PrepareMessage != nil {
		return "prepare"
	} else if q.CommitMessage != nil {
		return "commit"
	} else if q.RoundChangeMessage != nil {
		return "round-change"
	} else if q.NewBlockMessage != nil {
		return "new-block"
	}
	return ""
}

type QBFTMessageWithRecipient struct {
	Message QBFTMessagePayload
	Sender  NodeID
}

// SealedProposal represents the sealed proposal model
type SealedProposal struct {
	Block          *Block
	CommittedSeals []CommittedSeal
}

type CommittedSeal struct {
	// Signature value
	Signature []byte

	// Node that signed
	NodeID NodeID
}
