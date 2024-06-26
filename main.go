package main

import (
	"fmt"
	"net"

	crypto "github.com/AzlanAmjad/DreamscapeCanvas-Blockchain/cryptography"
	network "github.com/AzlanAmjad/DreamscapeCanvas-Blockchain/peer-to-peer-network"
	"github.com/sirupsen/logrus"
)

func main() {
	// entry point

	// ask user for node ID
	var id string
	logrus.Info("Enter the node ID: ")
	_, err := fmt.Scanln(&id)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to read node ID")
	}

	// ask user what port to listen on
	var port string
	logrus.Info("Enter the port to listen on: ")
	_, err = fmt.Scanln(&port)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to read port")
	}
	// create net.Addr for a TCP connection
	addr, err := net.ResolveTCPAddr("tcp", "localhost:"+port)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to resolve TCP address")
	}

	// ask user what port to listen on for the API
	var APIport string
	logrus.Info("Enter the port to listen on for the API: ")
	_, err = fmt.Scanln(&APIport)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to read port")
	}
	// create net.Addr for a TCP connection
	APIaddr, err := net.ResolveTCPAddr("tcp", "localhost:"+APIport)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to resolve TCP address")
	}

	// ask user if this node is a validator
	var isValidator string
	logrus.Info("Is this node a validator? (y/n): ")
	_, err = fmt.Scanln(&isValidator)
	if err != nil {
		logrus.WithError(err).Fatal("Failed to read input")
	}

	var localNode *network.Server
	var privateKey crypto.PrivateKey
	if isValidator == "y" {
		// generate private key
		privateKey = crypto.GeneratePrivateKey()
		// create local node
		localNode = makeServer(id, &privateKey, addr, APIaddr)
	} else {
		// create local node
		localNode = makeServer(id, nil, addr, APIaddr)
	}

	// start local node
	go localNode.Start()

	select {}
}

func makeServer(id string, privateKey *crypto.PrivateKey, addr net.Addr, APIaddr net.Addr) *network.Server {
	// one seed node, hardcoded at port 8000
	seedAddr, err := net.ResolveTCPAddr("tcp", "localhost:8000")
	if err != nil {
		logrus.WithError(err).Fatal("Failed to resolve TCP address")
	}

	serverOptions := network.ServerOptions{
		Addr:      addr,
		APIAddr:   APIaddr,
		ID:        id,
		SeedNodes: []net.Addr{seedAddr},
	}
	if privateKey != nil {
		serverOptions.PrivateKey = privateKey
	}

	// We will create a new server with the server options.
	server, err := network.NewServer(serverOptions)
	if err != nil {
		panic(err)
	}
	return server
}
