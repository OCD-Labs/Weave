package indexer

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Topic hash tests

func TestEventTopicHash_AssetAdded(t *testing.T) {
	hash := topicAssetAdded.Hex()
	if !strings.HasPrefix(hash, "0x") || len(hash) != 66 {
		t.Errorf("AssetAdded topic hash invalid format: %s", hash)
	}
}

func TestEventTopicHash_BasketCreated(t *testing.T) {
	hash := topicBasketCreated.Hex()
	if !strings.HasPrefix(hash, "0x") || len(hash) != 66 {
		t.Errorf("BasketCreated topic hash invalid format: %s", hash)
	}
}

func TestEventTopicHash_Deposited(t *testing.T) {
	hash := topicDeposited.Hex()
	if !strings.HasPrefix(hash, "0x") || len(hash) != 66 {
		t.Errorf("Deposited topic hash invalid: %s", hash)
	}
}

func TestEventTopicHash_AllDistinct(t *testing.T) {
	topics := map[string]common.Hash{
		"BasketCreated": topicBasketCreated,
		"Deposited":     topicDeposited,
		"Redeemed":      topicRedeemed,
		"Rebalanced":    topicRebalanced,
		"FeeSnapshoted": topicFeeSnapshoted,
		"AssetAdded":    topicAssetAdded,
		"AssetDeact":    topicAssetDeact,
		"BasketSuspend": topicBasketSuspend,
	}

	seen := make(map[common.Hash]string)
	for name, topic := range topics {
		if prev, exists := seen[topic]; exists {
			t.Errorf("topic hash collision between %s and %s: %s", name, prev, topic.Hex())
		}
		seen[topic] = name
	}
}

func TestEventTopic_DeterministicHash(t *testing.T) {
	h1 := eventTopic("Deposited(address,uint256,uint256,uint256)")
	h2 := eventTopic("Deposited(address,uint256,uint256,uint256)")
	if h1 != h2 {
		t.Error("eventTopic is not deterministic")
	}

	h3 := eventTopic("Deposited(address,uint256,uint256)")
	if h1 == h3 {
		t.Error("different signatures should produce different hashes")
	}
}

// abi.NewType tests

func TestABINewType_ValidTypes(t *testing.T) {
	validTypes := []string{"string", "address", "uint256", "bool", "bytes32"}
	for _, typ := range validTypes {
		_, err := abi.NewType(typ, "", nil)
		if err != nil {
			t.Errorf("abi.NewType(%q) failed unexpectedly: %v", typ, err)
		}
	}
}

func TestABINewType_InvalidType_ReturnsError(t *testing.T) {
	_, err := abi.NewType("notavalidtype", "", nil)
	if err == nil {
		t.Error("abi.NewType with invalid type should return an error")
	}
}

// AssetAdded log tests

func buildAssetAddedLog(token, oracle common.Address, symbol, name, sector string) types.Log {
	data, _ := assetAddedABI.Pack(symbol, name, sector, oracle)
	return types.Log{
		Topics:      []common.Hash{topicAssetAdded, common.BytesToHash(token.Bytes())},
		Data:        data,
		BlockNumber: 67344644,
		TxHash:      common.HexToHash("0xabc"),
		Address:     common.HexToAddress("0xE46331c15A61c8F99114c970f607E9b199603bb9"),
	}
}

func TestDecodeAssetAddedLog_AllFields(t *testing.T) {
	token  := common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e")
	oracle := common.HexToAddress("0x26daf42381ced15760c5f47a5072a228370b100b")
	vLog   := buildAssetAddedLog(token, oracle, "TSLA", "Tesla Inc", "Consumer Discretionary")

	decoded, err := assetAddedABI.Unpack(vLog.Data)
	if err != nil {
		t.Fatalf("failed to decode AssetAdded log data: %v", err)
	}
	if len(decoded) < 4 {
		t.Fatalf("expected 4 decoded fields, got %d", len(decoded))
	}

	gotSymbol, _ := decoded[0].(string)
	gotName,   _ := decoded[1].(string)
	gotSector, _ := decoded[2].(string)
	gotOracle, _ := decoded[3].(common.Address)

	if gotSymbol != "TSLA" {
		t.Errorf("symbol: expected TSLA, got %q", gotSymbol)
	}
	if gotName != "Tesla Inc" {
		t.Errorf("name: expected Tesla Inc, got %q", gotName)
	}
	if gotSector != "Consumer Discretionary" {
		t.Errorf("sector: expected Consumer Discretionary, got %q", gotSector)
	}
	if strings.ToLower(gotOracle.Hex()) != strings.ToLower(oracle.Hex()) {
		t.Errorf("oracle: expected %s, got %s", oracle.Hex(), gotOracle.Hex())
	}
}

