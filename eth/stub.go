package eth

import (
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/livepeer/go-livepeer/pm"
)

type Client struct {
	checkTxError bool
	nonce        uint64
}

func (c *Client) CheckTx(tx *types.Transaction) error {
	if c.checkTxError {
		return fmt.Errorf("tx reverted")
	}
	time.Sleep(3 * time.Second)
	return nil
}

func (c *Client) RedeemWinningTicket(ticket *pm.Ticket, sig []byte, recipientRand *big.Int) (*types.Transaction, error) {
	if ticket.FaceValue.Cmp(big.NewInt(500)) < 0 {
		return nil, fmt.Errorf("error submitting tx")
	}
	if ticket.FaceValue.Cmp(big.NewInt(1000)) < 0 {
		c.checkTxError = true
	}

	return types.NewTransaction(c.nonce, ticket.Recipient, nil, 0, nil, nil), nil
}
