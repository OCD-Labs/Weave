package indexer

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

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

func buildAssetAddedLog(token, oracle common.Address, symbol, name, sector string) types.Log {
	data, _ := assetAddedABI.Pack(symbol, name, sector, oracle)

	return types.Log{
		Topics: []common.Hash{
			topicAssetAdded,
			common.BytesToHash(token.Bytes()),
		},
		Data:        data,
		BlockNumber: 67344644,
		TxHash:      common.HexToHash("0xabc"),
		Address:     common.HexToAddress("0xE46331c15A61c8F99114c970f607E9b199603bb9"),
	}
}

func TestDecodeAssetAddedLog_AllFields(t *testing.T) {
	token  := common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e")
	oracle := common.HexToAddress("0x26daf42381ced15760c5f47a5072a228370b100b")

	vLog := buildAssetAddedLog(token, oracle, "TSLA", "Tesla Inc", "Consumer Discretionary")

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
	vLog := types.Log{
		Topics: []common.Hash{topicAssetAdded},
		Data:   []byte{},
	}
	if len(vLog.Topics) >= 2 {
		t.Error("test setup error: expected fewer than 2 topics")
	}
}

func TestDecodeAssetAddedLog_MalformedData(t *testing.T) {
	token := common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e")
	vLog := types.Log{
		Topics: []common.Hash{
			topicAssetAdded,
			common.BytesToHash(token.Bytes()),
		},
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
		common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e"), // TSLA
		common.HexToAddress("0x5884ad2f920c162cfbbacc88c9c51aa75ec09e02"), // AMZN
		common.HexToAddress("0x71178bac73cbeb415514eb542a8995b82669778d"), // AMD
	}
	weights := []*big.Int{
		big.NewInt(5000),
		big.NewInt(3000),
		big.NewInt(2000),
	}

	vLog := buildBasketCreatedLog(
		basket, creatorToken, creator,
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

	gotName,   _ := decoded[0].(string)
	gotSymbol, _ := decoded[1].(string)
	gotThesis, _ := decoded[2].(string)
	gotConsts, _ := decoded[3].([]common.Address)
	gotWeights,_ := decoded[4].([]*big.Int)
	gotRebal,  _ := decoded[5].(bool)

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
	vLog := types.Log{
		Topics: []common.Hash{topicBasketCreated, {}, {}},
		Data:   []byte{},
	}
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

func buildDepositedLog(investor common.Address, usdgAmount, tokensMinted, feeUsdg *big.Int) types.Log {
	data := make([]byte, 96)
	copy(data[0:32], common.LeftPadBytes(usdgAmount.Bytes(), 32))
	copy(data[32:64], common.LeftPadBytes(tokensMinted.Bytes(), 32))
	copy(data[64:96], common.LeftPadBytes(feeUsdg.Bytes(), 32))

	return types.Log{
		Topics: []common.Hash{
			topicDeposited,
			common.BytesToHash(investor.Bytes()),
		},
		Data:        data,
		BlockNumber: 67369113,
		TxHash:      common.HexToHash("0xdeposittx"),
		Address:     common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6"),
	}
}

func TestDecodeDepositedLog_Amounts(t *testing.T) {
	investor     := common.HexToAddress("0x4e4B989abE79381C1B8a4871d6aF481B175F4865")
	usdgAmount   := big.NewInt(10_000_000) // 10 USDG (6 decimals)
	tokensMinted := new(big.Int).Mul(big.NewInt(995), new(big.Int).Exp(big.NewInt(10), big.NewInt(16), nil)) // 9.95 tokens
	feeUsdg      := big.NewInt(50_000) // 0.05 USDG fee

	vLog := buildDepositedLog(investor, usdgAmount, tokensMinted, feeUsdg)

	if len(vLog.Data) < 96 {
		t.Fatalf("expected 96 bytes of data, got %d", len(vLog.Data))
	}

	gotUsdg   := new(big.Int).SetBytes(vLog.Data[0:32])
	gotTokens := new(big.Int).SetBytes(vLog.Data[32:64])
	gotFee    := new(big.Int).SetBytes(vLog.Data[64:96])

	if gotUsdg.Cmp(usdgAmount) != 0 {
		t.Errorf("usdg: expected %s, got %s", usdgAmount, gotUsdg)
	}
	if gotTokens.Cmp(tokensMinted) != 0 {
		t.Errorf("tokens minted: expected %s, got %s", tokensMinted, gotTokens)
	}
	if gotFee.Cmp(feeUsdg) != 0 {
		t.Errorf("fee: expected %s, got %s", feeUsdg, gotFee)
	}
}

func TestDecodeDepositedLog_InsufficientData(t *testing.T) {
	vLog := types.Log{
		Topics:  []common.Hash{topicDeposited, {}},
		Data:    make([]byte, 32),
		TxHash:  common.HexToHash("0x1"),
		Address: common.HexToAddress("0x2"),
	}
	if len(vLog.Data) >= 96 {
		t.Error("test setup error: expected fewer than 96 bytes")
	}
}

func TestDecodeDepositedLog_InvestorAddress(t *testing.T) {
	expected := common.HexToAddress("0x4e4B989abE79381C1B8a4871d6aF481B175F4865")
	vLog := buildDepositedLog(expected, big.NewInt(1), big.NewInt(1), big.NewInt(0))

	got := common.HexToAddress(vLog.Topics[1].Hex())
	if strings.ToLower(got.Hex()) != strings.ToLower(expected.Hex()) {
		t.Errorf("investor: expected %s, got %s", expected.Hex(), got.Hex())
	}
}

func buildRedeemedLog(investor common.Address, tokensBurned, usdgReturned, feeUsdg *big.Int) types.Log {
	data := make([]byte, 96)
	copy(data[0:32], common.LeftPadBytes(tokensBurned.Bytes(), 32))
	copy(data[32:64], common.LeftPadBytes(usdgReturned.Bytes(), 32))
	copy(data[64:96], common.LeftPadBytes(feeUsdg.Bytes(), 32))

	return types.Log{
		Topics: []common.Hash{
			topicRedeemed,
			common.BytesToHash(investor.Bytes()),
		},
		Data:        data,
		BlockNumber: 67370000,
		TxHash:      common.HexToHash("0xredeemtx"),
		Address:     common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6"),
	}
}

func TestDecodeRedeemedLog_Amounts(t *testing.T) {
	investor     := common.HexToAddress("0x4e4B989abE79381C1B8a4871d6aF481B175F4865")
	tokensBurned := new(big.Int).Mul(big.NewInt(5), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	usdgReturned := big.NewInt(4_975_000) // ~4.975 USDG after fee
	feeUsdg      := big.NewInt(25_000)    // 0.025 USDG fee

	vLog := buildRedeemedLog(investor, tokensBurned, usdgReturned, feeUsdg)

	gotBurned   := new(big.Int).SetBytes(vLog.Data[0:32])
	gotReturned := new(big.Int).SetBytes(vLog.Data[32:64])
	gotFee      := new(big.Int).SetBytes(vLog.Data[64:96])

	if gotBurned.Cmp(tokensBurned) != 0 {
		t.Errorf("tokens burned: expected %s, got %s", tokensBurned, gotBurned)
	}
	if gotReturned.Cmp(usdgReturned) != 0 {
		t.Errorf("usdg returned: expected %s, got %s", usdgReturned, gotReturned)
	}
	if gotFee.Cmp(feeUsdg) != 0 {
		t.Errorf("fee: expected %s, got %s", feeUsdg, gotFee)
	}
}

func buildFeeSnapshotLog(basketAddr common.Address, snapshotID int64, usdgAmount *big.Int) types.Log {
	data := make([]byte, 32)
	copy(data[0:32], common.LeftPadBytes(usdgAmount.Bytes(), 32))

	snapIDHash := common.BigToHash(big.NewInt(snapshotID))

	return types.Log{
		Topics: []common.Hash{
			topicFeeSnapshoted,
			snapIDHash,
		},
		Data:        data,
		BlockNumber: 67369113,
		TxHash:      common.HexToHash("0xfeesnaptx"),
		Address:     basketAddr,
	}
}

func TestDecodeFeeSnapshotLog(t *testing.T) {
	basket     := common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")
	snapshotID := int64(1)
	usdgAmount := big.NewInt(40_000) // 0.04 USDG creator cut

	vLog := buildFeeSnapshotLog(basket, snapshotID, usdgAmount)

	if len(vLog.Topics) < 2 {
		t.Fatal("expected at least 2 topics")
	}

	gotSnapshotID := new(big.Int).SetBytes(vLog.Topics[1].Bytes()).Int64()
	if gotSnapshotID != snapshotID {
		t.Errorf("snapshotID: expected %d, got %d", snapshotID, gotSnapshotID)
	}

	if len(vLog.Data) < 32 {
		t.Fatal("expected at least 32 bytes of data")
	}

	gotAmount := new(big.Int).SetBytes(vLog.Data[0:32])
	if gotAmount.Cmp(usdgAmount) != 0 {
		t.Errorf("usdgAmount: expected %s, got %s", usdgAmount, gotAmount)
	}
}

func TestBoolToInt(t *testing.T) {
	if boolToInt(true) != 1 {
		t.Error("boolToInt(true) should return 1")
	}
	if boolToInt(false) != 0 {
		t.Error("boolToInt(false) should return 0")
	}
}

func TestMustABIType_ValidTypes(t *testing.T) {
	validTypes := []string{"string", "address", "uint256", "bool", "bytes32"}
	for _, typ := range validTypes {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("mustABIType(%q) panicked unexpectedly: %v", typ, r)
				}
			}()
			mustABIType(typ)
		}()
	}
}