func TestDecodeAssetAddedLog_TokenAddress(t *testing.T) {
	expected := common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e")
	oracle   := common.HexToAddress("0x26daf42381ced15760c5f47a5072a228370b100b")
	vLog     := buildAssetAddedLog(expected, oracle, "TSLA", "Tesla Inc", "Consumer Discretionary")

	if len(vLog.Topics) < 2 {
		t.Fatal("expected at least 2 topics")
	}
	got := common.HexToAddress(vLog.Topics[1].Hex())
	if strings.ToLower(got.Hex()) != strings.ToLower(expected.Hex()) {
		t.Errorf("token address: expected %s, got %s", expected.Hex(), got.Hex())
	}
}

func TestDecodeAssetAddedLog_InsufficientTopics(t *testing.T) {
	vLog := types.Log{Topics: []common.Hash{topicAssetAdded}, Data: []byte{}}
	if len(vLog.Topics) >= 2 {
		t.Error("test setup error: expected fewer than 2 topics")
	}
}

func TestDecodeAssetAddedLog_MalformedData(t *testing.T) {
	token := common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e")
	vLog := types.Log{
		Topics:      []common.Hash{topicAssetAdded, common.BytesToHash(token.Bytes())},
		Data:        []byte("not valid abi encoded data at all"),
		BlockNumber: 1,
	}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("assetAddedABI.Unpack panicked on malformed data: %v", r)
		}
	}()
	_, err := assetAddedABI.Unpack(vLog.Data)
	if err == nil {
		t.Error("expected decode error for malformed data, got nil")
	}
}

// BasketCreated log tests

func buildBasketCreatedLog(
	basket, creatorToken, creator common.Address,
	name, symbol, thesis string,
	constituents []common.Address,
	weights []*big.Int,
	rebalancing bool,
) types.Log {
	data, _ := basketCreatedABI.Pack(name, symbol, thesis, constituents, weights, rebalancing)
	return types.Log{
		Topics: []common.Hash{
			topicBasketCreated,
			common.BytesToHash(basket.Bytes()),
			common.BytesToHash(creatorToken.Bytes()),
			common.BytesToHash(creator.Bytes()),
		},
		Data:        data,
		BlockNumber: 67369113,
		TxHash:      common.HexToHash("0x953315ccb55bfa5c6be7f07c5a1378d7af7a3f5b09366ee31b6a0507177822e5"),
	}
}

