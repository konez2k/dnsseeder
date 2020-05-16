package main

import (
	"log"
	"net"
	"os"
	"strconv"
	"time"

	"errors"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/peer"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btclog"
)

type crawlError struct {
	errLoc string
	Err    error
}

// Error returns a formatted error about a crawl
func (e *crawlError) Error() string {
	return "err: " + e.errLoc + ": " + e.Err.Error()
}

// crawlNode runs in a goroutine, crawls the remote ip and updates the master
// list of currently active addresses
func crawlNode(rc chan *result, s *dnsseeder, nd *node) {

	res := &result{
		node: net.JoinHostPort(nd.na.IP.String(), strconv.Itoa(int(nd.na.Port))),
	}

	// connect to the remote ip and ask them for their addr list
	res.nas, res.msg = crawlIP(s, res)

	// all done so push the result back to the seeder.
	//This will block until the seeder reads the result
	rc <- res

	// goroutine will end and be cleaned up
}

// crawlIP retrievs a slice of ip addresses from a client
func crawlIP(s *dnsseeder, r *result) ([]*wire.NetAddress, *crawlError) {
	verack := make(chan struct{})
	onAddr := make(chan *wire.MsgAddr)

	chainParams := &chaincfg.MainNetParams
	chainParams.Net = s.id

	peerCfg := &peer.Config{
		UserAgentName:    "bitcoin-seeder", // User agent name to advertise.
		UserAgentVersion: "1.0.0",          // User agent version to advertise.
		ChainParams:      chainParams,
		ProtocolVersion:  s.pver,
		Services:         0,
		Listeners: peer.MessageListeners{
			OnAddr: func(p *peer.Peer, msg *wire.MsgAddr) {
				onAddr <- msg
			},
			OnVersion: func(p *peer.Peer, msg *wire.MsgVersion) *wire.MsgReject {
				if config.debug {
					log.Printf("%s - debug - %s - Remote version: %v\n", s.name, r.node, msg.ProtocolVersion)
				}
				// fill the node struct with the remote details
				r.version = msg.ProtocolVersion
				r.services = msg.Services
				r.lastBlock = msg.LastBlock
				r.strVersion = msg.UserAgent
				return nil
			},
			OnVerAck: func(p *peer.Peer, msg *wire.MsgVerAck) {
				verack <- struct{}{}
			},
		},
	}

	if config.debug {
		logger := btclog.NewBackend(os.Stdout).Logger(s.name)
		logger.SetLevel(btclog.LevelDebug)
		peer.UseLogger(logger)
	}

	p, err := peer.NewOutboundPeer(peerCfg, r.node)
	if err != nil {
		return nil, &crawlError{"NewOutboundPeer: error", err}
	}

	// Establish the connection to the peer address and mark it connected.
	conn, err := net.Dial("tcp", p.Addr())
	if err != nil {
		return nil, &crawlError{"net.Dial: error", err}
	}
	p.AssociateConnection(conn)

	// Wait for the verack message or timeout in case of failure.
	select {
	case <-verack:
	case <-time.After(time.Second * 5):
		return nil, &crawlError{"Verack timeout", errors.New("")}
	}

	// if we get this far and if the seeder is full then don't ask for addresses. This will reduce bandwith usage while still
	// confirming that we can connect to the remote node
	if len(s.theList) > s.maxSize {
		return nil, nil
	}

	// send getaddr command
	p.QueueMessage(wire.NewMsgGetAddr(), nil)

	addrMsg := new(wire.MsgAddr)
	select {
	case addrMsg = <-onAddr:
	case <-time.After(time.Second * 6):
	}

	// Disconnect the peer.
	p.Disconnect()
	p.WaitForDisconnect()

	return addrMsg.AddrList, nil
}
