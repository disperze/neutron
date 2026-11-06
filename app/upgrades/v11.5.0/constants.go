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
	// (counterparty: Noble channel-18).
	NobleChannel = "channel-30"

	// NobleMultisig receives Noble's USDC escrow for Neutron.
	//
	// TODO: set the Noble multisig before tagging the release. While it is
	// empty the return fails validation and is skipped (see ReturnNobleEscrow),
	// so the upgrade only installs the IBC blocks.
	NobleMultisig = ""

	// ReturnTimeoutSeconds is the timeout of the return packet: the maximum the
	// module allows (7 days). A timed-out return can only be resent by another
	// upgrade, since Neutron governance cannot execute MsgReturnToNoble.
	ReturnTimeoutSeconds = usdcmigrationtypes.MaxTimeoutSeconds

	// rateLimitAnyChannel is the rate-limit contract's wildcard channel.
	rateLimitAnyChannel = "any"
)

var Upgrade = upgrades.Upgrade{
	UpgradeName:          UpgradeName,
	CreateUpgradeHandler: CreateUpgradeHandler,
	StoreUpgrades: storetypes.StoreUpgrades{
		Added:   []string{usdcmigrationtypes.StoreKey},
		Deleted: []string{},
	},
}
