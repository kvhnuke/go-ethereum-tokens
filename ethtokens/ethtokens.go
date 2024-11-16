// Copyright 2016 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

// Package ethtokens
package ethtokens

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rpc"
)

const (
	chainHeadChanSize = 1024
	lastSynced        = "lastsynced"
	maxROitems        = 1024 * 10
)

// New returns a monitoring service ready for stats reporting.
func New(node *node.Node, backend backend, engine consensus.Engine) error {
	ethtokens := &Service{
		backend: backend,
		engine:  engine,
		server:  node.Server(),
		pongCh:  make(chan struct{}),
		histCh:  make(chan []uint64, 1),
		db:      rawdb.NewTable(backend.ChainDb(), rawdb.TokenBalancePrefix),
	}
	node.RegisterLifecycle(ethtokens)
	return nil
}
func (s *Service) Start() error {
	// Subscribe to chain events to execute updates on
	chainHeadCh := make(chan core.ChainHeadEvent, chainHeadChanSize)
	s.headSub = s.backend.SubscribeChainHeadEvent(chainHeadCh)
	s.lastSyncedBlock = common.Big0
	lastSynced, _ := s.db.Get([]byte(lastSynced))
	if len(lastSynced) > 0 {
		s.lastSyncedBlock = new(big.Int).SetBytes(lastSynced)
	}
	go s.loop(chainHeadCh)
	log.Info("token daemon started")
	return nil
}

func (s *Service) Stop() error {
	s.headSub.Unsubscribe()
	log.Info("token daemon stopped")
	return nil
}

func (s *Service) syncOneBlock(blockNumber int64) {
	header, _ := s.backend.HeaderByNumber(context.Background(), rpc.BlockNumber(blockNumber))
	receipts, _ := s.backend.GetReceipts(context.Background(), header.Hash())
	if blockNumber%10000 == 0 || new(big.Int).Sub(s.maxblock, s.lastSyncedBlock).Cmp(big.NewInt(1000)) <= 0 {
		fmt.Printf("syncing %d\n", blockNumber)
	}
	batchdb := s.db.NewBatch()
	for _, r := range receipts {
		for _, h := range r.Logs {
			if len(h.Topics) == 3 && h.Topics[0].Hex() == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" {
				fromAddress := common.BytesToAddress(h.Topics[1].Bytes())
				toAddress := common.BytesToAddress(h.Topics[2].Bytes())
				//	fmt.Printf("%s %s %s %d\n", h.Address, fromAddress, toAddress, h.BlockNumber)
				batchdb.Put(append(fromAddress.Bytes(), h.Address.Bytes()...), h.Address.Bytes())
				batchdb.Put(append(toAddress.Bytes(), h.Address.Bytes()...), h.Address.Bytes())
			}
		}
	}
	batchdb.Write()
}
func (s *Service) startSyncing() {
	s.syncing = true
	for i := new(big.Int).Set(s.lastSyncedBlock); i.Cmp(s.maxblock) <= 0; i.Add(i, common.Big1) {
		s.syncOneBlock(i.Int64())
		s.lastSyncedBlock = i
		s.db.Put([]byte(lastSynced), i.Bytes())
	}
	s.syncing = false
}
func (s *Service) loop(chainHeadCh chan core.ChainHeadEvent) {
	go func() {
	HandleLoop:
		for {
			select {
			case head := <-chainHeadCh:
				s.maxblock = head.Block.Number()
				if !s.syncing {
					go s.startSyncing()
				} else {
					s.syncOneBlock(head.Block.Number().Int64())
				}
				fmt.Printf("%s \n", head.Block.Number())
			case <-s.headSub.Err():
				break HandleLoop
			}
		}
	}()

}

type backend interface {
	SubscribeChainHeadEvent(ch chan<- core.ChainHeadEvent) event.Subscription
	SubscribeChainEvent(ch chan<- core.ChainEvent) event.Subscription
	SubscribeNewTxsEvent(ch chan<- core.NewTxsEvent) event.Subscription
	SubscribeLogsEvent(ch chan<- []*types.Log) event.Subscription
	CurrentHeader() *types.Header
	HeaderByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Header, error)
	GetReceipts(ctx context.Context, hash common.Hash) (types.Receipts, error)
	Stats() (pending int, queued int)
	ChainDb() ethdb.Database
}

// Service implements an Ethereum netstats reporting daemon that pushes local
// chain statistics up to a monitoring server.
type Service struct {
	server  *p2p.Server // Peer-to-peer server to retrieve networking infos
	backend backend
	engine  consensus.Engine // Consensus engine to retrieve variadic block fields

	pongCh chan struct{} // Pong notifications are fed into this channel
	histCh chan []uint64 // History request block numbers are fed into this channel

	headSub         event.Subscription
	db              ethdb.Database
	syncing         bool
	maxblock        *big.Int
	lastSyncedBlock *big.Int
}
