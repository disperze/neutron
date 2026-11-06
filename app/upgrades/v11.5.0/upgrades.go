package v11_5_0

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"

	usdcmigrationkeeper "github.com/disperze/usdc-migration/v10/x/usdcmigration/keeper"
	usdcmigrationtypes "github.com/disperze/usdc-migration/v10/x/usdcmigration/types"

	"github.com/solva-solutions/neutron/v11/app/upgrades"
)

// CreateUpgradeHandler adds x/usdcmigration and returns Noble's USDC escrow.
//
// Neutron governance can only execute software upgrades, wasm store code and
// text proposals (app/govfilter), so this handler does what the return
// proposal does on other chains: it sets the module params, removes the
// voucher's IBC rate-limit path and sends MsgReturnToNoble. It does not freeze
// the voucher with SetSendEnabled: holders need bank sends to redeem it in the
// redemption contract, and lifting a freeze would need another upgrade.
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

		// A failed return must not halt the chain at the upgrade height: nothing
		// of it is written, and it can be sent again by another upgrade.
		if err := ReturnNobleEscrow(ctx, keepers, NobleChannel, NobleMultisig); err != nil {
			ctx.Logger().Error("usdcmigration: Noble escrow return skipped", "error", err.Error())
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

// ReturnNobleEscrow removes the voucher's rate-limit paths and returns the
// supply of the channel's voucher to receiver on Noble, atomically: on any
// error nothing is written and the error is returned. The params must already
// be set.
func ReturnNobleEscrow(ctx sdk.Context, keepers *upgrades.UpgradeKeepers, channelID, receiver string) error {
	cacheCtx, write := ctx.CacheContext()
	res, err := returnNobleEscrow(cacheCtx, keepers, channelID, receiver)
	if err != nil {
		return err
	}
	// write also re-emits the events (send_packet, usdcmigration_return_to_noble)
	// that relayers and the redemption flow need.
	write()
	ctx.Logger().Info("usdcmigration: Noble escrow return sent",
		"channel", channelID, "receiver", receiver,
		"amount", res.Amount.String(), "denom", res.Denom, "sequence", res.Sequence)
	return nil
}

func returnNobleEscrow(
	ctx sdk.Context,
	keepers *upgrades.UpgradeKeepers,
	channelID, receiver string,
) (*usdcmigrationtypes.MsgReturnToNobleResponse, error) {
	gov := authtypes.NewModuleAddress(govtypes.ModuleName).String()
	params := keepers.UsdcMigrationKeeper.GetParams(ctx)
	voucher := usdcmigrationtypes.VoucherDenom(channelID, params.BaseDenom)

	// 1. The rate-limit contract's send quotas on the voucher (35% of the supply
	// a day) would reject a return of the whole supply.
	contract := keepers.IbcRateLimitKeeper.GetParams(ctx).ContractAddress
	if contract != "" {
		if err := removeRateLimitPaths(ctx, keepers.WasmKeeper, contract, gov, channelID, voucher); err != nil {
			return nil, err
		}
	}

	// 2. Return the voucher supply. It is below Noble's escrow for the channel,
	// so Noble accepts it whichever pending packets land first.
	supply := keepers.BankKeeper.GetSupply(ctx, voucher)
	return usdcmigrationkeeper.NewMsgServerImpl(keepers.UsdcMigrationKeeper).ReturnToNoble(ctx, &usdcmigrationtypes.MsgReturnToNoble{
		Authority:      gov,
		ChannelId:      channelID,
		Receiver:       receiver,
		Amount:         supply.Amount,
		TimeoutSeconds: ReturnTimeoutSeconds,
		Memo:           "", // a callbacks key here would trigger callbacks middleware below
	})
}

type rateLimitQuery struct {
	GetQuotas *rateLimitPath `json:"get_quotas,omitempty"`
}

type rateLimitExecute struct {
	RemovePath *rateLimitPath `json:"remove_path,omitempty"`
}

type rateLimitPath struct {
	ChannelID string `json:"channel_id"`
	Denom     string `json:"denom"`
}

// removeRateLimitPaths removes the voucher's paths on channel "any" and on the
// Noble channel channelID, if the contract has them, with the gov module as sender (it
// holds every role on Neutron's rate-limit contract since v11.0.0).
func removeRateLimitPaths(ctx sdk.Context, wk *wasmkeeper.Keeper, contract, sender, channelID, denom string) error {
	contractAddr, err := sdk.AccAddressFromBech32(contract)
	if err != nil {
		return fmt.Errorf("invalid rate-limit contract address %s: %w", contract, err)
	}
	wasmSrv := wasmkeeper.NewMsgServerImpl(wk)

	for _, channel := range []string{rateLimitAnyChannel, channelID} {
		path := &rateLimitPath{ChannelID: channel, Denom: denom}

		query, err := json.Marshal(rateLimitQuery{GetQuotas: path})
		if err != nil {
			return err
		}
		bz, err := wk.QuerySmart(ctx, contractAddr, query)
		if err != nil {
			// The contract loads the path from storage, so a missing path is a
			// cosmwasm-std NotFound error ("<type>; key: [..] not found"), not
			// an empty list.
			if strings.Contains(err.Error(), "not found") {
				continue
			}
			return fmt.Errorf("failed to query rate-limit quotas on %s: %w", channel, err)
		}
		var quotas []json.RawMessage
		if err := json.Unmarshal(bz, &quotas); err != nil {
			return fmt.Errorf("failed to parse rate-limit quotas on %s: %w", channel, err)
		}
		if len(quotas) == 0 {
			continue
		}

		msg, err := json.Marshal(rateLimitExecute{RemovePath: path})
		if err != nil {
			return err
		}
		if _, err := wasmSrv.ExecuteContract(ctx, &wasmtypes.MsgExecuteContract{
			Sender:   sender,
			Contract: contract,
			Msg:      msg,
		}); err != nil {
			return fmt.Errorf("failed to remove rate-limit path %s/%s: %w", channel, denom, err)
		}
		ctx.Logger().Info("usdcmigration: rate-limit path removed", "channel", channel, "denom", denom)
	}
	return nil
}
