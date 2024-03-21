package network

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"net"
	"os"
	"time"

	core "github.com/AzlanAmjad/DreamscapeCanvas-Blockchain/blockchain-core"
	crypto "github.com/AzlanAmjad/DreamscapeCanvas-Blockchain/cryptography"
	"github.com/go-kit/log"
	"github.com/sirupsen/logrus"
)

// The server is a container which will contain every module of the server.
// 1. Nodes / servers can be BlockExplorers, Wallets, and other nodes, to strengthen the network.
// 1. Nodes / servers can also be validators, that participate in the consensus algorithm. They
// can be elected to create new blocks, and validate transactions, and propose blocks into the network.

var defaultBlockTime = 5 * time.Second

// One node can have multiple transports, for example, a TCP transport and a UDP transport.
type ServerOptions struct {
	SeedNodes     []net.Addr
	Addr          net.Addr
	ID            string
	Logger        log.Logger
	RPCDecodeFunc RPCDecodeFunc
	RPCProcessor  RPCProcessor
	PrivateKey    *crypto.PrivateKey
	// if a node / server is elect as a validator, server needs to know when its time to consume its mempool and create a new block.
	BlockTime      time.Duration
	MaxMemPoolSize int
}

type Server struct {
	TCPTransport  *TCPTransport
	Peers         map[net.Addr]*TCPPeer
	ServerOptions ServerOptions
	isValidator   bool
	// holds transactions that are not yet included in a block.
	memPool     *TxPool
	rpcChannel  chan ReceiveRPC
	peerChannel chan *TCPPeer
	quitChannel chan bool
	chain       *core.Blockchain
}

// NewServer creates a new server with the given options.
func NewServer(options ServerOptions) (*Server, error) {
	// setting default values if none are specified
	if options.BlockTime == time.Duration(0) {
		options.BlockTime = defaultBlockTime
	}
	if options.Logger == nil {
		options.Logger = log.NewLogfmtLogger(os.Stderr)
		options.Logger = log.With(options.Logger, "ID", options.ID)
	}
	if options.MaxMemPoolSize == 0 {
		options.MaxMemPoolSize = 100
	}

	// create the default LevelDB storage
	dbPath := fmt.Sprintf("./leveldb/%s/blockchain", options.ID)
	storage, err := core.NewLevelDBStorage(dbPath)
	if err != nil {
		return nil, err
	}
	// create new blockchain with the LevelDB storage
	bc, err := core.NewBlockchain(storage, genesisBlock(), options.ID)
	if err != nil {
		return nil, err
	}

	// create a channel to receive peers from the transport
	peerCh := make(chan *TCPPeer)
	// create the TCP transport
	tcpTransport, err := NewTCPTransport(options.Addr, peerCh, options.ID)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to create TCP transport")
	}

	s := &Server{
		TCPTransport:  tcpTransport,
		Peers:         make(map[net.Addr]*TCPPeer),
		ServerOptions: options,
		// Validators will have private keys, so they can sign blocks.
		// Note: only one validator can be elected, one source of truth, one miner in the whole network.
		isValidator: options.PrivateKey != nil,
		memPool:     NewTxPool(options.MaxMemPoolSize),
		rpcChannel:  make(chan ReceiveRPC),
		peerChannel: peerCh,
		quitChannel: make(chan bool),
		chain:       bc,
	}

	// Set the default RPC decoder.
	if s.ServerOptions.RPCDecodeFunc == nil {
		s.ServerOptions.RPCDecodeFunc = DefaultRPCDecoder
	}

	// Set the default RPC processor.
	if s.ServerOptions.RPCProcessor == nil {
		s.ServerOptions.RPCProcessor = s
	}

	// Goroutine (Thread) to process creation of blocks if the node is a validator.
	if s.isValidator {
		go s.validatorLoop()
	}

	// log server creation
	s.ServerOptions.Logger.Log(
		"msg", "server created",
		"server_id", s.ServerOptions.ID,
		"validator", s.isValidator,
	)

	return s, nil
}

// dial the seed nodes, and add them to the list of peers
func (s *Server) bootstrapNetwork() {
	// dial the seed nodes
	for _, seed := range s.ServerOptions.SeedNodes {
		// make sure the peer is not ourselves
		if s.TCPTransport.Addr.String() != seed.String() {
			// dial the seed node
			conn, err := net.Dial("tcp", seed.String())
			if err != nil {
				logrus.WithError(err).Error("Failed to dial seed node")
				continue
			}

			fmt.Println("Dialed seed node", seed.String())

			// create a new peer
			peer := &TCPPeer{conn: conn, Incoming: false} // we initiated this connection so Incoming is false
			// send peer to server peer channel
			s.peerChannel <- peer
		} else {
			// log that we are not dialing ourselves
			s.ServerOptions.Logger.Log("msg", "We are a seed node, not dialing ourselves", "seed", seed)
		}
	}
}

