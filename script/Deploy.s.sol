// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Script, console} from "forge-std/Script.sol";
import {WeaveRegistry}        from "../src/WeaveRegistry.sol";
import {BasketImplementation} from "../src/BasketImplementation.sol";
import {BasketFactory}        from "../src/BasketFactory.sol";
import {WeaveAutomation}      from "../src/WeaveAutomation.sol";
import {SwapRouter}           from "../src/SwapRouter.sol";
import {OracleAdapter}        from "../src/OracleAdapter.sol";
import {IWeaveRegistry}       from "../src/interfaces/IWeaveRegistry.sol";

/// @notice Full deployment sequence for Weave on Robinhood Chain testnet.
/// Run with: make deploy
/// Prices are read from environment variables at deploy time so they are
/// never stale hardcoded constants. Set these in .env before deploying:
///   TSLA_PRICE_8DEC, AMZN_PRICE_8DEC, PLTR_PRICE_8DEC, NFLX_PRICE_8DEC, AMD_PRICE_8DEC
/// Each value is the price in 8-decimal USD (e.g. TSLA at $415.55 = 41555000000).
contract DeployWeave is Script {

    // Source: https://docs.robinhood.com/chain/contracts
    address constant USDG = 0x7E955252E15c84f5768B83c41a71F9eba181802F;
    address constant TSLA = 0xC9f9c86933092BbbfFF3CCb4b105A4A94bf3Bd4E;
    address constant AMZN = 0x5884aD2f920c162CFBbACc88C9C51AA75eC09E02;
    address constant PLTR = 0x1FBE1a0e43594b3455993B5dE5Fd0A7A266298d0;
    address constant NFLX = 0x3b8262A63d25f0477c4DDE23F83cfe22Cb768C93;
    address constant AMD  = 0x71178BAc73cBeb415514eB542a8995b82669778d;

    function run() external {
        uint256 deployerKey = vm.envUint("PRIVATE_KEY");
        address deployer    = vm.addr(deployerKey);

        // Read prices from env — never hardcode. Set in .env before deploying.
        // Format: 8-decimal USD integer. TSLA at $415.55 → 41555000000.
        int256 tslaPrice = int256(vm.envUint("TSLA_PRICE_8DEC"));
        int256 amznPrice = int256(vm.envUint("AMZN_PRICE_8DEC"));
        int256 pltrPrice = int256(vm.envUint("PLTR_PRICE_8DEC"));
        int256 nflxPrice = int256(vm.envUint("NFLX_PRICE_8DEC"));
        int256 amdPrice  = int256(vm.envUint("AMD_PRICE_8DEC"));

        vm.startBroadcast(deployerKey);

        WeaveRegistry registry = new WeaveRegistry(
            deployer,           // governance — EOA for hackathon, multisig in production
            USDG,
            deployer,           // protocolTreasury — same as deployer for now
            50,                 // managementFeeBps: 0.5%
            2_000,              // protocolShareBps: 20% of fee to protocol
            100_000_000,        // minAUMForAutomation: $100 USDG (6 dec)
            86_400,             // oracleStalenessSecs: 24 hours — OracleAdapter always fresh
            10_000_000,         // minFirstDepositUsdg: $10 USDG (6 dec)
            20,                 // maxConstituents
            100,                // minWeightBps: 1%
            1_000_000,          // minRebalanceTradeSizeUsdg: $1 USDG (6 dec)
            100                 // maxSwapSlippageBps: 1%
        );
        console.log("WeaveRegistry:       ", address(registry));

        // OracleAdapter implements IWeaveOracle with the full Chainlink return shape.
        // On mainnet, replace with direct Chainlink aggregator addresses in addAsset().
        OracleAdapter oracleTSLA = new OracleAdapter("TSLA / USD", tslaPrice);
        OracleAdapter oracleAMZN = new OracleAdapter("AMZN / USD", amznPrice);
        OracleAdapter oraclePLTR = new OracleAdapter("PLTR / USD", pltrPrice);
        OracleAdapter oracleNFLX = new OracleAdapter("NFLX / USD", nflxPrice);
        OracleAdapter oracleAMD  = new OracleAdapter("AMD / USD",  amdPrice);

        console.log("OracleAdapter TSLA:  ", address(oracleTSLA));
        console.log("OracleAdapter AMZN:  ", address(oracleAMZN));
        console.log("OracleAdapter PLTR:  ", address(oraclePLTR));
        console.log("OracleAdapter NFLX:  ", address(oracleNFLX));
        console.log("OracleAdapter AMD:   ", address(oracleAMD));

        SwapRouter swapRouter = new SwapRouter(address(registry));
        console.log("SwapRouter:          ", address(swapRouter));

        BasketImplementation implementation = new BasketImplementation();
        console.log("BasketImplementation:", address(implementation));

        BasketFactory factory = new BasketFactory(
            address(registry),
            address(implementation)
        );
        console.log("BasketFactory:       ", address(factory));

        WeaveAutomation automation = new WeaveAutomation(
            address(registry),
            deployer
        );
        console.log("WeaveAutomation:     ", address(automation));

        registry.setBasketFactory(address(factory));
        registry.setSwapRouter(address(swapRouter));
        registry.setAutomationContract(address(automation));

        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: TSLA,
            oracle:       address(oracleTSLA),
            symbol:       "TSLA",
            name:         "Tesla Inc",
            sector:       "Consumer Discretionary",
            active:       true
        }));

        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: AMZN,
            oracle:       address(oracleAMZN),
            symbol:       "AMZN",
            name:         "Amazon.com Inc",
            sector:       "Consumer Discretionary",
            active:       true
        }));

        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: PLTR,
            oracle:       address(oraclePLTR),
            symbol:       "PLTR",
            name:         "Palantir Technologies Inc",
            sector:       "Technology",
            active:       true
        }));

        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: NFLX,
            oracle:       address(oracleNFLX),
            symbol:       "NFLX",
            name:         "Netflix Inc",
            sector:       "Communication Services",
            active:       true
        }));

        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: AMD,
            oracle:       address(oracleAMD),
            symbol:       "AMD",
            name:         "Advanced Micro Devices Inc",
            sector:       "Technology",
            active:       true
        }));

        vm.stopBroadcast();

        console.log("\n--- Blockscout verification commands ---");
        console.log("make verify ADDR=%s NAME=src/WeaveRegistry.sol:WeaveRegistry",              address(registry));
        console.log("make verify ADDR=%s NAME=src/SwapRouter.sol:SwapRouter",                    address(swapRouter));
        console.log("make verify ADDR=%s NAME=src/BasketImplementation.sol:BasketImplementation", address(implementation));
        console.log("make verify ADDR=%s NAME=src/BasketFactory.sol:BasketFactory",              address(factory));
        console.log("make verify ADDR=%s NAME=src/WeaveAutomation.sol:WeaveAutomation",          address(automation));
        console.log("make verify ADDR=%s NAME=src/OracleAdapter.sol:OracleAdapter (TSLA)",       address(oracleTSLA));
    }
}