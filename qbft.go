package qbft

import (
	"bytes"
	"fmt"
	"log"
	"math"
	"os"
	"time"
)

type Timer interface {
	SetTimeout(n time.Duration)
}

type Config struct {
	Transport Transport
	Logger    *log.Logger
	Signer    Signer
	Timer     Timer
}

func DefaultConfig() *Config {
	return &Config{
		Logger: log.New(os.Stderr, "", log.LstdFlags),
	}
}

func (c *Config) ApplyOps(opts ...ConfigOption) {
	for _, opt := range opts {
		opt(c)
	}
}

type ConfigOption func(*Config)

func WithTimer(t Timer) ConfigOption {
	return func(c *Config) {
		c.Timer = t
	}
}

func WithTransport(t Transport) ConfigOption {
	return func(c *Config) {
		c.Transport = t
	}
}

func WithLogger(l *log.Logger) ConfigOption {
	return func(c *Config) {
		c.Logger = l
	}
}

func WithSigner(s Signer) ConfigOption {
	return func(c *Config) {
		c.Signer = s
	}
}

type Signer interface {
	SignMessage(msg []byte) ([]byte, error)
	RecoverSigner(msg, signature []byte) (NodeID, error)
}

func New(localID NodeID, opts ...ConfigOption) *QBFT {
	cfg := DefaultConfig()
	cfg.ApplyOps(opts...)

	q := &QBFT{
		config:  cfg,
		msgCh:   make(chan QBFTMessageWithRecipient, 1000), // for testing
		state:   newState(),
		localID: localID,
	}
	return q
}

func (q *QBFT) Start() {
	for {
		select {
		case msg := <-q.config.Transport.Recv():
			// decode any signedContainer from the message
			if proposal := msg.Message.ProposalMessage; proposal != nil {
				if err := q.recoverSigner2(&proposal.Payload); err != nil {
					panic(err)
				}
				for _, msg := range proposal.RoundChangeCertificate {
					if err := q.recoverSigner2(&msg.Payload); err != nil {
						panic(err)
					}
				}

			} else if prepare := msg.Message.PrepareMessage; prepare != nil {
				if err := q.recoverSigner2(&prepare.Payload); err != nil {
					panic(err)
				}

			} else if commit := msg.Message.CommitMessage; commit != nil {
				if err := q.recoverSigner2(&commit.Payload); err != nil {
					panic(err)
				}

			} else if roundChange := msg.Message.RoundChangeMessage; roundChange != nil {
				if err := q.recoverSigner2(&roundChange.Payload); err != nil {
					panic(err)
				}

			} else {
				panic("TODO")
			}

			q.msgCh <- *msg
		case <-q.closeCh:
			return
		}
	}
}

// QBFT is a BFT consensus protocol
type QBFT struct {
	config  *Config
	backend Backend
	state   *state
	localID NodeID
	closeCh chan struct{}
	msgCh   chan QBFTMessageWithRecipient
	resCh   chan struct{}
	stopCh  chan struct{}
}

func (q *QBFT) readMsgs() <-chan QBFTMessageWithRecipient {
	return q.msgCh
}

func (q *QBFT) SetBackend(b Backend) {
	q.backend = b
	q.state.resetState(b.ValidatorSet())
	q.startNewRound(0)
}

func (q *QBFT) Close() {
	close(q.closeCh)
}

func (q *QBFT) Run() chan struct{} {
	q.resCh = make(chan struct{})
	q.stopCh = make(chan struct{})

	go q.runImpl()

	return q.resCh
}

func (q *QBFT) Stop() {
	close(q.stopCh)

	// wait for the runImpl to finish
	<-q.resCh
}

