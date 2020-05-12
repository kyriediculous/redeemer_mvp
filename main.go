package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/big"
	"net/http"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/golang/glog"
	"github.com/livepeer/go-livepeer/pm"
)

type data struct {
	Ticket        *pm.Ticket
	Sig           []byte
	RecipientRand *big.Int
}

func main() {
	data := data{
		Ticket: &pm.Ticket{
			Recipient:              ethcommon.HexToAddress("0x13d3c3bddd3531d11f92884bc49cb98255d51370"),
			Sender:                 pm.RandAddress(),
			FaceValue:              big.NewInt(2000),
			WinProb:                big.NewInt(2345),
			SenderNonce:            uint32(123),
			RecipientRandHash:      pm.RandHash(),
			CreationRound:          int64(500),
			CreationRoundBlockHash: pm.RandHash(),
		},
		Sig:           pm.RandBytes(42),
		RecipientRand: big.NewInt(4567),
	}

	val, _ := json.Marshal(data)

	fmt.Print(string(val))

	res, err := http.Post("http://localhost:8080", "application/json", bytes.NewBuffer(val))
	if err != nil {
		log.Fatal(err)
	}

	defer res.Body.Close()
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		log.Fatal(err)
	}

	glog.Info(string(body))
	return
}
