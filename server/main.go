package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"time"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/golang/glog"
	"github.com/golang/protobuf/ptypes/empty"
	"github.com/kyriediculous/redeemer/eth"
	"github.com/kyriediculous/redeemer/proto"
	"github.com/livepeer/go-livepeer/pm"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Write an ETH Client stub that returns errors depending on facevalue or winprob values
// eg facevalue > 500 submission ok
// 500 < facevalue < 1000 submission ok , mining error
// favevalue > 1000 mining ok

type EthClient interface {
	CheckTx(tx *types.Transaction) error
	RedeemWinningTicket(ticket *pm.Ticket, sig []byte, recipientRand *big.Int) (*types.Transaction, error)
}

const GRPCConnectTimeout = 3 * time.Second
const EthTxTimeout = 600 * time.Second

func main() {
	recipient := flag.String("recipient", "0x13d3c3bddd3531d11f92884bc49cb98255d51370", "Ticket recipient Eth address")
	/*
		ethAcctAddr := flag.String("ethAcctAddr", ".13d3c3bddd3531d11f92884bc49cb98255d51370", "Existing Eth account address")
		ethPassword := flag.String("ethPassword", "", "Password for existing Eth account address")
		ethKeystorePath := flag.String("ethKeystorePath", "./", "Path for the Eth Key")
		ethURL := flag.String("ethURL", "https://rinkeby.infura.io/v3/534bea718e1e4b85881761c876845cf8", "Ethereum node JSON-RPC URL")
		ethController := flag.String("ethController", "0xA268AEa9D048F8d3A592dD7f1821297972D4C8Ea", "Protocol smart contract address")
		gasLimit := flag.Int("gasLimit", 0, "Gas limit for ETH transactions")
		gasPrice := flag.Int("gasPrice", 0, "Gas price for ETH transactions")
	*/
	flag.Parse()

	if *recipient == "" {
		glog.Errorf("No recipient provided")
		return
	}
	recipientAddr := ethcommon.HexToAddress(*recipient)

	/*
		var keystoreDir string
		if _, err := os.Stat(*ethKeystorePath); !os.IsNotExist(err) {
			keystoreDir, _ = filepath.Split(*ethKeystorePath)
		}

		if keystoreDir == "" {
			glog.Errorf("Cannot find keystore directory")
			return
		}

		//Get the Eth client connection information
		if *ethURL == "" {
			glog.Fatal("Need to specify an Ethereum node JSON-RPC URL using -ethUrl")
		}

		//Set up eth client
		backend, err := ethclient.Dial(*ethURL)
		if err != nil {
			glog.Errorf("Failed to connect to Ethereum client: %v", err)
			return
		}

		client, err := eth.NewClient(ethcommon.HexToAddress(*ethAcctAddr), keystoreDir, backend, ethcommon.HexToAddress(*ethController), EthTxTimeout)
		if err != nil {
			glog.Errorf("Failed to create client: %v", err)
			return
		}

		var bigGasPrice *big.Int
		if *gasPrice > 0 {
			bigGasPrice = big.NewInt(int64(*gasPrice))
		}

		err = client.Setup(*ethPassword, uint64(*gasLimit), bigGasPrice)
		if err != nil {
			glog.Errorf("Failed to setup client: %v", err)
			return
		}
	*/

	client := &eth.Client{}

	fmt.Println("Starting server on port :50051...")

	// 50051 is the default port for gRPC
	// Ideally we'd use 0.0.0.0 instead of localhost as well
	listener, err := net.Listen("tcp", ":50051")
	defer listener.Close()

	if err != nil {
		log.Fatalf("Unable to listen on port :50051: %v", err)
	}

	// slice of gRPC options
	// Here we can configure things like TLS
	opts := []grpc.ServerOption{}
	// var s *grpc.Server
	s := grpc.NewServer(opts...)
	defer s.Stop()
	srv := newTicketRedeemerServer(recipientAddr, client)
	proto.RegisterTicketRedeemerServer(s, srv)

	// Start the server in a child routine
	go func() {
		if err := s.Serve(listener); err != nil {
			log.Fatalf("Failed to serve: %v", err)
		}
	}()
	fmt.Println("Server succesfully started on port :50051")

	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt)
	<-c

	fmt.Println("\nStopping the server...")
}

func newTicketRedeemerServer(recipient ethcommon.Address, eth EthClient) *ticketRedeemerServer {
	return &ticketRedeemerServer{
		quit:      make(chan struct{}),
		recipient: recipient,
		eth:       eth,
	}
}

type ticketRedeemerServer struct {
	clients   sync.Map
	quit      chan struct{}
	recipient ethcommon.Address
	eth       EthClient
}