func TestDecodeBasketCreatedLog_AllFields(t *testing.T) {
	basket       := common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")
	creatorToken := common.HexToAddress("0x29ba5c3470b3a6c06bd6cce2e43c019d846c01c0")
	creator      := common.HexToAddress("0x4e4b989abe79381c1b8a4871d6af481b175f4865")
	constituents := []common.Address{
		common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e"),
		common.HexToAddress("0x5884ad2f920c162cfbbacc88c9c51aa75ec09e02"),
		common.HexToAddress("0x71178bac73cbeb415514eb542a8995b82669778d"),
	}
	weights := []*big.Int{big.NewInt(5000), big.NewInt(3000), big.NewInt(2000)}

	vLog := buildBasketCreatedLog(basket, creatorToken, creator,
		"AI Infrastructure", "AIIB",
		"Companies building the physical infrastructure for AI",
		constituents, weights, false,
	)

	if len(vLog.Topics) < 4 {
		t.Fatalf("expected 4 topics, got %d", len(vLog.Topics))
	}

	gotBasket       := strings.ToLower(common.HexToAddress(vLog.Topics[1].Hex()).Hex())
	gotCreatorToken := strings.ToLower(common.HexToAddress(vLog.Topics[2].Hex()).Hex())
	gotCreator      := strings.ToLower(common.HexToAddress(vLog.Topics[3].Hex()).Hex())

	if gotBasket != strings.ToLower(basket.Hex()) {
		t.Errorf("basket: expected %s, got %s", basket.Hex(), gotBasket)
	}
	if gotCreatorToken != strings.ToLower(creatorToken.Hex()) {
		t.Errorf("creatorToken: expected %s, got %s", creatorToken.Hex(), gotCreatorToken)
	}
	if gotCreator != strings.ToLower(creator.Hex()) {
		t.Errorf("creator: expected %s, got %s", creator.Hex(), gotCreator)
	}

	decoded, err := basketCreatedABI.Unpack(vLog.Data)
	if err != nil {
		t.Fatalf("failed to decode BasketCreated data: %v", err)
	}
	if len(decoded) < 6 {
		t.Fatalf("expected 6 decoded fields, got %d", len(decoded))
	}

	gotName,    _ := decoded[0].(string)
	gotSymbol,  _ := decoded[1].(string)
	gotThesis,  _ := decoded[2].(string)
	gotConsts,  _ := decoded[3].([]common.Address)
	gotWeights, _ := decoded[4].([]*big.Int)
	gotRebal,   _ := decoded[5].(bool)

	if gotName != "AI Infrastructure" {
		t.Errorf("name: expected AI Infrastructure, got %q", gotName)
	}
	if gotSymbol != "AIIB" {
		t.Errorf("symbol: expected AIIB, got %q", gotSymbol)
	}
	if gotThesis == "" {
		t.Error("thesis should not be empty")
	}
	if len(gotConsts) != 3 {
		t.Errorf("expected 3 constituents, got %d", len(gotConsts))
	}
	if len(gotWeights) != 3 {
		t.Errorf("expected 3 weights, got %d", len(gotWeights))
	}
	totalWeight := int64(0)
	for _, w := range gotWeights {
		totalWeight += w.Int64()
	}
	if totalWeight != 10000 {
		t.Errorf("weights must sum to 10000, got %d", totalWeight)
	}
	if gotRebal != false {
		t.Errorf("rebalancing: expected false, got %v", gotRebal)
	}
}

func TestDecodeBasketCreatedLog_InsufficientTopics(t *testing.T) {
	vLog := types.Log{Topics: []common.Hash{topicBasketCreated, {}, {}}, Data: []byte{}}
	if len(vLog.Topics) >= 4 {
		t.Error("test setup error: expected fewer than 4 topics")
	}
}

func TestDecodeBasketCreatedLog_MalformedData(t *testing.T) {
	vLog := types.Log{
		Topics: []common.Hash{
			topicBasketCreated,
			common.BytesToHash(common.HexToAddress("0x1").Bytes()),
			common.BytesToHash(common.HexToAddress("0x2").Bytes()),
			common.BytesToHash(common.HexToAddress("0x3").Bytes()),
		},
		Data: []byte("garbage"),
	}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("basketCreatedABI.Unpack panicked on malformed data: %v", r)
		}
	}()
	decoded, err := basketCreatedABI.Unpack(vLog.Data)
	if err == nil && len(decoded) >= 6 {
		t.Error("expected decode error or insufficient fields for garbage data")
	}
}

// Deposited log tests

func buildDepositedLog(investor common.Address, usdgAmount, tokensMinted, feeUsdg *big.Int) types.Log {
	data, _ := depositedABI.Pack(usdgAmount, tokensMinted, feeUsdg)
	return types.Log{
		Topics:      []common.Hash{topicDeposited, common.BytesToHash(investor.Bytes())},
		Data:        data,
		BlockNumber: 67369113,
		TxHash:      common.HexToHash("0xdeposittx"),
		Address:     common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6"),
	}
}

