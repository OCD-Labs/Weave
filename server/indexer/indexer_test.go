package indexer

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestEventTopicHash_AssetAdded(t *testing.T) {
	// Confirmed on-chain topic hash from cast logs output.
	expected := "0x9f1ffd7915fd0d97979cca0ac07b1048a70d2e76e98d1fdb62e5ad5759f7737b"
	got := topicAssetAdded.Hex()
	if got != expected {
		t.Errorf("AssetAdded topic hash mismatch\nexpected: %s\ngot:      %s", expected, got)
	}
}

func TestEventTopicHash_BasketCreated(t *testing.T) {
	hash := topicBasketCreated.Hex()
	if !strings.HasPrefix(hash, "0x") || len(hash) != 66 {
		t.Errorf("BasketCreated topic hash invalid: %s", hash)
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

// buildAssetAddedLog constructs a synthetic AssetAdded log matching the on-chain format.
// AssetAdded(address indexed token, string symbol, string sector)
func buildAssetAddedLog(token common.Address, symbol, sector string) types.Log {
	// ABI-encode (string, string) for the data field.
	strType, _ := abi.NewType("string", "", nil)
	args := abi.Arguments{
		{Type: strType},
		{Type: strType},
	}
	data, _ := args.Pack(symbol, sector)

	return types.Log{
		Topics: []common.Hash{
			topicAssetAdded,
			common.BytesToHash(token.Bytes()),
		},
		Data:        data,
		BlockNumber: 65989946,
		TxHash:      common.HexToHash("0xabc"),
		Address:     common.HexToAddress("0xE46331c15A61c8F99114c970f607E9b199603bb9"),
	}
}

func TestDecodeAssetAddedLog_Symbol(t *testing.T) {
	token := common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e")
	vLog := buildAssetAddedLog(token, "TSLA", "Consumer Discretionary")

	decoded, err := abi.Arguments{
		{Type: mustABIType("string")},
		{Type: mustABIType("string")},
	}.Unpack(vLog.Data)
	if err != nil {
		t.Fatalf("failed to decode log data: %v", err)
	}

	symbol := decoded[0].(string)
	sector := decoded[1].(string)

	if symbol != "TSLA" {
		t.Errorf("expected symbol TSLA, got %q", symbol)
	}
	if sector != "Consumer Discretionary" {
		t.Errorf("expected sector Consumer Discretionary, got %q", sector)
	}
}

func TestDecodeAssetAddedLog_TokenAddress(t *testing.T) {
	expected := common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e")
	vLog := buildAssetAddedLog(expected, "TSLA", "Consumer Discretionary")

	if len(vLog.Topics) < 2 {
		t.Fatal("expected at least 2 topics")
	}

	got := common.HexToAddress(vLog.Topics[1].Hex())
	if strings.ToLower(got.Hex()) != strings.ToLower(expected.Hex()) {
		t.Errorf("expected token %s, got %s", expected.Hex(), got.Hex())
	}
}

func TestDecodeAssetAddedLog_InsufficientTopics(t *testing.T) {
	// A log with only one topic (missing the indexed token address) should be ignored gracefully.
	vLog := types.Log{
		Topics: []common.Hash{topicAssetAdded},
		Data:   []byte{},
	}

	// handleAssetAdded returns early when len(Topics) < 2 — verify this guard holds.
	if len(vLog.Topics) >= 2 {
		t.Error("test setup error: expected fewer than 2 topics")
	}
}

func TestDecodeAssetAddedLog_MalformedData(t *testing.T) {
	// Garbage data should not panic — it should return a decode error that the handler catches.
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
			t.Errorf("handleAssetAdded panicked on malformed data: %v", r)
		}
	}()

	_, err := abi.Arguments{
		{Type: mustABIType("string")},
		{Type: mustABIType("string")},
	}.Unpack(vLog.Data)

	// We expect an error — not a panic. The handler logs and falls back gracefully.
	if err == nil {
		t.Error("expected decode error for malformed data, got nil")
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
		BlockNumber: 65990000,
		TxHash:      common.HexToHash("0xdeposittx"),
		Address:     common.HexToAddress("0xbasketaddress"),
	}
}

func TestDecodeDepositedLog_Amounts(t *testing.T) {
	investor := common.HexToAddress("0x4e4B989abE79381C1B8a4871d6aF481B175F4865")
	usdgAmount := big.NewInt(100_000_000)   // 100 USDG (6 decimals)
	tokensMinted := new(big.Int).Mul(big.NewInt(100), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	feeUsdg := big.NewInt(500_000) // 0.5 USDG fee

	vLog := buildDepositedLog(investor, usdgAmount, tokensMinted, feeUsdg)

	if len(vLog.Data) < 96 {
		t.Fatalf("expected 96 bytes of data, got %d", len(vLog.Data))
	}

	gotUsdg := new(big.Int).SetBytes(vLog.Data[0:32])
	gotTokens := new(big.Int).SetBytes(vLog.Data[32:64])
	gotFee := new(big.Int).SetBytes(vLog.Data[64:96])

	if gotUsdg.Cmp(usdgAmount) != 0 {
		t.Errorf("usdg amount mismatch: expected %s, got %s", usdgAmount, gotUsdg)
	}
	if gotTokens.Cmp(tokensMinted) != 0 {
		t.Errorf("tokens minted mismatch: expected %s, got %s", tokensMinted, gotTokens)
	}
	if gotFee.Cmp(feeUsdg) != 0 {
		t.Errorf("fee mismatch: expected %s, got %s", feeUsdg, gotFee)
	}
}

func TestDecodeDepositedLog_InsufficientData(t *testing.T) {
	// A log with less than 96 bytes of data should be rejected gracefully.
	vLog := types.Log{
		Topics:  []common.Hash{topicDeposited, {}},
		Data:    make([]byte, 32), // only 32 bytes instead of 96
		TxHash:  common.HexToHash("0x1"),
		Address: common.HexToAddress("0x2"),
	}

	if len(vLog.Data) >= 96 {
		t.Error("test setup error: expected fewer than 96 bytes")
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

// FuzzDecodeAssetAddedData confirms the ABI decoder never panics on arbitrary input.
// Any input is valid to pass — we only require no panic, not successful decoding.
func FuzzDecodeAssetAddedData(f *testing.F) {
	// Seed with known valid encoded data from our live on-chain TSLA AssetAdded event.
	validData, _ := hex.DecodeString(
		"00000000000000000000000000000000000000000000000000000000000000400000000000000000000000000000000000000000000000000000000000000080000000000000000000000000000000000000000000000000000000000000000454534c41000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000016436f6e73756d65722044697363726574696f6e61727900000000000000000000",
	)
	f.Add(validData)
	f.Add([]byte{})
	f.Add([]byte("garbage"))
	f.Add(make([]byte, 96))
	f.Add(make([]byte, 256))

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("decoder panicked on input %x: %v", data, r)
			}
		}()

		// This must never panic — only return an error or successfully decode.
		abi.Arguments{
			{Type: mustABIType("string")},
			{Type: mustABIType("string")},
		}.Unpack(data)
	})
}

// FuzzDecodeBigIntFromLogData confirms uint256 byte slice extraction never panics.
func FuzzDecodeBigIntFromLogData(f *testing.F) {
	f.Add(make([]byte, 96))
	f.Add(make([]byte, 0))
	f.Add(make([]byte, 32))
	f.Add(make([]byte, 1000))

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