func (q *QBFT) runImpl() {
	// There are two conditions to do processing:
	// 1. A new message has arrived for the sequence.
	// 2. Round change timeout.
	msgs := q.readMsgs()

	if err := q.buildInitialProposal(); err != nil {
		// log it
		panic(err)
	}

	for {
		fmt.Println("- iter -", q.localID, q.resCh)

		select {
		case msg := <-msgs:
			q.handleMessage(msg)

		case <-q.state.timeoutTimer.C:
			fmt.Println("SZSSS")
			q.startNewRoundAndSendRoundChange(q.state.round + 1)

		case <-q.stopCh:
			q.config.Logger.Printf("[INFO]: Node is out")
			close(q.resCh)
			return
		}

		// check if the channel has been closed due to an execution
		// of a commit message which created a sealed proposal.
		select {
		case <-q.resCh:
			return
		default:
		}
	}
}

func (q *QBFT) buildInitialProposal() error {
	proposer := q.state.validators.CalculateProposer(q.state.round)
	if proposer != q.localID {
		return nil
	}

	block, digest, err := q.backend.BuildProposal(q.state.round)
	if err != nil {
		return err
	}
	if block == nil {
		return fmt.Errorf("empty proposal")
	}

	// TODO: Validate that the fields are correct.

	proposal := &Proposal{
		Height: q.backend.Height(),
		Round:  0,
		Digest: digest,
	}
	signedProposal, err := q.signMessage(UnsignedPayload{Proposal: proposal})
	if err != nil {
		return err
	}

	q.config.Logger.Print("_ build proposal _")

	proposalMsg := ProposalMessage{
		Payload:       signedProposal,
		ProposedBlock: block,
	}

	ownMsg := q.multicast(QBFTMessagePayload{ProposalMessage: &proposalMsg})
	if err := q.uponProposal(ownMsg); err != nil {
		return err
	}
	return nil
}

func (q *QBFT) handleMessage(m QBFTMessageWithRecipient) {
	msg := m.Message

	if m.Message.Height() != q.backend.Height() {
		// TODO: handle this with the message queue
		q.config.Logger.Printf("[INFO]: Out of order message, height=%d, typ=%s", m.Message.Height(), m.Message.typ())
		return
	}

	if msg.PrepareMessage != nil {
		if err := q.uponPrepare(m); err != nil {
			panic(err)
		}

	} else if msg.ProposalMessage != nil {
		if err := q.uponProposal(m); err != nil {
			panic(err)
		}

	} else if msg.CommitMessage != nil {
		if err := q.uponCommit(m); err != nil {
			panic(err)
		}

	} else if msg.RoundChangeMessage != nil {
		if err := q.uponRoundChange(m); err != nil {
			panic(err)
		}
	}
}

func (q *QBFT) recoverSigners2(msgs *[]SignedContainer) error {
	for _, msg := range *msgs {
		signer, err := q.config.Signer.RecoverSigner([]byte{}, msg.Signature)
		if err != nil {
			return err
		}
		msg.sender = signer
	}
	return nil
}

func (q *QBFT) recoverSigner2(msg *SignedContainer) error {
	signer, err := q.config.Signer.RecoverSigner([]byte{}, msg.Signature)
	if err != nil {
		return err
	}
	msg.sender = signer
	return nil
}

func (q *QBFT) recoverSigner(msg SignedContainer) (NodeID, error) {
	return q.config.Signer.RecoverSigner([]byte{}, msg.Signature)
}

// getExtendedRCC returns the highest round change set
func (q *QBFT) getExtendedRCC() (*messageSet[RoundChangeMessage], uint64) {
	curRound := q.state.round

	var rccSet *messageSet[RoundChangeMessage]
	var maxRound uint64

	fmt.Println("_ get extended rcc _")

	// Using the 'messageSet' routing we ensure already that:
	// 1. all messages are sent by different senders.
	// 2. all messages are from the same round.
	// 3. senders all part of the activa validator set for this height.
	for round, msgSet := range q.state.roundChangeMessages {
		fmt.Println("_ msg set _", msgSet)

		// round is old
		if round < curRound {
			fmt.Println("A")
			continue
		}

		// there is not enough quorum for this round
		if !msgSet.isQuorum(q.state.quorum) {
			fmt.Println("B", msgSet, msgSet.accumulatedVotingPower, q.state.quorum)
			continue
		}

		// the prepared-certificate is valid
		// q.isValidPC(nil, 0, 0)

		fmt.Println("--rccset ", rccSet)

		if rccSet == nil || maxRound < round {
			rccSet = msgSet
			maxRound = round
		}
	}

	fmt.Println("_ END ", rccSet, maxRound)

	return rccSet, maxRound
}