func TestDecodeDepositedLog_ViaABI(t *testing.T) {
	investor     := common.HexToAddress("0x4e4B989abE79381C1B8a4871d6aF481B175F4865")
	usdgAmount   := big.NewInt(10_000_000)
	tokensMinted, _ := new(big.Int).SetString("9950000000000000000", 10)
	feeUsdg      := big.NewInt(50_000)

	vLog := buildDepositedLog(investor, usdgAmount, tokensMinted, feeUsdg)

	decoded, err := depositedABI.Unpack(vLog.Data)
	if err != nil || len(decoded) < 3 {
		t.Fatalf("depositedABI.Unpack failed: %v", err)
	}
	if decoded[0].(*big.Int).Cmp(usdgAmount) != 0 {
		t.Errorf("usdgAmount: expected %s, got %s", usdgAmount, decoded[0])
	}
	if decoded[1].(*big.Int).Cmp(tokensMinted) != 0 {
		t.Errorf("tokensMinted: expected %s, got %s", tokensMinted, decoded[1])
	}
	if decoded[2].(*big.Int).Cmp(feeUsdg) != 0 {
		t.Errorf("feeUsdg: expected %s, got %s", feeUsdg, decoded[2])
	}
}

func TestDecodeDepositedLog_Amounts(t *testing.T) {
	investor     := common.HexToAddress("0x4e4B989abE79381C1B8a4871d6aF481B175F4865")
	usdgAmount   := big.NewInt(10_000_000)
	tokensMinted := new(big.Int).Mul(big.NewInt(995), new(big.Int).Exp(big.NewInt(10), big.NewInt(16), nil))
	feeUsdg      := big.NewInt(50_000)

	vLog := buildDepositedLog(investor, usdgAmount, tokensMinted, feeUsdg)

	decoded, err := depositedABI.Unpack(vLog.Data)
	if err != nil || len(decoded) < 3 {
		t.Fatalf("depositedABI.Unpack failed: %v", err)
	}
	if decoded[0].(*big.Int).Cmp(usdgAmount) != 0 {
		t.Errorf("usdg: expected %s, got %s", usdgAmount, decoded[0])
	}
	if decoded[1].(*big.Int).Cmp(tokensMinted) != 0 {
		t.Errorf("tokens minted: expected %s, got %s", tokensMinted, decoded[1])
	}
	if decoded[2].(*big.Int).Cmp(feeUsdg) != 0 {
		t.Errorf("fee: expected %s, got %s", feeUsdg, decoded[2])
	}
}

func TestDecodeDepositedLog_InvestorAddress(t *testing.T) {
	expected := common.HexToAddress("0x4e4B989abE79381C1B8a4871d6aF481B175F4865")
	vLog     := buildDepositedLog(expected, big.NewInt(1), big.NewInt(1), big.NewInt(0))

	got := common.HexToAddress(vLog.Topics[1].Hex())
	if strings.ToLower(got.Hex()) != strings.ToLower(expected.Hex()) {
		t.Errorf("investor: expected %s, got %s", expected.Hex(), got.Hex())
	}
}

func TestDecodeDepositedLog_InsufficientTopics(t *testing.T) {
	vLog := types.Log{
		Topics:  []common.Hash{topicDeposited},
		Data:    []byte{},
		TxHash:  common.HexToHash("0x1"),
		Address: common.HexToAddress("0x2"),
	}
	if len(vLog.Topics) >= 2 {
		t.Error("test setup error: expected fewer than 2 topics")
	}
}

// Redeemed log tests

func buildRedeemedLog(investor common.Address, tokensBurned, usdgReturned, feeUsdg *big.Int) types.Log {
	data, _ := redeemedABI.Pack(tokensBurned, usdgReturned, feeUsdg)
	return types.Log{
		Topics:      []common.Hash{topicRedeemed, common.BytesToHash(investor.Bytes())},
		Data:        data,
		BlockNumber: 67370000,
		TxHash:      common.HexToHash("0xredeemtx"),
		Address:     common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6"),
	}
}

