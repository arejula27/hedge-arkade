package arkadeadapter

import (
	"fmt"

	"github.com/arejula27/hedge/arkade"
	"github.com/arejula27/hedge/service/internal/domain"
	"github.com/btcsuite/btcd/wire"
)

func outpointOf(c *domain.Contract) (wire.OutPoint, error) {
	if c.Funding == nil {
		return wire.OutPoint{}, fmt.Errorf("contract %s has no funding outpoint", c.ID)
	}
	hash, err := arkade.ChainHash(c.Funding.Txid)
	if err != nil {
		return wire.OutPoint{}, fmt.Errorf("the funding txid %q: %w", c.Funding.Txid, err)
	}
	return wire.OutPoint{Hash: *hash, Index: c.Funding.Vout}, nil
}
