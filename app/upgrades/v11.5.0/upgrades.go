package v11_5_0

import (
	"context"
	"fmt"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	usdcmigrationkeeper "github.com/disperze/usdc-migration/v10/x/usdcmigration/keeper"
	usdcmigrationtypes "github.com/disperze/usdc-migration/v10/x/usdcmigration/types"

	"github.com/solva-solutions/neutron/v11/app/upgrades"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	keepers *upgrades.UpgradeKeepers,
	_ upgrades.StoreKeys,
	_ codec.Codec,
) upgradetypes.UpgradeHandler {
	return func(c context.Context, _ upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		ctx := sdk.UnwrapSDKContext(c)

		ctx.Logger().Info("Starting module migrations...")

		// RunMigrations calls InitGenesis of x/usdcmigration with its default,
		// inert params, so the Noble channel must be set after it.
		vm, err := mm.RunMigrations(ctx, configurator, vm)
		if err != nil {
			return vm, err
		}

		if err := SetUsdcMigrationParams(ctx, keepers.UsdcMigrationKeeper); err != nil {
			return vm, err
		}

		ctx.Logger().Info(fmt.Sprintf("Migration {%s} applied", UpgradeName))
		return vm, nil
	}
}

// SetUsdcMigrationParams turns on the Noble USDC receive and send blocks for
// Neutron's Noble channel.
func SetUsdcMigrationParams(ctx sdk.Context, k *usdcmigrationkeeper.Keeper) error {
	params := usdcmigrationtypes.DefaultParams()
	params.NobleChannels = []string{NobleChannel}
	// SetParams validates the params.
	if err := k.SetParams(ctx, params); err != nil {
		return fmt.Errorf("failed to set usdcmigration params: %w", err)
	}
	ctx.Logger().Info("usdcmigration params set", "noble_channels", params.NobleChannels)
	return nil
}
