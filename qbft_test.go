package qbft

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildProposal(t *testing.T) {
	q := newMockQBFT(t)
	require.NoError(t, q.buildInitialProposal())

	q.expect(expectResult{
		outgoing: 2, // prepare and proposal
	})
}

/*
func TestUponProposal_Round0(t *testing.T) {
	q := newMockQBFT(t)

	err := q.uponProposal(ProposalMessage{
		ProposalPayload: q.get("a").sign(&Proposal{}),
		ProposedBlock: &Block{
			RoundNumber: 0,
			Proposer:    "a",
		},
	})
	// not enough voting power in the justifications (empty)
	require.Error(t, err)
}

func TestUponProposal_RoundNot0(t *testing.T) {
	q := newMockQBFT(t)

	err := q.uponProposal(ProposalMessage{
		ProposalPayload: q.get("b").sign(&Proposal{
			Round: 1,
		}),
		ProposedBlock: &Block{
			RoundNumber: 1,
			Proposer:    "b",
		},
	})
	require.NoError(t, err)

	q.expect(expectResult{
		outgoing: 1, // prepare
	})
}
*/

func TestUponPrepare(t *testing.T) {
	q := newMockQBFT(t)
	q.uponPrepare(QBFTMessageWithRecipient{})

	q.expect(expectResult{})
}

func TestUponRoundTimeout(t *testing.T) {

}

func TestIsValidProposal(t *testing.T) {

}

type mockQbft struct {
	t *testing.T

	// config options
	accounts []string
	height   uint64

	*QBFT

	proposer     NodeID
	accountPool  *testerAccountPool
	sentMessages []*QBFTMessageWithRecipient
}

type mockConfigOption func(*mockQbft)

func withHeight(height uint64) mockConfigOption {
	return func(m *mockQbft) {
		m.height = height
	}
}

func withAccounts(acct ...string) mockConfigOption {
	return func(m *mockQbft) {
		m.accounts = acct
	}
}

func newMockQBFT(t *testing.T, opts ...mockConfigOption) *mockQbft {
	m := &mockQbft{
		t: t,
	}
	for _, opt := range opts {
		opt(m)
	}

	if len(m.accounts) == 0 {
		m.accounts = []string{"a", "b", "c"}
	}

	m.accountPool = newTesterAccountPool()
	for _, acct := range m.accounts {
		m.accountPool.add(acct)
	}

	proposer := NodeID(m.accounts[0])
	signer := &mockSigner{
		node: proposer,
	}

	qbft := New(
		proposer,
		WithTransport(m),
		WithSigner(signer),
	)
	qbft.SetBackend(m)
	m.QBFT = qbft
	m.proposer = proposer

	return m
}

func (m *mockQbft) sign(payload UnsignedPayload) SignedContainer {
	return m.accountPool.get(string(m.proposer)).sign(payload)
}

func (m *mockQbft) get(name string) *testerAccount {
	return m.accountPool.get(name)
}

type expectResult struct {
	outgoing int
}

func (m *mockQbft) expect(res expectResult) {
	if size := len(m.sentMessages); size != res.outgoing {
		m.t.Fatalf("incorrect outgoing messages actual: %v, expected: %v", size, res.outgoing)
	}
}

func (m *mockQbft) Height() uint64 {
	return m.height
}

func (m *mockQbft) ValidatorSet() ValidatorSet {
	return m.accountPool.validatorSet()
}

func (m *mockQbft) Insert(p *SealedProposal) error {
	return nil
}

func (m *mockQbft) BuildProposal(round uint64) (*Block, []byte, error) {
	b := &Block{
		RoundNumber: round,
		Height:      m.height,
		Proposer:    m.proposer,
	}
	return b, []byte{0x1}, nil
}

func (m *mockQbft) ValidateProposal(*Proposal) error {
	return nil
}

func (m *mockQbft) Recv() chan *QBFTMessageWithRecipient {
	return nil
}

func (m *mockQbft) Send(msg *QBFTMessageWithRecipient) error {
	m.sentMessages = append(m.sentMessages, msg)
	return nil
}

func TestQuorumCalculation(t *testing.T) {
	cases := []struct {
		num, quorum, faulty uint64
	}{}

	for _, c := range cases {
		quorum, faulty := getQuorumNumbers(c.num)
		require.Equal(t, c.quorum, quorum)
		require.Equal(t, c.faulty, faulty)
	}
}

/*
func TestIsHighestPrepared(t *testing.T) {
	cases := []struct {
		msgs []SignedContainer
		res  *RoundChange
	}{
		{
			[]SignedContainer{
				{
					UnsignedPayload: RoundChange{},
				},
			},
			nil,
		},
		{
			[]SignedContainer{
				{
					UnsignedPayload: RoundChange{
						PreparedRound: 1,
						PreparedValue: []byte{0x1},
					},
				},
			},
			&RoundChange{
				PreparedRound: 1,
				PreparedValue: []byte{0x1},
			},
		},
	}

	for _, c := range cases {
		highestPrepared(c.msgs)
	}
}
*/

type testerAccount struct {
	alias NodeID
}

func (t *testerAccount) sign(payload UnsignedPayload) SignedContainer {
	return SignedContainer{
		UnsignedPayload: payload,
		Signature:       []byte(t.alias),
	}
}

type testerAccountPool struct {
	accounts []*testerAccount
}

func newTesterAccountPool(num ...int) *testerAccountPool {
	t := &testerAccountPool{
		accounts: []*testerAccount{},
	}
	if len(num) == 1 {
		for i := 0; i < num[0]; i++ {
			t.accounts = append(t.accounts, &testerAccount{
				alias: NodeID(strconv.Itoa(i)),
			})
		}
	}
	return t
}

func (ap *testerAccountPool) add(accounts ...string) {
	for _, account := range accounts {
		if acct := ap.get(account); acct != nil {
			continue
		}
		ap.accounts = append(ap.accounts, &testerAccount{
			alias: NodeID(account),
		})
	}
}

func (ap *testerAccountPool) get(name string) *testerAccount {
	for _, account := range ap.accounts {
		if string(account.alias) == name {
			return account
		}
	}
	return nil
}

func (ap *testerAccountPool) validatorSet() ValidatorSet {
	validatorIds := []NodeID{}
	for _, acc := range ap.accounts {
		validatorIds = append(validatorIds, acc.alias)
	}
	return mockValidatorSet(validatorIds)
}

type mockValidatorSet []NodeID

func (m mockValidatorSet) CalculateProposer(round uint64) NodeID {
	seed := uint64(0)

	offset := 0
	// add last proposer

	seed = uint64(offset) + round
	pick := seed % uint64(m.Len())

	return m[pick]
}

func (m mockValidatorSet) index(id NodeID) int {
	for i, currentId := range m {
		if currentId == id {
			return i
		}
	}

	return -1
}

func (m mockValidatorSet) Exists(from NodeID) (uint64, bool) {
	if m.index(from) != -1 {
		return 1, true
	}
	return 0, false
}

func (m mockValidatorSet) VotingPower() uint64 {
	return uint64(m.Len())
}

func (m mockValidatorSet) Len() int {
	return len(m)
}

type mockSigner struct {
	node NodeID
}

func (m *mockSigner) SignMessage(msg []byte) ([]byte, error) {
	return []byte(m.node), nil
}

func (m *mockSigner) RecoverSigner(msg, signature []byte) (NodeID, error) {
	return NodeID(signature), nil
}