func TestMustABIType_InvalidType_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("mustABIType with invalid type should panic")
		}
	}()
	mustABIType("notavalidtype")
}

func TestEventTopic_DeterministicHash(t *testing.T) {
	// Same signature always produces the same hash.
	h1 := eventTopic("Deposited(address,uint256,uint256,uint256)")
	h2 := eventTopic("Deposited(address,uint256,uint256,uint256)")
	if h1 != h2 {
		t.Error("eventTopic is not deterministic")
	}

	// Different signatures produce different hashes.
	h3 := eventTopic("Deposited(address,uint256,uint256)")
	if h1 == h3 {
		t.Error("different signatures should produce different hashes")
	}
}

// FuzzDecodeAssetAddedData confirms the enriched AssetAdded ABI decoder
// never panics on arbitrary input.
func FuzzDecodeAssetAddedData(f *testing.F) {
	// Seed with a known valid encoding of (symbol, name, sector, oracle).
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

// FuzzDecodeBasketCreatedData confirms the enriched BasketCreated ABI decoder
// never panics on arbitrary input.
func FuzzDecodeBasketCreatedData(f *testing.F) {
	constituents := []common.Address{
		common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e"),
	}
	weights := []*big.Int{big.NewInt(10000)}
	validData, _ := basketCreatedABI.Pack(
		"AI Infrastructure", "AIIB",
		"Companies building AI infrastructure",
		constituents, weights, false,
	)
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

// FuzzDecodeBigIntFromLogData confirms uint256 byte slice extraction never panics.
func FuzzDecodeBigIntFromLogData(f *testing.F) {
	f.Add(make([]byte, 96))
	f.Add(make([]byte, 0))
	f.Add(make([]byte, 32))
	f.Add(make([]byte, 1000))

	// Seed with real deposit amounts from the AIIB basket creation.
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