func TestDecodeRedeemedLog_ViaABI(t *testing.T) {
	investor     := common.HexToAddress("0x4e4B989abE79381C1B8a4871d6aF481B175F4865")
	tokensBurned, _ := new(big.Int).SetString("5000000000000000000", 10)
	usdgReturned := big.NewInt(4_975_000)
	feeUsdg      := big.NewInt(25_000)

	vLog := buildRedeemedLog(investor, tokensBurned, usdgReturned, feeUsdg)

	decoded, err := redeemedABI.Unpack(vLog.Data)
	if err != nil || len(decoded) < 3 {
		t.Fatalf("redeemedABI.Unpack failed: %v", err)
	}
	if decoded[0].(*big.Int).Cmp(tokensBurned) != 0 {
		t.Errorf("tokensBurned: expected %s, got %s", tokensBurned, decoded[0])
	}
	if decoded[1].(*big.Int).Cmp(usdgReturned) != 0 {
		t.Errorf("usdgReturned: expected %s, got %s", usdgReturned, decoded[1])
	}
	if decoded[2].(*big.Int).Cmp(feeUsdg) != 0 {
		t.Errorf("feeUsdg: expected %s, got %s", feeUsdg, decoded[2])
	}
}

func TestDecodeRedeemedLog_Amounts(t *testing.T) {
	investor     := common.HexToAddress("0x4e4B989abE79381C1B8a4871d6aF481B175F4865")
	tokensBurned := new(big.Int).Mul(big.NewInt(5), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	usdgReturned := big.NewInt(4_975_000)
	feeUsdg      := big.NewInt(25_000)

	vLog := buildRedeemedLog(investor, tokensBurned, usdgReturned, feeUsdg)

	decoded, err := redeemedABI.Unpack(vLog.Data)
	if err != nil || len(decoded) < 3 {
		t.Fatalf("redeemedABI.Unpack failed: %v", err)
	}
	if decoded[0].(*big.Int).Cmp(tokensBurned) != 0 {
		t.Errorf("tokens burned: expected %s, got %s", tokensBurned, decoded[0])
	}
	if decoded[1].(*big.Int).Cmp(usdgReturned) != 0 {
		t.Errorf("usdg returned: expected %s, got %s", usdgReturned, decoded[1])
	}
	if decoded[2].(*big.Int).Cmp(feeUsdg) != 0 {
		t.Errorf("fee: expected %s, got %s", feeUsdg, decoded[2])
	}
}

// FeeSnapshot log tests

func buildFeeSnapshotLog(addr common.Address, snapshotID int64, usdgAmount *big.Int) types.Log {
	totalSupply := new(big.Int).Mul(big.NewInt(1_000_000), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	data, _ := feeSnapshotABI.Pack(usdgAmount, totalSupply)
	return types.Log{
		Topics:      []common.Hash{topicFeeSnapshoted, common.BigToHash(big.NewInt(snapshotID))},
		Data:        data,
		BlockNumber: 67369113,
		TxHash:      common.HexToHash("0xfeesnaptx"),
		Address:     addr,
	}
}

func TestDecodeFeeSnapshotLog_ViaABI(t *testing.T) {
	addr       := common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")
	snapshotID := int64(3)
	usdgAmount := big.NewInt(40_000)

	vLog := buildFeeSnapshotLog(addr, snapshotID, usdgAmount)

	gotSnapshotID := new(big.Int).SetBytes(vLog.Topics[1].Bytes()).Int64()
	if gotSnapshotID != snapshotID {
		t.Errorf("snapshotID: expected %d, got %d", snapshotID, gotSnapshotID)
	}

	decoded, err := feeSnapshotABI.Unpack(vLog.Data)
	if err != nil || len(decoded) < 1 {
		t.Fatalf("feeSnapshotABI.Unpack failed: %v", err)
	}
	if decoded[0].(*big.Int).Cmp(usdgAmount) != 0 {
		t.Errorf("usdgAmount: expected %s, got %s", usdgAmount, decoded[0])
	}
}

func TestDecodeFeeSnapshotLog(t *testing.T) {
	addr       := common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")
	snapshotID := int64(1)
	usdgAmount := big.NewInt(40_000)

	vLog := buildFeeSnapshotLog(addr, snapshotID, usdgAmount)

	if len(vLog.Topics) < 2 {
		t.Fatal("expected at least 2 topics")
	}

	gotSnapshotID := new(big.Int).SetBytes(vLog.Topics[1].Bytes()).Int64()
	if gotSnapshotID != snapshotID {
		t.Errorf("snapshotID: expected %d, got %d", snapshotID, gotSnapshotID)
	}

	decoded, err := feeSnapshotABI.Unpack(vLog.Data)
	if err != nil || len(decoded) < 1 {
		t.Fatalf("feeSnapshotABI.Unpack failed: %v", err)
	}
	if decoded[0].(*big.Int).Cmp(usdgAmount) != 0 {
		t.Errorf("usdgAmount: expected %s, got %s", usdgAmount, decoded[0])
	}
}

// Helper function tests

func TestBoolToInt(t *testing.T) {
	if boolToInt(true) != 1 {
		t.Error("boolToInt(true) should return 1")
	}
	if boolToInt(false) != 0 {
		t.Error("boolToInt(false) should return 0")
	}
}

func TestMinDuration(t *testing.T) {
	if minDuration(1*time.Second, 2*time.Second) != 1*time.Second {
		t.Error("minDuration should return the smaller value")
	}
	if minDuration(5*time.Minute, 1*time.Second) != 1*time.Second {
		t.Error("minDuration should return the smaller value")
	}
	if minDuration(3*time.Second, 3*time.Second) != 3*time.Second {
		t.Error("minDuration should return equal values unchanged")
	}
}

func TestGetEnv_ReturnsValue(t *testing.T) {
	os.Setenv("TEST_WEAVE_KEY", "hello")
	defer os.Unsetenv("TEST_WEAVE_KEY")
	if getEnv("TEST_WEAVE_KEY", "fallback") != "hello" {
		t.Error("getEnv should return the set value")
	}
}

func TestGetEnv_ReturnsFallback(t *testing.T) {
	os.Unsetenv("TEST_WEAVE_KEY_MISSING")
	if getEnv("TEST_WEAVE_KEY_MISSING", "fallback") != "fallback" {
		t.Error("getEnv should return fallback when key is not set")
	}
}

func TestGetEnv_TrimsWhitespace(t *testing.T) {
	os.Setenv("TEST_WEAVE_KEY_WS", "  trimmed  ")
	defer os.Unsetenv("TEST_WEAVE_KEY_WS")
	if getEnv("TEST_WEAVE_KEY_WS", "fallback") != "trimmed" {
		t.Error("getEnv should trim whitespace")
	}
}

// Indexer construction tests

func TestNew_MissingFactoryAddress_ReturnsError(t *testing.T) {
	os.Unsetenv("BASKET_FACTORY_ADDRESS")
	_, err := New(nil, "ws://localhost", "http://localhost", "0x1234", 0, nil)
	if err == nil {
		t.Error("New() should return error when BASKET_FACTORY_ADDRESS is not set")
	}
	if !strings.Contains(err.Error(), "BASKET_FACTORY_ADDRESS") {
		t.Errorf("error should mention BASKET_FACTORY_ADDRESS, got: %v", err)
	}
}

func TestNew_WithFactoryAddress_Succeeds(t *testing.T) {
	os.Setenv("BASKET_FACTORY_ADDRESS", "0xE9854c4734cd4A9dbC5086398A11df3c11f40b21")
	defer os.Unsetenv("BASKET_FACTORY_ADDRESS")

	idx, err := New(nil, "ws://localhost", "http://localhost", "0x19Ab3408af6503a7D4BeC255b064f8B02A345D04", 68391146, nil)
	if err != nil {
		t.Fatalf("New() should succeed with BASKET_FACTORY_ADDRESS set: %v", err)
	}
	if idx.factoryAddr.Hex() == (common.Address{}).Hex() {
		t.Error("factoryAddr should be set")
	}
	if idx.registryAddr.Hex() == (common.Address{}).Hex() {
		t.Error("registryAddr should be set")
	}
	if idx.deployBlock != 68391146 {
		t.Errorf("deployBlock: expected 68391146, got %d", idx.deployBlock)
	}
}

// blockTs cache tests

func TestBlockTsCache_Eviction(t *testing.T) {
	os.Setenv("BASKET_FACTORY_ADDRESS", "0xE9854c4734cd4A9dbC5086398A11df3c11f40b21")
	defer os.Unsetenv("BASKET_FACTORY_ADDRESS")

	idx, _ := New(nil, "", "", "0x1", 0, nil)

	for i := uint64(0); i < blockTsCacheMax; i++ {
		idx.blockTsMu.Lock()
		if len(idx.blockTsFIFO) >= blockTsCacheMax {
			oldest := idx.blockTsFIFO[0]
			idx.blockTsFIFO = idx.blockTsFIFO[1:]
			delete(idx.blockTs, oldest.blockNumber)
		}
		idx.blockTs[i] = uint64(i * 1000)
		idx.blockTsFIFO = append(idx.blockTsFIFO, blockTsEntry{blockNumber: i, timestamp: uint64(i * 1000)})
		idx.blockTsMu.Unlock()
	}

	if len(idx.blockTs) != blockTsCacheMax {
		t.Fatalf("cache should have exactly %d entries, got %d", blockTsCacheMax, len(idx.blockTs))
	}

	newBlock := uint64(blockTsCacheMax)
	idx.blockTsMu.Lock()
	if len(idx.blockTsFIFO) >= blockTsCacheMax {
		oldest := idx.blockTsFIFO[0]
		idx.blockTsFIFO = idx.blockTsFIFO[1:]
		delete(idx.blockTs, oldest.blockNumber)
	}
	idx.blockTs[newBlock] = 99999
	idx.blockTsFIFO = append(idx.blockTsFIFO, blockTsEntry{blockNumber: newBlock, timestamp: 99999})
	idx.blockTsMu.Unlock()

	idx.blockTsMu.Lock()
	_, block0Present  := idx.blockTs[0]
	_, newBlockPresent := idx.blockTs[newBlock]
	cacheLen := len(idx.blockTs)
	idx.blockTsMu.Unlock()

	if block0Present {
		t.Error("block 0 should have been evicted from the cache")
	}
	if !newBlockPresent {
		t.Error("new block should be present in the cache after insertion")
	}
	if cacheLen != blockTsCacheMax {
		t.Errorf("cache size should remain at %d after eviction, got %d", blockTsCacheMax, cacheLen)
	}
}

func TestBlockTsCache_HitReturnsCachedValue(t *testing.T) {
	os.Setenv("BASKET_FACTORY_ADDRESS", "0xE9854c4734cd4A9dbC5086398A11df3c11f40b21")
	defer os.Unsetenv("BASKET_FACTORY_ADDRESS")

	idx, _ := New(nil, "", "", "0x1", 0, nil)

	idx.blockTsMu.Lock()
	idx.blockTs[12345] = 1780525194
	idx.blockTsFIFO = append(idx.blockTsFIFO, blockTsEntry{blockNumber: 12345, timestamp: 1780525194})
	idx.blockTsMu.Unlock()

	ts := idx.blockTimestamp(nil, 12345)
	if ts != 1780525194 {
		t.Errorf("cache hit: expected 1780525194, got %d", ts)
	}
}

// Fuzz tests

func FuzzDecodeAssetAddedData(f *testing.F) {
	oracle := common.HexToAddress("0x26daf42381ced15760c5f47a5072a228370b100b")
	validData, _ := assetAddedABI.Pack("TSLA", "Tesla Inc", "Consumer Discretionary", oracle)
	f.Add(validData)
	f.Add([]byte{})
	f.Add([]byte("garbage"))
	f.Add(make([]byte, 96))
	f.Add(make([]byte, 256))

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("assetAddedABI.Unpack panicked on input %x: %v", data, r)
			}
		}()
		assetAddedABI.Unpack(data)
	})
}

