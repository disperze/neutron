package v11_5_0_test

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	transfertypes "github.com/cosmos/ibc-go/v10/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types" //nolint:staticcheck
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/disperze/usdc-migration/v10/x/usdcmigration"
	usdcmigrationkeeper "github.com/disperze/usdc-migration/v10/x/usdcmigration/keeper"
	usdcmigrationtypes "github.com/disperze/usdc-migration/v10/x/usdcmigration/types"

	"github.com/solva-solutions/neutron/v11/app"
	"github.com/solva-solutions/neutron/v11/app/params"
	"github.com/solva-solutions/neutron/v11/app/upgrades"
	v11_5_0 "github.com/solva-solutions/neutron/v11/app/upgrades/v11.5.0"
	"github.com/solva-solutions/neutron/v11/testutil"
	ibcratelimittypes "github.com/solva-solutions/neutron/v11/x/ibc-rate-limit/types"
)

const (
	// nobleUSDCVoucher is the Noble USDC voucher on neutron-1, from the chain registry.
	nobleUSDCVoucher = "ibc/B559A80D62249C8AA07A380E2A2BEA6E5CA9A6F079C912C3A9E9B494105E4F81"

	rateLimiterWasm = "../../../x/ibc-rate-limit/bytecode/rate_limiter.wasm"
)

type UpgradeTestSuite struct {
	testutil.IBCConnectionTestSuite
}

func TestKeeperTestSuite(t *testing.T) {
	suite.Run(t, new(UpgradeTestSuite))
}

func (suite *UpgradeTestSuite) SetupTest() {
	suite.IBCConnectionTestSuite.SetupTest()
}

func upgradeKeepers(a *app.App) *upgrades.UpgradeKeepers {
	return &upgrades.UpgradeKeepers{
		BankKeeper:          a.BankKeeper,
		WasmKeeper:          &a.WasmKeeper,
		IbcRateLimitKeeper:  a.RateLimitingICS4Wrapper.IbcratelimitKeeper,
		UsdcMigrationKeeper: a.UsdcMigrationKeeper,
	}
}

func nobleAddress() string {
	addr, err := sdk.Bech32ifyAddressBytes("noble", []byte("noble-multisig-test-addr"))
	if err != nil {
		panic(err)
	}
	return addr
}

func govAddress() string {
	return authtypes.NewModuleAddress(govtypes.ModuleName).String()
}

func countEvents(ctx sdk.Context, eventType string) int {
	n := 0
	for _, e := range ctx.EventManager().Events() {
		if e.Type == eventType {
			n++
		}
	}
	return n
}

func (suite *UpgradeTestSuite) TestUpgrade() {
	a := suite.GetNeutronZoneApp(suite.ChainA)
	ctx := suite.ChainA.GetContext().WithChainID("neutron-1").WithEventManager(sdk.NewEventManager())
	t := suite.T()

	// Before the upgrade the module is inert: no Noble channel is configured.
	require.Empty(t, a.UsdcMigrationKeeper.GetParams(ctx).NobleChannels)

	upgrade := upgradetypes.Plan{
		Name:   v11_5_0.UpgradeName,
		Info:   "some text here",
		Height: 100,
	}
	require.NoError(t, a.UpgradeKeeper.ApplyUpgrade(ctx, upgrade))

	got := a.UsdcMigrationKeeper.GetParams(ctx)
	require.Equal(t, []string{v11_5_0.NobleChannel}, got.NobleChannels)
	require.Equal(t, usdcmigrationtypes.DefaultBaseDenom, got.BaseDenom)
	require.True(t, got.RecvBlockEnabled)
	require.True(t, got.SendBlockEnabled)

	// The configured channel yields Neutron's Noble USDC voucher.
	require.Equal(t, nobleUSDCVoucher, usdcmigrationtypes.VoucherDenom(v11_5_0.NobleChannel, got.BaseDenom))
	require.True(t, got.ShouldBlockSend(usdcmigrationtypes.VoucherPath(v11_5_0.NobleChannel, got.BaseDenom)))

	// There is no channel-30 here (and no multisig until it is set), so the return
	// fails and is skipped without failing the upgrade.
	require.Zero(t, countEvents(ctx, usdcmigrationtypes.EventTypeReturnToNoble))

	// The voucher is not frozen: holders need bank sends to redeem it.
	require.True(t, a.BankKeeper.IsSendEnabledDenom(ctx, nobleUSDCVoucher))
}

func (suite *UpgradeTestSuite) TestTransferStackIsWrapped() {
	a := suite.GetNeutronZoneApp(suite.ChainA)
	_, ok := a.TransferStack.(usdcmigration.IBCMiddleware)
	suite.Require().True(ok, "usdcmigration middleware must be outermost on the transfer route")
}

