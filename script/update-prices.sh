#!/usr/bin/env bash
# update-prices.sh — Simulates market movement by drifting oracle prices ±5% each run.
# Reads current prices from the chain so drift is continuous across invocations.
# Run every 5 minutes: watch -n 300 ./scripts/update-prices.sh
set -uo pipefail

RPC_URL="${RPC_URL:-https://rpc.testnet.chain.robinhood.com}"
PRIVATE_KEY="${PRIVATE_KEY:?PRIVATE_KEY env var is required}"

ORACLE_TSLA="0xb4cCD5Aff61fCc5f87b2C79A777992561C4d5BD9"
ORACLE_AMZN="0xe2a8fa094812435B74c1b2081Ba35630b9b1Cbb7"
ORACLE_PLTR="0x0B01F4D56b39c534cDab9eCe15708E249bA8FC36"
ORACLE_NFLX="0xb98Fb1AC54dc33d33F6937b3ce8B93baE4f4F005"
ORACLE_AMD="0xDaf7e6168A748A0348e8392d31377B486D9278Ab"

# Price floors and ceilings in 8-decimal oracle units (price * 1e8)
FLOOR_TSLA=15000000000   # $150
CEIL_TSLA=40000000000    # $400
FLOOR_AMZN=15000000000   # $150
CEIL_AMZN=40000000000    # $400
FLOOR_PLTR=5000000000    # $50
CEIL_PLTR=20000000000    # $200
FLOOR_NFLX=50000000000   # $500
CEIL_NFLX=150000000000   # $1500
FLOOR_AMD=5000000000     # $50
CEIL_AMD=25000000000     # $250

MAX_DRIFT_PCT=5

drift() {
  local price=$1
  local floor=$2
  local ceil=$3
  local rand=$(( RANDOM % (MAX_DRIFT_PCT * 200 + 1) ))
  local delta_bps=$(( rand - MAX_DRIFT_PCT * 100 ))
  local new_price=$(( price + price * delta_bps / 10000 ))
  [ "$new_price" -lt "$floor" ] && new_price=$floor
  [ "$new_price" -gt "$ceil"  ] && new_price=$ceil
  echo "$new_price"
}

read_price() {
  local oracle=$1
  cast call "$oracle" "latestRoundData()(uint80,int256,uint256,uint256,uint80)" \
    --rpc-url "$RPC_URL" | sed 's/\[.*\]//' | awk 'NR==2{gsub(/[^0-9]/,"",$0); print $0}'
}

echo "Reading current prices from chain..."
PRICE_TSLA=$(read_price "$ORACLE_TSLA")
PRICE_AMZN=$(read_price "$ORACLE_AMZN")
PRICE_PLTR=$(read_price "$ORACLE_PLTR")
PRICE_NFLX=$(read_price "$ORACLE_NFLX")
PRICE_AMD=$(read_price  "$ORACLE_AMD")

PRICE_TSLA=$(drift "$PRICE_TSLA" "$FLOOR_TSLA" "$CEIL_TSLA")
PRICE_AMZN=$(drift "$PRICE_AMZN" "$FLOOR_AMZN" "$CEIL_AMZN")
PRICE_PLTR=$(drift "$PRICE_PLTR" "$FLOOR_PLTR" "$CEIL_PLTR")
PRICE_NFLX=$(drift "$PRICE_NFLX" "$FLOOR_NFLX" "$CEIL_NFLX")
PRICE_AMD=$(drift  "$PRICE_AMD"  "$FLOOR_AMD"  "$CEIL_AMD")

echo "Pushing new prices..."
cast send "$ORACLE_TSLA" "setPrice(int256)" "$PRICE_TSLA" --rpc-url "$RPC_URL" --private-key "$PRIVATE_KEY" --quiet
echo "TSLA: \$$(echo "scale=2; $PRICE_TSLA / 100000000" | bc)"
cast send "$ORACLE_AMZN" "setPrice(int256)" "$PRICE_AMZN" --rpc-url "$RPC_URL" --private-key "$PRIVATE_KEY" --quiet
echo "AMZN: \$$(echo "scale=2; $PRICE_AMZN / 100000000" | bc)"
cast send "$ORACLE_PLTR" "setPrice(int256)" "$PRICE_PLTR" --rpc-url "$RPC_URL" --private-key "$PRIVATE_KEY" --quiet
echo "PLTR: \$$(echo "scale=2; $PRICE_PLTR / 100000000" | bc)"
cast send "$ORACLE_NFLX" "setPrice(int256)" "$PRICE_NFLX" --rpc-url "$RPC_URL" --private-key "$PRIVATE_KEY" --quiet
echo "NFLX: \$$(echo "scale=2; $PRICE_NFLX / 100000000" | bc)"
cast send "$ORACLE_AMD"  "setPrice(int256)" "$PRICE_AMD"  --rpc-url "$RPC_URL" --private-key "$PRIVATE_KEY" --quiet
echo "AMD:  \$$(echo "scale=2; $PRICE_AMD  / 100000000" | bc)"

echo ""
echo "Done. Backend picks up changes within 60 seconds."