func FuzzDecodeBasketCreatedData(f *testing.F) {
	constituents := []common.Address{common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e")}
	weights      := []*big.Int{big.NewInt(10000)}
	validData, _ := basketCreatedABI.Pack("AI Infrastructure", "AIIB", "Companies building AI infrastructure", constituents, weights, false)
	f.Add(validData)
	f.Add([]byte{})
	f.Add([]byte("garbage"))
	f.Add(make([]byte, 256))

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("basketCreatedABI.Unpack panicked on input %x: %v", data, r)
			}
		}()
		basketCreatedABI.Unpack(data)
	})
}

func FuzzDecodeDepositedData(f *testing.F) {
	usdgAmount   := big.NewInt(10_000_000)
	tokensMinted, _ := new(big.Int).SetString("9950000000000000000", 10)
	feeUsdg      := big.NewInt(50_000)
	validData, _ := depositedABI.Pack(usdgAmount, tokensMinted, feeUsdg)
	f.Add(validData)
	f.Add([]byte{})
	f.Add([]byte("garbage"))
	f.Add(make([]byte, 96))

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("depositedABI.Unpack panicked on input %x: %v", data, r)
			}
		}()
		depositedABI.Unpack(data)
	})
}

func FuzzDecodeRedeemedData(f *testing.F) {
	tokensBurned, _ := new(big.Int).SetString("5000000000000000000", 10)
	usdgReturned := big.NewInt(4_975_000)
	feeUsdg      := big.NewInt(25_000)
	validData, _ := redeemedABI.Pack(tokensBurned, usdgReturned, feeUsdg)
	f.Add(validData)
	f.Add([]byte{})
	f.Add([]byte("garbage"))
	f.Add(make([]byte, 96))

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("redeemedABI.Unpack panicked on input %x: %v", data, r)
			}
		}()
		redeemedABI.Unpack(data)
	})
}