// TestSendBlock checks that the transfer keeper sends through the usdcmigration
// keeper: the voucher of a Noble channel cannot leave the chain, other denoms can.
func (suite *UpgradeTestSuite) TestSendBlock() {
	suite.ConfigureTransferChannel()
	a := suite.GetNeutronZoneApp(suite.ChainA)
	t := suite.T()
	channelID := suite.TransferPath.EndpointA.ChannelID

	ctx := suite.ChainA.GetContext()
	p := usdcmigrationtypes.DefaultParams()
	p.NobleChannels = []string{channelID}
	require.NoError(t, a.UsdcMigrationKeeper.SetParams(ctx, p))

	voucher := usdcmigrationtypes.VoucherDenom(channelID, usdcmigrationtypes.DefaultBaseDenom)
	trace := transfertypes.ExtractDenomFromPath(usdcmigrationtypes.VoucherPath(channelID, usdcmigrationtypes.DefaultBaseDenom))
	a.TransferKeeper.Keeper.SetDenom(ctx, trace)

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
		_, err := a.TransferKeeper.Keeper.Transfer(cacheCtx, msg)
		return err
	}

	require.ErrorIs(t, transfer(voucher), usdcmigrationtypes.ErrSendBlocked)
	require.NoError(t, transfer(params.DefaultDenom))
}

// setupReturn configures the transfer channel as the Noble channel, mints the
// voucher, and registers a rate-limit contract with a 1% send quota on the
// voucher for channel "any", which a return of the whole supply would exceed.
func (suite *UpgradeTestSuite) setupReturn() (channelID, voucher string, supply math.Int, contract sdk.AccAddress) {
	suite.ConfigureTransferChannel()
	a := suite.GetNeutronZoneApp(suite.ChainA)
	ctx := suite.ChainA.GetContext()
	t := suite.T()
	channelID = suite.TransferPath.EndpointA.ChannelID

	p := usdcmigrationtypes.DefaultParams()
	p.NobleChannels = []string{channelID}
	require.NoError(t, a.UsdcMigrationKeeper.SetParams(ctx, p))

	voucher = usdcmigrationtypes.VoucherDenom(channelID, usdcmigrationtypes.DefaultBaseDenom)
	supply = math.NewInt(2_057_515_890_000)
	suite.FundAcc(suite.ChainA.SenderAccounts[1].SenderAccount.GetAddress(), sdk.NewCoins(sdk.NewCoin(voucher, supply)))

	wasmParams := a.WasmKeeper.GetParams(ctx)
	wasmParams.CodeUploadAccess = wasmtypes.AllowEverybody
	require.NoError(t, a.WasmKeeper.SetParams(ctx, wasmParams))

	owner := suite.ChainA.SenderAccounts[0].SenderAccount.GetAddress()
	codeID := suite.StoreTestCode(ctx, owner, rateLimiterWasm)
	transferModule := a.AccountKeeper.GetModuleAddress(transfertypes.ModuleName)
	initMsg := fmt.Sprintf(`{
		"gov_module": %q,
		"ibc_module": %q,
		"paths": [{"channel_id": "any", "denom": %q, "quotas": [{"name": "daily", "duration": 86400, "send_recv": [1, 1]}]}]
	}`, govAddress(), transferModule.String(), voucher)
	contract, _, err := wasmkeeper.NewDefaultPermissionKeeper(a.WasmKeeper).
		Instantiate(ctx, codeID, owner, owner, []byte(initMsg), "rate limiter", nil)
	require.NoError(t, err)
	require.NoError(t, a.RateLimitingICS4Wrapper.SetParams(ctx, ibcratelimittypes.Params{ContractAddress: contract.String()}))

	require.Equal(t, 1, suite.quotaCount(ctx, contract, "any", voucher))
	return channelID, voucher, supply, contract
}

func (suite *UpgradeTestSuite) quotaCount(ctx sdk.Context, contract sdk.AccAddress, channel, denom string) int {
	a := suite.GetNeutronZoneApp(suite.ChainA)
	query := fmt.Sprintf(`{"get_quotas": {"channel_id": %q, "denom": %q}}`, channel, denom)
	bz, err := a.WasmKeeper.QuerySmart(ctx, contract, []byte(query))
	if err != nil && strings.Contains(err.Error(), "not found") {
		return 0 // the contract has no such path
	}
	suite.Require().NoError(err)
	var quotas []json.RawMessage
	suite.Require().NoError(json.Unmarshal(bz, &quotas))
	return len(quotas)
}