func (q *QBFT) startNewRound(round uint64) {
	q.config.Logger.Printf("start new round, round=%d", round)
	q.state.round = round
	q.state.acceptedPB = nil
	q.state.commitSent = false

	if round == 0 || round > q.state.round {
		q.state.setTimer(5 * time.Second)
	}
}

func (q *QBFT) startNewRoundAndSendRoundChange(newRound uint64) error {
	q.startNewRound(newRound)

	roundChange, err := q.signMessage(UnsignedPayload{RoundChange: &RoundChange{
		Height:              q.backend.Height(),
		Round:               newRound,
		PreparedCertificate: q.state.latestPC,
	}})
	if err != nil {
		return err
	}

	roundChangeMessage := RoundChangeMessage{
		Payload:             roundChange,
		LatestProposedBlock: q.state.acceptedPB,
	}

	msg := q.multicast(QBFTMessagePayload{RoundChangeMessage: &roundChangeMessage})
	if err := q.uponRoundChange(msg); err != nil {
		return err
	}

	return nil
}

func (q *QBFT) uponRoundChange(m QBFTMessageWithRecipient) error {
	q.config.Logger.Printf("[DEBUG] received 'round-change' message: from=%s, height=%d", m.Sender, m.Message.Height())

	if len(m.Message.RoundChangeMessage.Payload.UnsignedPayload.RoundChange.PreparedCertificate) != 0 {
		// if it is included, it should be correct
		fmt.Println("....", m.Message.RoundChangeMessage.Payload.UnsignedPayload.RoundChange.PreparedCertificate)

		if !q.isValidPC(m.Message.RoundChangeMessage.Payload.UnsignedPayload.RoundChange.PreparedCertificate, 100, q.backend.Height()) {
			panic("it should not happen")
		}
	}

	if !q.state.addMessage(m) {
		return nil
	}

	rcc, round := q.getExtendedRCC()
	if rcc == nil {
		return nil
	}

	q.config.Logger.Printf("[INFO]: there is an extended rcc round %d", round)

	q.startNewRound(round)

	if q.backend.ValidatorSet().CalculateProposer(round) != q.localID {
		return nil
	}

	// we are the proposer for this round change
	// check if any of the round change messages includes a proposal for a block

	var (
		block    *Block
		maxRound uint64
	)

	for _, msg := range rcc.messageMap {
		proposedBlock := msg.LatestProposedBlock
		if proposedBlock == nil {
			continue
		}

		if block != nil && proposedBlock.RoundNumber > maxRound {
			block = proposedBlock
			maxRound = proposedBlock.RoundNumber
		}
	}

	if block == nil {
		var err error
		block, _, err = q.backend.BuildProposal(round)
		if err != nil {
			return err
		}
	}

	proposal := Proposal{
		Height: q.backend.Height(),
		Round:  round,
	}

	signedProposal, err := q.signMessage(UnsignedPayload{Proposal: &proposal})
	if err != nil {
		return err
	}

	q.config.Logger.Print("built round change proposal proposal")

	proposalMsg := ProposalMessage{
		Payload:                signedProposal,
		ProposedBlock:          block,
		RoundChangeCertificate: []*RoundChangeMessage{},
	}
	for _, msg := range rcc.messageMap {
		proposalMsg.RoundChangeCertificate = append(proposalMsg.RoundChangeCertificate, &msg)
	}

	ownMsg := q.multicast(QBFTMessagePayload{ProposalMessage: &proposalMsg})
	if err := q.uponProposal(ownMsg); err != nil {
		return err
	}

	return nil
}

