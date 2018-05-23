package consensus

import (
	"errors"
	"fmt"
	"sync"

	"github.com/dfinity/go-dfinity-crypto/bls"
)

var errChainDataAlreadyExists = errors.New("chain data already exists")

type finalized struct {
	Block Hash
	BP    Hash
}

type unNotarized struct {
	BP     Hash
	Weight float64
}

type notarized struct {
	Block  Hash
	Weight float64

	NtChildren    []*notarized
	NonNtChildren []*unNotarized

	BP       Hash
	State    State
	SysState *SysState
}

// Chain is the blockchain.
type Chain struct {
	cfg          Config
	RandomBeacon *RandomBeacon
	n            *Node

	mu sync.RWMutex
	// the finalized block burried deep enough becomes part of the
	// history. Its block proposal and state will be discarded to
	// save space.
	History             []Hash
	LastHistoryState    State
	LastHistorySysState *SysState
	// reorg will never happen to the finalized block, we will
	// discard its associated state. The block proposal will not
	// be discarded, so when a new client joins, he can replay the
	// block proposals starting from LastHistoryState, verify the
	// new state root hash against the one stored in the next
	// block.
	Finalized             []*finalized
	LastFinalizedState    State
	LastFinalizedSysState *SysState
	Fork                  []*notarized
	UnNotarizedNotOnFork  []*unNotarized
	hashToBlock           map[Hash]*Block
	hashToBP              map[Hash]*BlockProposal
	hashToNtShare         map[Hash]*NtShare
	bpToNtShares          map[Hash][]*NtShare
	bpNeedNotarize        map[Hash]bool
}

// NewChain creates a new chain.
func NewChain(genesis *Block, genesisState State, seed Rand, cfg Config) *Chain {
	sysState := NewSysState()
	t := sysState.Transition()
	for _, txn := range genesis.SysTxns {
		valid := t.Record(txn)
		if !valid {
			panic("sys txn in genesis is invalid")
		}
	}

	sysState = t.Apply()
	sysState.Finalized()
	gh := genesis.Hash()
	return &Chain{
		cfg:                 cfg,
		RandomBeacon:        NewRandomBeacon(seed, sysState.groups, cfg),
		History:             []Hash{gh},
		LastHistoryState:    genesisState,
		LastHistorySysState: sysState,
		hashToBlock:         map[Hash]*Block{gh: genesis},
		hashToBP:            make(map[Hash]*BlockProposal),
		hashToNtShare:       make(map[Hash]*NtShare),
		bpToNtShares:        make(map[Hash][]*NtShare),
		bpNeedNotarize:      make(map[Hash]bool),
	}
}

// Block returns the block of the given hash.
func (c *Chain) Block(h Hash) (*Block, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	b, ok := c.hashToBlock[h]
	return b, ok
}

// BlockProposal returns the block of the given hash.
func (c *Chain) BlockProposal(h Hash) (*BlockProposal, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	b, ok := c.hashToBP[h]
	return b, ok
}

// NtShare returns the notarization share of the given hash.
func (c *Chain) NtShare(h Hash) (*NtShare, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	b, ok := c.hashToNtShare[h]
	return b, ok
}

// NeedNotarize returns if the block proposal of the given hash needs
// to be notarized.
func (c *Chain) NeedNotarize(h Hash) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	b, ok := c.bpNeedNotarize[h]
	if ok {
		return b
	}

	return false
}

// FinalizedChain returns the finalized block chain.
func (c *Chain) FinalizedChain() []*Block {
	var bs []*Block
	for _, b := range c.History {
		bs = append(bs, c.hashToBlock[b])
	}

	for _, b := range c.Finalized {
		bs = append(bs, c.hashToBlock[b.Block])
	}

	return bs
}

func (c *Chain) round() int {
	round := len(c.History)
	round += len(c.Finalized)
	round += maxHeight(c.Fork)
	return round
}

// Round returns the current round.
func (c *Chain) Round() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.round()
}

func maxHeight(ns []*notarized) int {
	max := 0
	for _, n := range ns {
		h := maxHeight(n.NtChildren) + 1
		if max < h {
			max = h
		}
	}
	return max
}

func (c *Chain) heaviestFork() *notarized {
	// TODO: implement correctly
	n := c.Fork[0]
	for len(n.NtChildren) > 0 {
		n = n.NtChildren[0]
	}

	return n
}

// Leader returns the notarized block of the current round whose chain
// is the heaviest.
func (c *Chain) Leader() (*Block, State, *SysState) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.Finalized) == 0 {
		if len(c.Fork) == 0 {
			return c.hashToBlock[c.History[len(c.History)-1]], c.LastHistoryState, c.LastHistorySysState
		}
	} else {
		if len(c.Fork) == 0 {
			return c.hashToBlock[c.Finalized[len(c.Finalized)-1].Block], c.LastFinalizedState, c.LastFinalizedSysState
		}
	}

	n := c.heaviestFork()
	return c.hashToBlock[n.Block], n.State, n.SysState
}

