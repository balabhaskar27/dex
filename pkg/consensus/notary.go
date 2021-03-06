package consensus

import (
	"context"
	"fmt"
	"math"
	"time"

	log "github.com/helinwang/log15"
)

// Notary notarizes blocks.
type Notary struct {
	owner Addr
	sk    SK
	share SK
	chain *Chain
	store *storage
}

// NewNotary creates a new notary.
func NewNotary(owner Addr, sk, share SK, chain *Chain, store *storage) *Notary {
	return &Notary{owner: owner, sk: sk, share: share, chain: chain, store: store}
}

// Notarize notarizes block proposals.
//
// It will collect block proposals to notarize until ctx is done, then
// it will notarize the highest weight accumulated block
// proposals. And it will keep notarizing the newly collected block
// proposal if the weight is equal to or greater than the collected
// block proposals until cancel context is done.
func (n *Notary) Notarize(ctx, cancel context.Context, bCh chan *BlockProposal, onNotarize func(*NtShare, time.Duration)) {
	var bestRankBPs []*BlockProposal
	bestRank := uint16(math.MaxUint16)
	recvBestRank := false
	recvBestRankCh := make(chan struct{})
	notarize := func() {
		for _, bp := range bestRankBPs {
			s, dur := n.notarize(bp, n.chain.txnPool)
			if s != nil {
				onNotarize(s, dur)
			}
		}

		for {
			select {
			case <-cancel.Done():
				return
			case bp := <-bCh:
				rank, err := n.chain.randomBeacon.Rank(bp.Owner, bp.Round)
				if err != nil {
					log.Error("get rank error", "err", err, "bp round", bp.Round)
					continue
				}

				if rank <= bestRank {
					bestRank = rank
					s, dur := n.notarize(bp, n.chain.txnPool)
					if s != nil {
						onNotarize(s, dur)
					}
				}
			}
		}
	}

	for {
		select {
		case <-recvBestRankCh:
			notarize()
			return
		case <-ctx.Done():
			notarize()
			return
		case bp := <-bCh:
			rank, err := n.chain.randomBeacon.Rank(bp.Owner, bp.Round)
			if err != nil {
				log.Error("get rank error", "err", err, "bp round", bp.Round)
				continue
			}

			if rank == 0 && !recvBestRank {
				recvBestRank = true
				close(recvBestRankCh)
			}

			if len(bestRankBPs) == 0 {
				bestRankBPs = []*BlockProposal{bp}
				bestRank = rank
				continue
			}

			if rank < bestRank {
				bestRankBPs = []*BlockProposal{bp}
				bestRank = rank
			} else if rank == bestRank {
				bestRankBPs = append(bestRankBPs, bp)
			}
		case <-cancel.Done():
			return
		}
	}
}

func (n *Notary) notarize(bp *BlockProposal, pool TxnPool) (*NtShare, time.Duration) {
	bpHash := bp.Hash()
	nts := &NtShare{
		Round: bp.Round,
		BP:    bpHash,
	}

	prevBlock := n.store.Block(bp.PrevBlock)
	if prevBlock == nil {
		panic(fmt.Errorf("should not happen: can not find pre block %v, bp: %v", bp.PrevBlock, bpHash))
	}

	state := n.chain.BlockState(bp.PrevBlock)
	if state == nil {
		panic(fmt.Errorf("should not happen: can not find the state of pre block %v, bp: %v", bp.PrevBlock, bpHash))
	}

	start := time.Now()
	newState, _, err := state.CommitTxns(bp.Txns, pool, bp.Round)
	if err != nil {
		panic("should not happen, record block proposal transaction error, could be due to adversary: " + err.Error())
	}

	dur := time.Now().Sub(start)
	log.Debug("notarize record txns done", "round", nts.Round, "bp", nts.BP, "dur", dur)

	stateRoot := newState.Hash()
	blk := &Block{
		Owner:         bp.Owner,
		Round:         bp.Round,
		StateRoot:     stateRoot,
		BlockProposal: bpHash,
		PrevBlock:     bp.PrevBlock,
	}

	nts.StateRoot = stateRoot
	nts.BP = bpHash
	nts.SigShare = n.share.Sign(blk.Encode(false))
	nts.Owner = n.owner
	nts.Sig = n.sk.Sign(nts.Encode(false))
	return nts, dur
}