func (q *QBFT) hasPartialRoundChangeQuorum() (bool, uint64) {
	// there is at least a partial quorum of nodes that are already
	// in a higher round.
	currentRound := q.state.round
	senders := map[NodeID]struct{}{}

	var minRound uint64
	var minRoundSet bool

	fmt.Println("xxxx")
	fmt.Println(q.state.roundChangeMessages, currentRound)

	for round, msgsPerRound := range q.state.roundChangeMessages {
		if round <= currentRound {
			continue
		}

		fmt.Println("----")
		fmt.Println(round, msgsPerRound.messageMap)

		if !minRoundSet && round < minRound {
			minRound = round
			minRoundSet = true
		}
		for sender := range msgsPerRound.messageMap {
			senders[sender] = struct{}{}
		}
	}

	fmt.Println("-- senders --")
	fmt.Println(senders)

	if len(senders) > int(q.state.quorum)/2 {
		// TODO
		return true, minRound
	}
	return false, minRound
}

func (q *QBFT) isValidPC(pc []SignedContainer, rlimit, height uint64) bool {
	if len(pc) == 0 {
		fmt.Println("Pre")
		return false
	}

	var (
		proposalFound    bool
		expectedRound    *uint64
		totalVotingPower uint64
	)

	alreadyVoted := map[NodeID]struct{}{}

	for _, container := range pc {
		fmt.Println("_ container _", container.sender)

		msg := container.UnsignedPayload
		var msgRound uint64

		if msg.Prepare != nil {
			msgRound = msg.Prepare.Round

			// Only one pre per validator allowed
			if _, ok := alreadyVoted[container.sender]; ok {
				fmt.Println("A1")
				return false
			}
			alreadyVoted[container.sender] = struct{}{}

			// All the senders must exist in the validator set
			votingPower, ok := q.state.validators.Exists(container.sender)
			if !ok {
				fmt.Println("A2")
				return false
			}
			totalVotingPower += votingPower

		} else if msg.Proposal != nil {
			msgRound = msg.Proposal.Round

			// only one proposal allowed in the pc
			if proposalFound {
				fmt.Println("A3")
				return false
			}
			proposalFound = true

			// proposal is sent by the proposer
			if q.state.validators.CalculateProposer(msgRound) != container.sender {
				fmt.Println("A4")
				return false
			}
		} else {
			fmt.Println("A5")
			return false
		}

		// all the messages in the pc are for the same round
		if expectedRound == nil {
			expectedRound = &msgRound
		} else if *expectedRound != msgRound {
			fmt.Println("A6")
			return false
		}
	}

	// the voting power of all the senders in the PC must be higher
	// than the
	if totalVotingPower < q.state.quorum {
		fmt.Println("A7")
		return false
	}

	// the round included in all the messages is lower than rlimit
	if *expectedRound >= rlimit {
		fmt.Println("A8")
		return false
	}

	return true
}

