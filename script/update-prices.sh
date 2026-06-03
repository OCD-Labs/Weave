#!/usr/bin/env bash
# update-prices.sh — Simulates market movement by drifting oracle prices ±5% each run.
# Reads current prices from the chain so drift is continuous across invocations.
# Run every 5 minutes: watch -n 300 ./scripts/update-prices.sh
set -uo pipefail

RPC_URL="${RPC_URL:-https://rpc.testnet.chain.robinhood.com}"
PRIVATE_KEY="${PRIVATE_KEY:?PRIVATE_KEY env var is required}"

ORACLE_TSLA="0xd8dfF4450DDBf180C5924cfA90b6A3b3cf0c7FaF"
ORACLE_AMZN="0x255C54477bD6C5Bce0a08788794180a8d8F2C407"
ORACLE_PLTR="0x466D0b351D32176a76E289305F570d60A16cd9D5"
ORACLE_NFLX="0xACd54cFFF7694101ca495d7F383f391Fb9015aC9"
ORACLE_AMD="0x1e10204e625ED4Ec090B46fd5f4e943a93464e15"

MAX_DRIFT_PCT=5

drift() {
  local price=$1
  local rand=$(( RANDOM % (MAX_DRIFT_PCT * 200 + 1) ))
  local delta_bps=$(( rand - MAX_DRIFT_PCT * 100 ))
  local new_price=$(( price + price * delta_bps / 10000 ))
  [ "$new_price" -le 0 ] && new_price=100000000
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

PRICE_TSLA=$(drift "$PRICE_TSLA")
PRICE_AMZN=$(drift "$PRICE_AMZN")
PRICE_PLTR=$(drift "$PRICE_PLTR")
PRICE_NFLX=$(drift "$PRICE_NFLX")
PRICE_AMD=$(drift  "$PRICE_AMD")

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