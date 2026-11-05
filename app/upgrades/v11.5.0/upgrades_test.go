package v11_5_0_test

import (
	"testing"

	"cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	transfertypes "github.com/cosmos/ibc-go/v10/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types" //nolint:staticcheck
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/disperze/usdc-migration/v10/x/usdcmigration"
	usdcmigrationtypes "github.com/disperze/usdc-migration/v10/x/usdcmigration/types"

	"github.com/solva-solutions/neutron/v11/app/params"
	v11_5_0 "github.com/solva-solutions/neutron/v11/app/upgrades/v11.5.0"
	"github.com/solva-solutions/neutron/v11/testutil"
)

// nobleUSDCVoucher is the Noble USDC voucher on neutron-1, from the chain registry.
const nobleUSDCVoucher = "ibc/B559A80D62249C8AA07A380E2A2BEA6E5CA9A6F079C912C3A9E9B494105E4F81"

type UpgradeTestSuite struct {
	testutil.IBCConnectionTestSuite
}

func TestKeeperTestSuite(t *testing.T) {
	suite.Run(t, new(UpgradeTestSuite))
}

func (suite *UpgradeTestSuite) SetupTest() {
	suite.IBCConnectionTestSuite.SetupTest()
}

func (suite *UpgradeTestSuite) TestUpgrade() {
	app := suite.GetNeutronZoneApp(suite.ChainA)
	ctx := suite.ChainA.GetContext().WithChainID("neutron-1")
	t := suite.T()

	// Before the upgrade the module is inert: no Noble channel is configured.
	require.Empty(t, app.UsdcMigrationKeeper.GetParams(ctx).NobleChannels)

	upgrade := upgradetypes.Plan{
		Name:   v11_5_0.UpgradeName,
		Info:   "some text here",
		Height: 100,
	}
	require.NoError(t, app.UpgradeKeeper.ApplyUpgrade(ctx, upgrade))

	got := app.UsdcMigrationKeeper.GetParams(ctx)
	require.Equal(t, []string{v11_5_0.NobleChannel}, got.NobleChannels)
	require.Equal(t, usdcmigrationtypes.DefaultBaseDenom, got.BaseDenom)
	require.True(t, got.RecvBlockEnabled)
	require.True(t, got.SendBlockEnabled)

	// The configured channel yields Neutron's Noble USDC voucher.
	require.Equal(t, nobleUSDCVoucher, usdcmigrationtypes.VoucherDenom(v11_5_0.NobleChannel, got.BaseDenom))
	require.True(t, got.ShouldBlockSend(usdcmigrationtypes.VoucherPath(v11_5_0.NobleChannel, got.BaseDenom)))
}

func (suite *UpgradeTestSuite) TestTransferStackIsWrapped() {
	app := suite.GetNeutronZoneApp(suite.ChainA)
	_, ok := app.TransferStack.(usdcmigration.IBCMiddleware)
	suite.Require().True(ok, "usdcmigration middleware must be outermost on the transfer route")
}

// TestSendBlock checks that the transfer keeper sends through the usdcmigration
// keeper: the voucher of a Noble channel cannot leave the chain, other denoms can.
func (suite *UpgradeTestSuite) TestSendBlock() {
	suite.ConfigureTransferChannel()
	app := suite.GetNeutronZoneApp(suite.ChainA)
	t := suite.T()
	channelID := suite.TransferPath.EndpointA.ChannelID

	ctx := suite.ChainA.GetContext()
	p := usdcmigrationtypes.DefaultParams()
	p.NobleChannels = []string{channelID}
	require.NoError(t, app.UsdcMigrationKeeper.SetParams(ctx, p))

	voucher := usdcmigrationtypes.VoucherDenom(channelID, usdcmigrationtypes.DefaultBaseDenom)
	trace := transfertypes.ExtractDenomFromPath(usdcmigrationtypes.VoucherPath(channelID, usdcmigrationtypes.DefaultBaseDenom))
	app.TransferKeeper.Keeper.SetDenom(ctx, trace)

	sender := suite.ChainA.SenderAccount.GetAddress()
	amount := math.NewInt(1_000_000)
	suite.FundAcc(sender, sdk.NewCoins(sdk.NewCoin(voucher, amount), sdk.NewCoin(params.DefaultDenom, amount)))
	receiver := suite.ChainB.SenderAccount.GetAddress().String()

	transfer := func(denom string) error {
		cacheCtx, _ := suite.ChainA.GetContext().CacheContext()
		msg := transfertypes.NewMsgTransfer(
			transfertypes.PortID, channelID,
			sdk.NewCoin(denom, amount),
			sender.String(), receiver,
			clienttypes.NewHeight(1, 1000), 0, "",
		)
		_, err := app.TransferKeeper.Keeper.Transfer(cacheCtx, msg)
		return err
	}

	require.ErrorIs(t, transfer(voucher), usdcmigrationtypes.ErrSendBlocked)
	require.NoError(t, transfer(params.DefaultDenom))
}