func (q *QBFT) uponProposal(m QBFTMessageWithRecipient) error {
	q.config.Logger.Printf("[DEBUG] received 'proposal' message: from=%s, height=%d", m.Sender, m.Message.Height())

	if q.state.acceptedPB != nil {
		return nil
	}

	msg := m.Message.ProposalMessage
	var prepare *PrepareMessage

	if msg.Payload.UnsignedPayload.Proposal.Round == 0 {
		// the proposer is expected
		if msg.Payload.sender != q.state.validators.CalculateProposer(0) {
			return fmt.Errorf("unexpected proposer")
		}

		// save proposal in the state to calculate QB
		q.state.addMessage(m)

		// proposal for round 0
		signedPrepare, err := q.signMessage(UnsignedPayload{Prepare: &Prepare{
			Height: q.backend.Height(),
			Round:  0,
		}})
		if err != nil {
			return err
		}

		q.state.acceptedPB = msg.ProposedBlock

		prepare = &PrepareMessage{
			Payload: signedPrepare,
		}
	} else {
		// special fallback case for rounds != 0
		round := msg.Payload.UnsignedPayload.Proposal.Round

		// the proposer is expected
		if msg.Payload.sender != q.state.validators.CalculateProposer(round) {
			return fmt.Errorf("unexpected proposer")
		}

		fmt.Println(msg.Payload.UnsignedPayload.Proposal)
		fmt.Println(msg.RoundChangeCertificate)

		// there is a quorum of rcc and messages are signed
		// by different validators (the quorum is on the payload part)
		// TODO: optimize
		sigs := []SignedContainer{}
		for _, msg := range msg.RoundChangeCertificate {
			sigs = append(sigs, msg.Payload)
		}
		ok := q.isQuorum(sigs, func(msg SignedContainer) bool {
			roundChange := msg.UnsignedPayload.RoundChange

			if roundChange.Height != q.backend.Height() {
				fmt.Println("B0")
				return false
			}
			if roundChange.Round != round {
				fmt.Println("B!")
				return false
			}
			return true
		})
		if !ok {
			return fmt.Errorf("failed to validate roudn change justification")
		}

		// find the round-change with the highest round
		var rccBlock *Block

		for _, roundChangeMsg := range msg.RoundChangeCertificate {
			if roundChangeMsg.LatestProposedBlock == nil {
				continue
			}

			pc := roundChangeMsg.Payload.UnsignedPayload.RoundChange.PreparedCertificate
			if !q.isValidPC(pc, 1000, q.backend.Height()) {
				continue
			}

			if rccBlock == nil || rccBlock.RoundNumber < roundChangeMsg.LatestProposedBlock.RoundNumber {
				rccBlock = roundChangeMsg.LatestProposedBlock
			}
		}

		if rccBlock != nil {
			// validate that this block is the one provided in the proposal
			if !bytes.Equal(rccBlock.Body, msg.ProposedBlock.Body) {
				panic("bad")
			}
		}

		q.startNewRound(msg.ProposedBlock.RoundNumber)
		q.state.addMessage(m)
		q.state.acceptedPB = msg.ProposedBlock

		signedPrepare, err := q.signMessage(UnsignedPayload{Prepare: &Prepare{
			Height: q.backend.Height(),
			Round:  msg.ProposedBlock.RoundNumber,
		}})
		if err != nil {
			return err
		}

		prepare = &PrepareMessage{
			Payload: signedPrepare,
		}
	}

	// send any prepare message
	ownMsg := q.multicast(QBFTMessagePayload{PrepareMessage: prepare})
	if err := q.uponPrepare(ownMsg); err != nil {
		return err
	}

	return nil
}

func (q *QBFT) isQuorum(msgs []SignedContainer, checkFn func(SignedContainer) bool) bool {
	alreadyVoted := map[NodeID]struct{}{}
	totalVotingPower := uint64(0)

	for _, msg := range msgs {
		fmt.Println("Sender", msg.sender)
		if !checkFn(msg) {
			fmt.Println("I")
			return false
		}

		// Only one pre per validator allowed
		//if _, ok := alreadyVoted[msg.sender]; ok {
		//	fmt.Println("H")
		//	return false
		//}
		alreadyVoted[msg.sender] = struct{}{}

		// All the senders must exist in the validator set
		votingPower, ok := q.state.validators.Exists(msg.sender)
		if !ok {
			fmt.Println("G")
			return false
		}
		totalVotingPower += votingPower
	}

	if totalVotingPower < q.state.quorum {
		fmt.Println("F")
		return false
	}

	return true
}

