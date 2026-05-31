// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Script, console} from "forge-std/Script.sol";
import {WeaveRegistry}        from "../src/WeaveRegistry.sol";
import {BasketImplementation} from "../src/BasketImplementation.sol";
import {BasketFactory}        from "../src/BasketFactory.sol";
import {WeaveAutomation}      from "../src/WeaveAutomation.sol";
import {MockSwapRouter}       from "../src/MockSwapRouter.sol";
import {MockOracle}           from "../src/MockOracle.sol";
import {IWeaveRegistry}       from "../src/interfaces/IWeaveRegistry.sol";

/// @notice Full deployment sequence for Weave on Robinhood Chain testnet.
/// Run with: make deploy
/// Verify each contract with: make verify ADDR=0x... NAME=src/Foo.sol:Foo
contract DeployWeave is Script {

    // Source: https://docs.robinhood.com/chain/contracts
    address constant USDG = 0x7E955252E15c84f5768B83c41a71F9eba181802F;
    address constant TSLA = 0xC9f9c86933092BbbfFF3CCb4b105A4A94bf3Bd4E;
    address constant AMZN = 0x5884aD2f920c162CFBbACc88C9C51AA75eC09E02;
    address constant PLTR = 0x1FBE1a0e43594b3455993B5dE5Fd0A7A266298d0;
    address constant NFLX = 0x3b8262A63d25f0477c4DDE23F83cfe22Cb768C93;
    address constant AMD  = 0x71178BAc73cBeb415514eB542a8995b82669778d;

    // ── Initial oracle prices (8-decimal USD, matching Chainlink convention) ──
    // These are approximate prices at time of writing — update before deployment.

    int256 constant TSLA_PRICE = 34200_00000000;   // $342.00
    int256 constant AMZN_PRICE = 20500_00000000;   // $205.00
    int256 constant PLTR_PRICE = 12800000000;       // $128.00
    int256 constant NFLX_PRICE = 123000000000;      // $1230.00 — note: adjust to current
    int256 constant AMD_PRICE  = 11000000000;       // $110.00

    function run() external {
        uint256 deployerKey = vm.envUint("PRIVATE_KEY");
        address deployer    = vm.addr(deployerKey);

        vm.startBroadcast(deployerKey);

        WeaveRegistry registry = new WeaveRegistry(
            deployer,           // governance — EOA for hackathon, multisig in production
            USDG,
            deployer,           // protocolTreasury — same as deployer for now
            50,                 // managementFeeBps: 0.5%
            2_000,              // protocolShareBps: 20% of fee to protocol
            100_000_000,        // minAUMForAutomation: $100 USDG (6 dec)
            86_400,             // oracleStalenessSecs: 24 hours — MockOracle always fresh
            10_000_000,         // minFirstDepositUsdg: $10 USDG (6 dec)
            20,                 // maxConstituents
            100                 // minWeightBps: 1%
        );
        console.log("WeaveRegistry:       ", address(registry));

        // Real Chainlink feed addresses for Robinhood Chain testnet are not yet
        // published. MockOracles implement IWeaveOracle and can be swapped for
        // real feeds via registry.addAsset() once Chainlink publishes addresses.

        MockOracle oracleTSLA = new MockOracle("TSLA / USD", TSLA_PRICE);
        MockOracle oracleAMZN = new MockOracle("AMZN / USD", AMZN_PRICE);
        MockOracle oraclePLTR = new MockOracle("PLTR / USD", PLTR_PRICE);
        MockOracle oracleNFLX = new MockOracle("NFLX / USD", NFLX_PRICE);
        MockOracle oracleAMD  = new MockOracle("AMD / USD",  AMD_PRICE);

        console.log("MockOracle TSLA:     ", address(oracleTSLA));
        console.log("MockOracle AMZN:     ", address(oracleAMZN));
        console.log("MockOracle PLTR:     ", address(oraclePLTR));
        console.log("MockOracle NFLX:     ", address(oracleNFLX));
        console.log("MockOracle AMD:      ", address(oracleAMD));

        MockSwapRouter swapRouter = new MockSwapRouter(address(registry));
        console.log("MockSwapRouter:      ", address(swapRouter));

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
        console.log("make verify ADDR=%s NAME=src/WeaveRegistry.sol:WeaveRegistry",        address(registry));
        console.log("make verify ADDR=%s NAME=src/MockSwapRouter.sol:MockSwapRouter",      address(swapRouter));
        console.log("make verify ADDR=%s NAME=src/BasketImplementation.sol:BasketImplementation", address(implementation));
        console.log("make verify ADDR=%s NAME=src/BasketFactory.sol:BasketFactory",        address(factory));
        console.log("make verify ADDR=%s NAME=src/WeaveAutomation.sol:WeaveAutomation",    address(automation));
        console.log("make verify ADDR=%s NAME=src/MockOracle.sol:MockOracle (TSLA)",       address(oracleTSLA));
    }
}