// Start will start the server.
// This is the core of the server, where the server will listen for messages from the transports.
// We make calls to helper functions for processing from here.
func (s *Server) Start() error {
	s.ServerOptions.Logger.Log("msg", "Starting main server loop", "server_id", s.ServerOptions.ID)
	// start the transport
	s.TCPTransport.Start()
	// bootstrap the network
	go s.bootstrapNetwork()

	// Run the server in a loop forever.
	for {
		select {
		// receive a new peer, we either connected via
		// 1. Seed node dialing
		// 2. Another peer dialed us
		case peer := <-s.peerChannel:
			// TODO (Azlan): Add mutex to protect the Peers map.
			// Add the peer to the list of peers.
			s.Peers[peer.conn.RemoteAddr()] = peer
			// print that peer was added
			s.ServerOptions.Logger.Log("msg", "Peer added", "us", peer.conn.LocalAddr(), "peer", peer.conn.RemoteAddr())
			// start reading from the peer
			go peer.readLoop(s.rpcChannel)

			// Probably not the right place to do this, but we need peers to exist before sending getstatus
			err := s.broadcastGetStatus()
			if err != nil {
				logrus.WithError(err).Error("Failed to broadcast getStatus")
			}
		// receive RPC message to process
		case rpc := <-s.rpcChannel:
			// Decode the message.
			decodedMessage, err := s.ServerOptions.RPCDecodeFunc(rpc)
			if err != nil {
				logrus.WithError(err).Error("Failed to decode message")
				continue
			}
			// Process the message.
			err = s.ServerOptions.RPCProcessor.ProcessMessage(rpc.From, decodedMessage)
			if err != nil {
				logrus.WithError(err).Error("Failed to process message")
			}
		case <-s.quitChannel:
			// log the quit signal
			s.ServerOptions.Logger.Log("msg", "Received quit signal", "server_id", s.ServerOptions.ID)
			// Quit the server.
			fmt.Println("Quitting the server.")
			s.handleQuit()
			return nil
		}
	}
}

// validatorLoop will create a new block every block time.
// validatorLoop is only started if the server is a validator.
func (s *Server) validatorLoop() {
	s.ServerOptions.Logger.Log("msg", "Starting validator loop", "server_id", s.ServerOptions.ID)

	// Create a ticker to create a new block every block time.
	ticker := time.NewTicker(s.ServerOptions.BlockTime)
	for {
		select {
		case <-s.quitChannel:
			return
		case <-ticker.C:
			// Here we will include CONSENSUS LOGIC / LEADER ELECTION LOGIC / BLOCK CREATION LOGIC.
			// If the server is the elected validator, create a new block.
			err := s.createNewBlock()
			if err != nil {
				logrus.WithError(err).Error("Failed to create new block")
			}
		}
	}
}

func (s *Server) ProcessMessage(from net.Addr, decodedMessage *DecodedMessage) error {
	s.ServerOptions.Logger.Log("msg", "Processing message", "type", decodedMessage.Header, "from", decodedMessage.From)

	switch decodedMessage.Header {
	case Transaction:
		tx, ok := decodedMessage.Message.(core.Transaction)
		if !ok {
			return fmt.Errorf("failed to cast message to transaction")
		}
		return s.processTransaction(from, &tx)
	case Block:
		block, ok := decodedMessage.Message.(core.Block)
		if !ok {
			return fmt.Errorf("failed to cast message to block")
		}
		return s.processBlock(from, &block)
	case GetStatus:
		s.ServerOptions.Logger.Log("msg", "Handling GetStatus", "msg", decodedMessage.Message)
		getStatus, ok := decodedMessage.Message.(core.GetStatusMessage)
		if !ok {
			fmt.Printf("error: %s", getStatus)
			return fmt.Errorf("failed to cast message to getstatus")
		}
		return s.processGetStatus(from, &getStatus)
	case Status:
		status, ok := decodedMessage.Message.(core.StatusMessage)
		if !ok {
			return fmt.Errorf("failed to cast message to status")
		}
		return s.processStatus(from, &status)
	default:
		return fmt.Errorf("unknown message type: %d", decodedMessage.Header)
	}
}