func (q *QBFT) uponPrepare(msg QBFTMessageWithRecipient) error {
	q.config.Logger.Printf("[DEBUG] received 'prepare' message: from=%s, height=%d", msg.Sender, msg.Message.Height())

	if !q.state.addMessage(msg) {
		return nil
	}

	// check if there is quorum for the updated round
	prepareMsg := msg.Message.PrepareMessage.Payload.UnsignedPayload.Prepare
	if !q.state.preparedMessages.atRound(prepareMsg.Round).isQuorum(q.state.quorum) {
		return nil
	}

	// check if the commit message was not sent yet
	if q.state.commitSent {
		return nil
	}

	// send commit message
	signedCommit, err := q.signMessage(UnsignedPayload{Commit: &Commit{
		Height: q.backend.Height(),
		Round:  q.state.round,
	}})
	if err != nil {
		return err
	}

	// find valid proposal message and prepare messages for this round
	latestPC := []SignedContainer{}
	proposalMap := q.state.proposalMessages.atRound(q.state.round).messageMap
	if len(proposalMap) != 1 {
		return fmt.Errorf("only one proposal per round expected but %d found", len(proposalMap))
	}
	for _, proposal := range proposalMap {
		latestPC = append(latestPC, proposal.Payload)
	}
	for _, prepare := range q.state.preparedMessages.atRound(q.state.round).messageMap {
		latestPC = append(latestPC, prepare.Payload)
	}

	q.state.latestPC = latestPC

	commitMsg := CommitMessage{
		Payload: signedCommit,
	}

	q.state.commitSent = true

	fmt.Println("_ is local ? _")

	ownMsg := q.multicast(QBFTMessagePayload{CommitMessage: &commitMsg})
	if err := q.uponCommit(ownMsg); err != nil {
		return err
	}

	return nil
}

func (q *QBFT) uponCommit(msg QBFTMessageWithRecipient) error {
	q.config.Logger.Printf("[DEBUG] received 'commit' message: from=%s, height=%d", msg.Sender, msg.Message.Height())

	if !q.state.addMessage(msg) {
		return nil
	}

	// check if there is quorum of commits
	if !q.state.commitMessages.atRound(q.state.round).isQuorum(q.state.quorum) {
		return nil
	}

	q.config.Logger.Printf("[INFO]: quorum of commits")
	fmt.Println(q.state.commitMessages.atRound(q.state.round).messageMap)

	q.config.Logger.Print("Insert proposal")

	sealedProposal := &SealedProposal{
		Block: q.state.acceptedPB.Copy(),
	}

	q.backend.Insert(sealedProposal)
	q.finish()

	return nil
}

func (q *QBFT) finish() {
	close(q.resCh)
}

func (q *QBFT) signMessage(msg UnsignedPayload) (SignedContainer, error) {
	signature, err := q.config.Signer.SignMessage(nil)
	if err != nil {
		return SignedContainer{}, err
	}
	return SignedContainer{UnsignedPayload: msg, Signature: signature, sender: q.localID}, nil
}

func (q *QBFT) multicast(msg QBFTMessagePayload) QBFTMessageWithRecipient {
	q.config.Logger.Printf("[INFO] multicast message: type=%s", msg.typ())

	err := q.config.Transport.Send(&QBFTMessageWithRecipient{
		Message: msg,
		Sender:  q.localID,
	})
	if err != nil {
		q.config.Logger.Printf("[INFO] failed to send message: %v", err)
	}

	return QBFTMessageWithRecipient{
		Message: msg,
		Sender:  q.localID,
	}
}

// isValidRoundChange returns `true` if and only if `sPayload` is a valid signed RoundChange
// payload for height `height`, round `round` under the assumption that the current set
// of validators is `validators`.
func (q *QBFT) isValidRoundChange(msg SignedContainer) error {
	return nil
}

func getQuorumNumbers(nodes uint64) (quorum uint64, faulty uint64) {
	// https://arxiv.org/pdf/1909.10194.pdf
	quorum = uint64(math.Ceil(float64(nodes*2) / 3))
	faulty = uint64(math.Floor(float64(nodes-1) / 3))
	return
}
