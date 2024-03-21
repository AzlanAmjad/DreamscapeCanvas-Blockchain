package network

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"io"
	"net"

	core "github.com/AzlanAmjad/DreamscapeCanvas-Blockchain/blockchain-core"
)

// SendRPC is a struct containing the address to send to and the payload to send.
type SendRPC struct {
	To      net.Addr
	Payload io.Reader
}

// ReceiveRPC is a struct containing the address received from and the payload received.
type ReceiveRPC struct {
	From    net.Addr
	Payload io.Reader
}

// RPC message types
type MessageType byte

const (
	Transaction MessageType = iota
	Block
	Status
	GetStatus
)

// Message is a struct containing the message type and the payload.
type Message struct {
	Header  MessageType
	Payload []byte
}

func NewMessage(header MessageType, payload []byte) Message {
	return Message{Header: header, Payload: payload}
}

func (m *Message) Bytes() []byte {
	buf := bytes.Buffer{}
	enc := gob.NewEncoder(&buf)
	err := enc.Encode(m)
	if err != nil {
		panic(err)
	}
	return buf.Bytes()
}

type DecodedMessage struct {
	Header  MessageType
	From    net.Addr
	Message any
}

type RPCDecodeFunc func(ReceiveRPC) (*DecodedMessage, error)

func DefaultRPCDecoder(rpc ReceiveRPC) (*DecodedMessage, error) {
	msg := Message{}

	// expect the RPC payload to be a gob encoded message
	err := gob.NewDecoder(rpc.Payload).Decode(&msg)
	if err != nil {
		return nil, fmt.Errorf("error decoding message from %s: %s", rpc.From, err)
	}

	switch msg.Header {
	case Transaction:
		tx := core.Transaction{}
		err = tx.Decode(bytes.NewReader(msg.Payload), core.NewTransactionDecoder())
		if err != nil {
			return nil, err
		}
		return &DecodedMessage{Header: msg.Header, From: rpc.From, Message: tx}, nil
	case Block:
		block := core.NewBlock()
		err = block.Decode(bytes.NewReader(msg.Payload), core.NewBlockDecoder())
		if err != nil {
			return nil, err
		}
		return &DecodedMessage{Header: msg.Header, From: rpc.From, Message: *block}, nil
	case GetStatus:
		return &DecodedMessage{Header: msg.Header, From: rpc.From, Message: nil}, nil
	case Status:
		statusMessage := new(core.GetStatusMessage)
		err = gob.NewDecoder(bytes.NewReader(msg.Payload)).Decode(statusMessage)
		if err != nil {
			return nil, err
		}
		return &DecodedMessage{Header: msg.Header, From: rpc.From, Message: statusMessage}, nil

	default:
		return nil, fmt.Errorf("unknown message type: %d", msg.Header)
	}
}

// RPCProcessor is an interface for processing RPCs.
type RPCProcessor interface {
	ProcessMessage(net.Addr, *DecodedMessage) error
}