func (suite *UpgradeTestSuite) TestReturnNobleEscrow() {
	channelID, voucher, supply, contract := suite.setupReturn()
	a := suite.GetNeutronZoneApp(suite.ChainA)
	ctx := suite.ChainA.GetContext().WithEventManager(sdk.NewEventManager())
	t := suite.T()

	require.NoError(t, v11_5_0.ReturnNobleEscrow(ctx, upgradeKeepers(a), channelID, nobleAddress()))

	// The rate-limit path is gone.
	require.Zero(t, suite.quotaCount(ctx, contract, "any", voucher))

	// One return packet, for the whole voucher supply, from gov to the multisig.
	var ret sdk.Event
	for _, e := range ctx.EventManager().Events() {
		if e.Type == usdcmigrationtypes.EventTypeReturnToNoble {
			ret = e
		}
	}
	require.Equal(t, 1, countEvents(ctx, usdcmigrationtypes.EventTypeReturnToNoble))
	attr := func(key string) string {
		v, ok := ret.GetAttribute(key)
		require.True(t, ok, key)
		return v.Value
	}
	require.Equal(t, supply.String(), attr(usdcmigrationtypes.AttributeKeyAmount))
	require.Equal(t, nobleAddress(), attr(usdcmigrationtypes.AttributeKeyReceiver))
	require.Equal(t, voucher, attr(usdcmigrationtypes.AttributeKeyDenom))
	require.Equal(t, 1, countEvents(ctx, "send_packet"))

	seq := attr(usdcmigrationtypes.AttributeKeySequence)
	require.Equal(t, "1", seq)
	require.NotEmpty(t, a.IBCKeeper.ChannelKeeper.GetPacketCommitment(ctx, transfertypes.PortID, channelID, 1))

	var data transfertypes.FungibleTokenPacketData
	sent := suite.sentPacketData(ctx)
	require.NoError(t, json.Unmarshal(sent, &data))
	require.Equal(t, usdcmigrationtypes.VoucherPath(channelID, usdcmigrationtypes.DefaultBaseDenom), data.Denom)
	require.Equal(t, govAddress(), data.Sender)
	require.Equal(t, nobleAddress(), data.Receiver)
	require.Empty(t, data.Memo)

	// Nothing is minted, burned or frozen on Neutron.
	require.Equal(t, supply, a.BankKeeper.GetSupply(ctx, voucher).Amount)
	require.True(t, a.BankKeeper.IsSendEnabledDenom(ctx, voucher))
}

// sentPacketData returns the data of the only send_packet event in ctx.
func (suite *UpgradeTestSuite) sentPacketData(ctx sdk.Context) []byte {
	for _, e := range ctx.EventManager().Events() {
		if e.Type != "send_packet" {
			continue
		}
		if v, ok := e.GetAttribute("packet_data_hex"); ok {
			return mustDecodeHex(suite.T(), v.Value)
		}
	}
	suite.FailNow("no send_packet event")
	return nil
}

// TestReturnNobleEscrowRollsBack checks that a failed return writes nothing:
// the rate-limit path removal is rolled back and no packet is sent.
func (suite *UpgradeTestSuite) TestReturnNobleEscrowRollsBack() {
	channelID, voucher, _, contract := suite.setupReturn()
	a := suite.GetNeutronZoneApp(suite.ChainA)
	t := suite.T()

	for name, receiver := range map[string]string{
		"unset multisig":   v11_5_0.NobleMultisig,
		"not a noble addr": suite.ChainB.SenderAccount.GetAddress().String(),
	} {
		ctx := suite.ChainA.GetContext().WithEventManager(sdk.NewEventManager())
		require.ErrorIs(t, v11_5_0.ReturnNobleEscrow(ctx, upgradeKeepers(a), channelID, receiver), usdcmigrationtypes.ErrInvalidReceiver, name)
		require.Equal(t, 1, suite.quotaCount(ctx, contract, "any", voucher), name)
		require.Zero(t, countEvents(ctx, usdcmigrationtypes.EventTypeReturnToNoble), name)
		require.Zero(t, countEvents(ctx, "send_packet"), name)
		require.Empty(t, a.IBCKeeper.ChannelKeeper.GetPacketCommitment(ctx, transfertypes.PortID, channelID, 1), name)
	}
}

// TestRateLimitBlocksReturnWithoutRemoval shows why the path is removed first:
// with the 1% quota in place the rate limiter rejects the return packet.
func (suite *UpgradeTestSuite) TestRateLimitBlocksReturnWithoutRemoval() {
	channelID, _, supply, _ := suite.setupReturn()
	a := suite.GetNeutronZoneApp(suite.ChainA)
	ctx, _ := suite.ChainA.GetContext().CacheContext()

	_, err := usdcmigrationkeeper.NewMsgServerImpl(a.UsdcMigrationKeeper).ReturnToNoble(ctx, &usdcmigrationtypes.MsgReturnToNoble{
		Authority:      govAddress(),
		ChannelId:      channelID,
		Receiver:       nobleAddress(),
		Amount:         supply,
		TimeoutSeconds: v11_5_0.ReturnTimeoutSeconds,
	})
	suite.Require().ErrorIs(err, ibcratelimittypes.ErrRateLimitExceeded)
}

func mustDecodeHex(t *testing.T, s string) []byte {
	bz, err := hex.DecodeString(s)
	require.NoError(t, err)
	return bz
}