func (s *Server) broadcast(from net.Addr, payload []byte) error {
	s.ServerOptions.Logger.Log("asd", "broadcasting to", len(s.Peers))
	for _, peer := range s.Peers {
		// don't send the message back to the peer that sent it to us
		// nil condition is checked because this data might not be from anyone
		// but from the node itself, for example, when a new block is created.
		if from == nil || peer.conn.RemoteAddr().String() != from.String() {
			err := peer.Send(payload)
			if err != nil {
				logrus.WithError(err).Error("Failed to send message to peer")
			}
		}
	}
	return nil
}

// broadcast the transaction to all known peers, eventually consistency model
func (s *Server) broadcastTx(from net.Addr, tx *core.Transaction) {
	//s.ServerOptions.Logger.Log("msg", "Broadcasting transaction", "hash", tx.GetHash(s.memPool.TransactionHasher))

	// encode the transaction, and put it in a message, and broadcast it to the network.
	transactionBytes := bytes.Buffer{}
	enc := core.NewTransactionEncoder()
	err := tx.Encode(&transactionBytes, enc)
	if err != nil {
		logrus.WithError(err).Error("Failed to encode transaction")
		return
	}
	msg := NewMessage(Transaction, transactionBytes.Bytes())
	err = s.broadcast(from, msg.Bytes())
	if err != nil {
		logrus.WithError(err).Error("Failed to broadcast transaction")
	}
}

// broadcast the block to all known peers, eventually consistency model
func (s *Server) broadcastBlock(from net.Addr, block *core.Block) {
	s.ServerOptions.Logger.Log("msg", "Broadcasting block", "hash", block.GetHash(s.chain.BlockHeaderHasher))

	// encode the block, and put it in a message, and broadcast it to the network.
	blockBytes := bytes.Buffer{}
	err := block.Encode(&blockBytes, s.chain.BlockEncoder)
	if err != nil {
		logrus.WithError(err).Error("Failed to encode block")
		return
	}
	msg := NewMessage(Block, blockBytes.Bytes())
	err = s.broadcast(from, msg.Bytes())
	if err != nil {
		logrus.WithError(err).Error("Failed to broadcast block")
	}
}

func (s *Server) broadcastGetStatus() error {
	getStatusBytes := bytes.Buffer{}
	getStatusMessage := new(core.GetStatusMessage)

	if err := gob.NewEncoder(&getStatusBytes).Encode(getStatusMessage); err != nil {
		return err
	}

	msg := NewMessage(GetStatus, getStatusBytes.Bytes())

	s.ServerOptions.Logger.Log("msg", "Broadcasting get status", "Message", getStatusBytes.Bytes())

	err := s.broadcast(nil, msg.Bytes())
	if err != nil {
		logrus.WithError(err).Error("Failed to broadcast block")
	}
	return nil
}

// handling blocks coming into the blockchain
// two ways:
// 1. through another block that is forwarding the block to known peers
// 2. the leader node chosen through consensus has created a new block, and is broadcasting it to us
func (s *Server) processBlock(from net.Addr, block *core.Block) error {
	blockHash := block.GetHash(s.chain.BlockHeaderHasher)
	s.ServerOptions.Logger.Log("msg", "Received new block", "hash", blockHash)

	// add the block to the blockchain, this includes validation
	// of the block before addition
	err := s.chain.AddBlock(block)
	if err != nil {
		// we return here so we don't broadcast to our peers
		// this is because if we have already added the block
		// which is possibly why we are in this error condition, then we
		// have already broadcasted the block to our peers previously.
		return err
	}

	// broadcast the block to the network (all peers)
	go s.broadcastBlock(from, block)

	s.ServerOptions.Logger.Log("msg", "Added block to blockchain", "hash", blockHash, "blockchainHeight", s.chain.GetHeight())

	return nil
}