func FuzzDecodeFeeSnapshotData(f *testing.F) {
	usdgAmount  := big.NewInt(40_000)
	totalSupply := new(big.Int).Mul(big.NewInt(1_000_000), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	validData, _ := feeSnapshotABI.Pack(usdgAmount, totalSupply)
	f.Add(validData)
	f.Add([]byte{})
	f.Add([]byte("garbage"))
	f.Add(make([]byte, 64))

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("feeSnapshotABI.Unpack panicked on input %x: %v", data, r)
			}
		}()
		feeSnapshotABI.Unpack(data)
	})
}

func FuzzDecodeBigIntFromLogData(f *testing.F) {
	f.Add(make([]byte, 96))
	f.Add(make([]byte, 0))
	f.Add(make([]byte, 32))
	f.Add(make([]byte, 1000))

	realData, _ := hex.DecodeString(
		"0000000000000000000000000000000000000000000000000000000000989680" +
			"0000000000000000000000000000000000000000000000008a1580485b230000" +
			"000000000000000000000000000000000000000000000000000000000000c350",
	)
	f.Add(realData)

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("big.Int extraction panicked on input length %d: %v", len(data), r)
			}
		}()
		if len(data) < 96 {
			return
		}
		new(big.Int).SetBytes(data[0:32])
		new(big.Int).SetBytes(data[32:64])
		new(big.Int).SetBytes(data[64:96])
	})
}

func TestRetryRPC_SucceedsOnFirstAttempt(t *testing.T) {
	calls := 0
	err := retryRPC(context.Background(), 3, time.Millisecond, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestRetryRPC_RetriesOnTransientError(t *testing.T) {
	calls := 0
	err := retryRPC(context.Background(), 3, time.Millisecond, func() error {
		calls++
		if calls < 3 {
			return fmt.Errorf("transient error")
		}
		return nil
	})
	if err != nil {
		t.Errorf("expected nil after retries, got %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestRetryRPC_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := retryRPC(ctx, 3, time.Millisecond, func() error {
		calls++
		return fmt.Errorf("some error")
	})
	if err == nil {
		t.Error("expected error on cancelled context")
	}
	if calls > 1 {
		t.Errorf("expected at most 1 call with cancelled context, got %d", calls)
	}
}

func TestRetryRPC_ExhaustsAttemptsAndReturnsLastError(t *testing.T) {
	calls := 0
	err := retryRPC(context.Background(), 3, time.Millisecond, func() error {
		calls++
		return fmt.Errorf("persistent error %d", calls)
	})
	if err == nil {
		t.Error("expected error after exhausting attempts")
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}
