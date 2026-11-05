package v11_5_0

import (
	storetypes "cosmossdk.io/store/types"

	usdcmigrationtypes "github.com/disperze/usdc-migration/v10/x/usdcmigration/types"

	"github.com/solva-solutions/neutron/v11/app/upgrades"
)

const (
	// UpgradeName defines the on-chain upgrade name.
	UpgradeName = "v11.5.0"

	// NobleChannel is Neutron's end of the ICS20 transfer channel to Noble
	// (counterparty: Noble channel-18). Its uusdc voucher is
	// ibc/B559A80D62249C8AA07A380E2A2BEA6E5CA9A6F079C912C3A9E9B494105E4F81.
	NobleChannel = "channel-30"
)

var Upgrade = upgrades.Upgrade{
	UpgradeName:          UpgradeName,
	CreateUpgradeHandler: CreateUpgradeHandler,
	StoreUpgrades: storetypes.StoreUpgrades{
		Added:   []string{usdcmigrationtypes.StoreKey},
		Deleted: []string{},
	},
}