func (s *ticketRedeemerServer) RedeemWinningTicket(ctx context.Context, ticket *proto.Ticket) (*empty.Empty, error) {
	// Check valid recipient
	if err := s.validateTicket(ticket); err != nil {
		glog.Error(err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Submit transaction on-chain
	tx, err := s.eth.RedeemWinningTicket(pmTicket(ticket), ticket.Sig, new(big.Int).SetBytes(ticket.RecipientRand))
	if err != nil {
		s.sendRedemption(
			&proto.Redemption{
				Result: &proto.Redemption_Error{
					Error: err.Error(),
				},
			},
		)
	}

	// Send Redemption received to all clients
	s.sendRedemption(
		&proto.Redemption{
			Result: &proto.Redemption_Data{
				Data: &proto.Redemption_Result{
					Stage:  proto.Redemption_PENDING,
					Sender: ticket.GetSender(),
					Value:  ticket.GetFaceValue(),
				},
			},
		},
	)

	// Wait for Transaction to be mined
	if err := s.eth.CheckTx(tx); err != nil {
		s.sendRedemption(
			&proto.Redemption{
				Result: &proto.Redemption_Error{
					Error: err.Error(),
				},
			},
		)
	}

	// Send Redemption success to all clients
	s.sendRedemption(
		&proto.Redemption{
			Result: &proto.Redemption_Data{
				Data: &proto.Redemption_Result{
					Stage:  proto.Redemption_MINED,
					Tx:     tx.Hash().Bytes(),
					Sender: ticket.GetSender(),
					Value:  ticket.GetFaceValue(),
				},
			},
		},
	)

	return &empty.Empty{}, nil
}

func (s *ticketRedeemerServer) Redemptions(req *proto.RedemptionsRequest, stream proto.TicketRedeemer_RedemptionsServer) error {
	// The client address will serve as the ID for the stream
	peer, ok := peer.FromContext(stream.Context())
	if !ok {
		return fmt.Errorf("context doesn't exist")
	}

	// Make a channel to receive ticket redemption messages
	// to send to the client over the stream
	redemptions := make(chan *proto.Redemption)
	s.clients.Store(peer.Addr.String(), redemptions)

	glog.Infof("new redemption subscription peer=%v", peer.Addr.String())

	// Block so that the stream is over a long-lived connection
	for {
		select {
		case redemption := <-redemptions:
			if err := stream.Send(redemption); err != nil {
				if err == io.EOF {
					s.clients.Delete(peer)
				}
				glog.Errorf("Unable to send Redemption message to %v err=%v", peer.Addr.String(), err)
			}
		case <-s.quit:
			return nil
		}
	}
}

func (s *ticketRedeemerServer) sendRedemption(redemption *proto.Redemption) {
	s.clients.Range(func(key, value interface{}) bool {
		value.(chan *proto.Redemption) <- redemption
		return true
	})
}

func (s *ticketRedeemerServer) validateTicket(ticket *proto.Ticket) error {
	recipient := ethcommon.BytesToAddress(ticket.GetRecipient())
	if recipient != s.recipient {
		return fmt.Errorf("invalid recipient=%v", recipient)
	}
	return nil
}

func startTicketRedeemerClient(uri *url.URL) (proto.TicketRedeemerClient, *grpc.ClientConn, error) {
	glog.Infof("Connecting RPC to TicketRedeemerServicer at %v", uri)
	conn, err := grpc.Dial(uri.Host,
		grpc.WithBlock(),
		grpc.WithTimeout(GRPCConnectTimeout))

	//TODO: PROVIDE KEEPALIVE SETTINGS
	if err != nil {
		glog.Errorf("Did not connect to orch=%v err=%v", uri, err)
		return nil, nil, fmt.Errorf("Did not connect to orch=%v err=%v", uri, err)
	}
	c := proto.NewTicketRedeemerClient(conn)

	return c, conn, nil
}

func pmTicket(ticket *proto.Ticket) *pm.Ticket {
	return &pm.Ticket{
		Recipient:              ethcommon.BytesToAddress(ticket.Recipient),
		Sender:                 ethcommon.BytesToAddress(ticket.Sender),
		FaceValue:              new(big.Int).SetBytes(ticket.FaceValue),
		WinProb:                new(big.Int).SetBytes(ticket.WinProb),
		SenderNonce:            ticket.SenderNonce,
		RecipientRandHash:      ethcommon.BytesToHash(ticket.RecipientRandHash),
		CreationRound:          ticket.CreationRound,
		CreationRoundBlockHash: ethcommon.BytesToHash(ticket.CreationRoundBlockHash),
	}
}
