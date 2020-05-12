package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/golang/glog"
	"github.com/kyriediculous/redeemer/proto"
	"github.com/livepeer/go-livepeer/pm"
	"google.golang.org/grpc"
)

const GRPCConnectTimeout = 3 * time.Second

func (c *redeemerClient) startTicketRedeemerClient() (*grpc.ClientConn, error) {
	glog.Infof("Connecting RPC to TicketRedeemerServicer at %v", c.redeemer)
	conn, err := grpc.Dial(":50051",
		grpc.WithBlock(),
		grpc.WithTimeout(GRPCConnectTimeout),
		grpc.WithInsecure(),
	)

	//TODO: PROVIDE KEEPALIVE SETTINGS
	if err != nil {
		glog.Errorf("Did not connect to orch=%v err=%v", c.redeemer, err)
		return nil, fmt.Errorf("Did not connect to orch=%v err=%v", c.redeemer, err)
	}
	rpc := proto.NewTicketRedeemerClient(conn)
	c.rpc = rpc

	return conn, nil
}

func main() {
	port := flag.Int("httpport", 8080, "Listener port for the HTTP API connection")
	redeemer := flag.String("redeemer", "localhost:50051", "redeemer URI")
	flag.Parse()

	uri, err := url.Parse(*redeemer)
	if err != nil {
		glog.Error(err)
		return
	}
	rc := NewRedeemerClient(uri)
	conn, err := rc.startTicketRedeemerClient()
	if err != nil {
		glog.Error(err)
		return
	}
	defer conn.Close()

	go func() {
		rc.listenAndServe(*port)
	}()

	go func() {
		rc.RedemptionMonitor()
	}()

	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt)
	<-c
}

type redeemerClient struct {
	redeemer *url.URL
	rpc      proto.TicketRedeemerClient
}

func NewRedeemerClient(redeemer *url.URL) *redeemerClient {
	return &redeemerClient{
		redeemer: redeemer,
	}
}

func (c *redeemerClient) listenAndServe(port int) {
	http.HandleFunc("/", c.ticketHandler)
	http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
}

func (c *redeemerClient) RedemptionMonitor() error {
	stream, err := c.rpc.Redemptions(context.Background(), &proto.RedemptionsRequest{})
	if err != nil {
		return err
	}

	for {
		redemption, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil && strings.Contains(err.Error(), "transport is closing") {
			break
		}
		switch res := redemption.Result.(type) {
		case *proto.Redemption_Error:
			fmt.Printf("New Redemption - err=%v\n", res.Error)
		case *proto.Redemption_Data:
			sender := ethcommon.BytesToAddress(res.Data.Sender)
			val := new(big.Int).SetBytes(res.Data.Value)
			var stage string
			if res.Data.Stage == proto.Redemption_PENDING {
				stage = "pending"
			} else {
				stage = "mined"
			}
			tx := ethcommon.BytesToHash(res.Data.Tx).Hex()
			fmt.Printf("New Redemption - sender=0x%x value=%v stage=%v tx=%v\n", sender, val, stage, tx)
		}
	}

	return nil
}

func (c *redeemerClient) RedeemWinningTicket(ticket *pm.Ticket, sig []byte, recipientRand *big.Int) error {
	_, err := c.rpc.RedeemWinningTicket(context.Background(), &proto.Ticket{
		Recipient:              ticket.Recipient.Bytes(),
		Sender:                 ticket.Sender.Bytes(),
		FaceValue:              ticket.FaceValue.Bytes(),
		WinProb:                ticket.WinProb.Bytes(),
		SenderNonce:            ticket.SenderNonce,
		RecipientRandHash:      ticket.RecipientRandHash.Bytes(),
		CreationRound:          ticket.CreationRound,
		CreationRoundBlockHash: ticket.CreationRoundBlockHash.Bytes(),
		Sig:                    sig,
		RecipientRand:          recipientRand.Bytes(),
	})
	return err
}

func (c *redeemerClient) ticketHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		msg := fmt.Sprintf(
			"Ticket Redeemer Client connected to %v",
			c.redeemer)
		fmt.Fprint(w, msg)
	case "POST":
		var data struct {
			Ticket        *pm.Ticket
			Sig           []byte
			RecipientRand *big.Int
		}
		defer r.Body.Close()
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			glog.Error("err reading body", err)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(err.Error()))
			return
		}

		if err := json.Unmarshal(body, &data); err != nil {
			glog.Error("err unmarshaling json", err)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(err.Error()))
			return
		}

		if err := c.RedeemWinningTicket(data.Ticket, data.Sig, data.RecipientRand); err != nil {
			glog.Error("error redeeming ticket", err)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(err.Error()))
		}

		w.Write([]byte{})
	default:
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
	}
}
