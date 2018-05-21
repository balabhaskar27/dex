package consensus

import (
	"errors"
	"fmt"
	"sync"
)

var errCommitteeNotSelected = errors.New("committee not selected yet")
var errAddrNotInCommittee = errors.New("addr not in committee")

// RoundInfo is the round information.
//
// The random beacon, block proposal, block notarization advance to
// the next round in lockstep.
type RoundInfo struct {
	mu                sync.Mutex
	nextRBCmteHistory []int
	nextNtCmteHistory []int
	nextBPCmteHistory []int
	groups            []*Group

	rbRand Rand
	ntRand Rand
	bpRand Rand

	curRoundShares []*RandBeaconSigShare
}

// TODO: maybe rename RoundInfo to Context, or RandomBeacon

// NewRoundInfo creates a new round info.
func NewRoundInfo(seed Rand, groups []*Group) *RoundInfo {
	rbRand := seed.Derive([]byte("random beacon committee rand seed"))
	bpRand := seed.Derive([]byte("block proposer committee rand seed"))
	ntRand := seed.Derive([]byte("notarization committee rand seed"))
	return &RoundInfo{
		groups:            groups,
		rbRand:            rbRand,
		bpRand:            bpRand,
		ntRand:            ntRand,
		nextRBCmteHistory: []int{rbRand.Mod(len(groups))},
		nextNtCmteHistory: []int{ntRand.Mod(len(groups))},
		nextBPCmteHistory: []int{bpRand.Mod(len(groups))},
	}
}

// RecvRandBeaconSigShare receives one share of the random beacon
// signature.
func (r *RoundInfo) RecvRandBeaconSigShare(s *RandBeaconSigShare, groupID int) (*RandBeaconSig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.randRound() != s.Round {
		return nil, fmt.Errorf("unexpected RandBeaconSigShare round: %d, expected: %d", s.Round, r.randRound())
	}

	r.curRoundShares = append(r.curRoundShares, s)
	if len(r.curRoundShares) >= groupThreshold {
		sig := recoverRandBeaconSig(r.curRoundShares)
		var rbs RandBeaconSig
		rbs.LastRandVal = s.LastRandVal
		rbs.Round = s.Round
		msg := rbs.Encode(false)
		if !sig.Verify(&r.groups[groupID].PK, string(msg)) {
			panic("impossible: random beacon group signature verification failed")
		}

		rbs.Sig = sig.Serialize()
		return &rbs, nil
	}
	return nil, nil
}

// RecvRandBeaconSig adds the random beacon signature.
func (r *RoundInfo) RecvRandBeaconSig(s *RandBeaconSig) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.randRound() != s.Round {
		return fmt.Errorf("unexpected RandBeaconSig round: %d, expected: %d", s.Round, r.randRound())
	}

	r.deriveRand(hash(s.Sig))
	r.curRoundShares = nil
	return nil
}

func (r *RoundInfo) randRound() int {
	return len(r.nextRBCmteHistory)
}

func (r *RoundInfo) deriveRand(h Hash) {
	r.rbRand = r.rbRand.Derive(h[:])
	r.nextRBCmteHistory = append(r.nextRBCmteHistory, r.rbRand.Mod(len(r.groups)))
	r.ntRand = r.ntRand.Derive(h[:])
	r.nextNtCmteHistory = append(r.nextNtCmteHistory, r.ntRand.Mod(len(r.groups)))
	r.bpRand = r.bpRand.Derive(h[:])
	r.nextBPCmteHistory = append(r.nextBPCmteHistory, r.bpRand.Mod(len(r.groups)))
}