func findPrevBlock(prevBlock Hash, ns []*notarized) *notarized {
	for _, notarized := range ns {
		if notarized.Block == prevBlock {
			return notarized
		}

		n := findPrevBlock(prevBlock, notarized.NtChildren)
		if n != nil {
			return n
		}
	}

	return nil
}

func (c *Chain) addBP(bp *BlockProposal, weight float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := bp.Hash()

	if _, ok := c.hashToBP[h]; ok {
		return errChainDataAlreadyExists
	}

	notarized := findPrevBlock(bp.PrevBlock, c.Fork)
	if notarized == nil {
		if len(c.Finalized) > 0 {
			if c.Finalized[len(c.Finalized)-1].Block != bp.PrevBlock {
				return fmt.Errorf("block proposal's parent not found: %x, round: %d", bp.PrevBlock, bp.Round)
			}
		}

		if c.History[len(c.History)-1] != bp.PrevBlock {
			return fmt.Errorf("block proposal's parent not found: %x, round: %d", bp.PrevBlock, bp.Round)
		}
	}

	c.hashToBP[h] = bp
	u := &unNotarized{Weight: weight, BP: h}

	if notarized != nil {
		notarized.NonNtChildren = append(notarized.NonNtChildren, u)
	} else {
		c.UnNotarizedNotOnFork = append(c.UnNotarizedNotOnFork, u)
		// TODO: delete unnotarized when receive notarized
	}
	c.bpNeedNotarize[h] = true
	go c.n.RecvBlockProposal(bp)
	return nil
}

func (c *Chain) addNtShare(n *NtShare, groupID int) (*Block, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	bp, ok := c.hashToBP[n.BP]
	if !ok {
		return nil, errors.New("block proposal not found")
	}

	if !c.bpNeedNotarize[n.BP] {
		return nil, errors.New("block proposal do not need notarization")
	}

	for _, s := range c.bpToNtShares[n.BP] {
		if s.Owner == n.Owner {
			return nil, errors.New("notarization share from the owner already received")
		}
	}

	c.bpToNtShares[n.BP] = append(c.bpToNtShares[n.BP], n)

	if len(c.bpToNtShares[n.BP]) >= c.cfg.GroupThreshold {
		sig, err := recoverNtSig(c.bpToNtShares[n.BP])
		if err != nil {
			// should not happen
			panic(err)
		}

		if !c.validateGroupSig(sig, groupID, bp) {
			panic("impossible: group sig not valid")
		}

		b := &Block{
			Round:           bp.Round,
			StateRoot:       n.StateRoot,
			BlockProposal:   n.BP,
			PrevBlock:       bp.PrevBlock,
			SysTxns:         bp.SysTxns,
			NotarizationSig: sig.Serialize(),
		}

		delete(c.bpNeedNotarize, n.BP)
		for _, share := range c.bpToNtShares[n.BP] {
			delete(c.hashToNtShare, share.Hash())
		}
		delete(c.bpToNtShares, n.BP)
		return b, nil
	}

	c.hashToNtShare[n.Hash()] = n
	return nil, nil
}

func (c *Chain) addBlock(b *Block, weight float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	prevRound := c.round()

	h := b.Hash()
	if _, ok := c.hashToBlock[h]; ok {
		return errors.New("block already exists")
	}

	if _, ok := c.hashToBP[b.BlockProposal]; !ok {
		return errors.New("block's proposal not found")
	}

	var prevState State
	var prevSysState *SysState

	nt := &notarized{Block: h, Weight: weight, BP: b.BlockProposal}
	prev := findPrevBlock(b.PrevBlock, c.Fork)
	if prev != nil {
		prevState = prev.State
		prevSysState = prev.SysState
	} else if len(c.Finalized) > 0 && c.Finalized[len(c.Finalized)-1].Block == b.PrevBlock {
		prevState = c.LastFinalizedState
		prevSysState = c.LastFinalizedSysState
	} else if c.History[len(c.History)-1] == b.PrevBlock {
		prevState = c.LastHistoryState
		prevSysState = c.LastHistorySysState
	} else {
		return errors.New("can not connect block to the chain")
	}

	// TODO: update state
	nt.State = prevState

	// TODO: update sys state once need to support system txn.
	nt.SysState = prevSysState

	// TODO: independently generate the state root and verify state root hash

	if prev != nil {
		prev.NtChildren = append(prev.NtChildren, nt)
	} else {
		c.Fork = append(c.Fork, nt)
	}

	// TODO: finalize blocks

	c.hashToBlock[h] = b
	delete(c.bpNeedNotarize, b.BlockProposal)
	delete(c.bpToNtShares, b.BlockProposal)

	round := c.round()
	fmt.Println("recv block", round, prevRound, c.RandomBeacon.Round())
	if round == prevRound+1 && round == c.RandomBeacon.Round() {
		go c.n.StartRound(round)
	}
	return nil
}

func (c *Chain) validateGroupSig(sig bls.Sign, groupID int, bp *BlockProposal) bool {
	msg := bp.Encode(true)
	return sig.Verify(&c.RandomBeacon.groups[groupID].PK, string(msg))
}
