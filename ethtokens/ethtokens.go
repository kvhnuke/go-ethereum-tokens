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
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
)

const (
	chainHeadChanSize = 10
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
	chainHeadCh := make(chan []*types.Log, chainHeadChanSize)
	s.headSub = s.backend.SubscribeLogsEvent(chainHeadCh)
	go s.loop(chainHeadCh)

	log.Info("token daemon started")
	return nil
}

func (s *Service) Stop() error {
	s.headSub.Unsubscribe()
	log.Info("token daemon stopped")
	return nil
}
func contains(s []common.Address, e common.Address) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}
func (s *Service) loop(chainHeadCh chan []*types.Log) {
	// Start a goroutine that exhausts the subscriptions to avoid events piling up
	go func() {

	HandleLoop:
		for {
			select {
			// Notify of chain head events, but drop if too frequent
			case head := <-chainHeadCh:
				for _, h := range head {
					if len(h.Topics) == 3 && h.Topics[0].Hex() == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" {
						fromAddress := common.BytesToAddress(h.Topics[1].Bytes())
						toAddress := common.BytesToAddress(h.Topics[2].Bytes())
						//fmt.Printf("%s %s %s %d\n", h.Address, fromAddress, toAddress, h.BlockNumber)
						saveToDB := func(ownerAddress common.Address, data []common.Address) {
							cBytes, err := rlp.EncodeToBytes(data)
							if err != nil {
								fmt.Printf("%s/n", err)
							} else {
								err := s.db.Put(ownerAddress.Bytes(), cBytes)
								if err != nil {
									fmt.Printf("%s/n", err)
								}
							}
						}
						checkAndAdd := func(tokenAddress common.Address, ownerAddress common.Address) {
							var contracts []common.Address
							if data, _ := s.db.Get(ownerAddress.Bytes()); len(data) != 0 {
								rlp.DecodeBytes(data, &contracts)
								if !contains(contracts, tokenAddress) {
									contracts = append(contracts, tokenAddress)
									saveToDB(ownerAddress, contracts)
								}
							} else {
								contracts = append(contracts, tokenAddress)
								saveToDB(ownerAddress, contracts)
							}
						}
						checkAndAdd(h.Address, fromAddress)
						checkAndAdd(h.Address, toAddress)
					}
				}
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
	GetTd(ctx context.Context, hash common.Hash) *big.Int
	Stats() (pending int, queued int)
	Downloader() *downloader.Downloader
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

	headSub event.Subscription
	db      ethdb.Database
}