// handling transactions coming into the blockchain
// two ways:
// 1. through a wallet (client), HTTP API will receive the transaction, and send it to the server, server will add it to the mempool.
// 2. through another node, the node will send the transaction to the server, server will add it to the mempool.
func (s *Server) processTransaction(from net.Addr, tx *core.Transaction) error {
	transaction_hash := tx.GetHash(s.memPool.Pending.TransactionHasher)

	s.ServerOptions.Logger.Log("msg", "Received new transaction", "hash", transaction_hash)

	// check if transaction exists
	if s.memPool.PendingHas(tx.GetHash(s.memPool.Pending.TransactionHasher)) {
		logrus.WithField("hash", transaction_hash).Warn("Transaction already exists in the mempool")
	}

	// verify the transaction signature
	verified, err := tx.VerifySignature()
	if err != nil {
		return err
	}
	if !verified {
		return fmt.Errorf("transaction signature is invalid")
	}

	// set first seen time if not set yet
	// that means we are the first to see this transaction
	// i.e. it has come from an external source, and not
	// been broadcasted to us from another node.
	// if another node running this same software has already seen this transaction
	// then it will have set the first seen time. giving us total ordering of transactions.
	// WE DO NOT USE LOGICAL TIMESTAMPS OR SYNCHRONIZATION MECHANISMS HERE BECAUSE TRANSACTIONS
	// ARE EXTERNAL TO THE NETWORK, AND CAN BE SENT FROM ANYWHERE.
	if tx.GetFirstSeen() == 0 {
		tx.SetFirstSeen(time.Now().UnixNano())
	}

	// broadcast the transaction to the network (all peers)
	go s.broadcastTx(from, tx)

	s.ServerOptions.Logger.Log("msg", "Adding transaction to mempool", "hash", transaction_hash, "memPoolSize", s.memPool.PendingLen())

	// add transaction to mempool
	return s.memPool.Add(tx)
}

func (s *Server) processGetStatus(from net.Addr, getStatus *core.GetStatusMessage) error {
	s.ServerOptions.Logger.Log("msg", "Received new getstatus message", getStatus)

	statusMessage := &core.StatusMessage{
		CurrentHeight: s.chain.GetHeight(),
		ID:            s.ServerOptions.ID,
	}

	buf := new(bytes.Buffer)

	if err := gob.NewEncoder(buf).Encode(statusMessage); err != nil {
		return err
	}

	msg := NewMessage(Status, buf.Bytes())

	// Get TCP Peer who sent the message, send status to that peer
	for _, peer := range s.Peers {
		if peer.conn.RemoteAddr() == from {
			peer.Send(msg.Bytes())
			return nil
		}
	}

	return fmt.Errorf("couldn't find who sent getStatus")
}

func (s *Server) processStatus(from net.Addr, status *core.StatusMessage) error {
	s.ServerOptions.Logger.Log("msg", "Received new status message", status)
	return nil
}

// Stop will stop the server.
func (s *Server) Stop() {
	s.quitChannel <- true
}

// handleQuit will handle the quit signal sent by Stop()
func (s *Server) handleQuit() {
	// shutdown the storage
	s.chain.Storage.Shutdown()
}

// createNewBlock will create a new block.
func (s *Server) createNewBlock() error {
	// get the previous block hash
	prevBlockHash, err := s.chain.GetBlockHash(s.chain.GetHeight())
	if err != nil {
		return err
	}

	// create a new block, with all mempool transactions
	// normally we might just include a specific number of transactions in each block
	// but here we are including all transactions in the mempool.
	// once we know how our transactions will be structured
	// we can include a specific number of transactions in each block.
	transactions := s.memPool.GetPendingTransactions()
	// flush mempool since we got all the transactions
	// IMPORTANT: we flush right away because while we execute
	// the block creation logic, new transactions might come in, since
	// createBlock() runs in the validatorLoop() goroutine, it is running
	// parallel to the main server loop Start().
	s.memPool.FlushPending()
	// create the block
	block := core.NewBlockWithTransactions(transactions)
	// link block to previous block
	block.Header.PrevBlockHash = prevBlockHash
	// update the block index
	block.Header.Index = s.chain.GetHeight() + 1
	// TODO (Azlan): update other block parameters so that this block is not marked as invalid.

	// sign the block as a validator of this block
	err = block.Sign(s.ServerOptions.PrivateKey)
	if err != nil {
		return err
	}

	// add the block to the blockchain
	err = s.chain.AddBlock(block)
	if err != nil {
		// something ent wrong, we need to re-add the transactions to the mempool
		// so that they can be included in the next block.
		for _, tx := range transactions {
			s.memPool.Add(tx)
		}

		return err
	}

	// as the leader node chosen by consensus for block addition
	// we were able to create a new block successfully,
	// broadcast it to all our peers, to ensure eventual consistency
	go s.broadcastBlock(nil, block)

	return nil
}

// genesisBlock creates the first block in the blockchain.
func genesisBlock() *core.Block {
	b := core.NewBlock()
	b.Header.Timestamp = 0000000000 // setting to zero for all genesis blocks created across all nodes
	b.Header.Index = 0
	b.Header.Version = 1
	return b
}
