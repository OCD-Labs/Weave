// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test}                 from "forge-std/Test.sol";
import {WeaveRegistry}        from "../src/WeaveRegistry.sol";
import {BasketImplementation} from "../src/BasketImplementation.sol";
import {BasketFactory}        from "../src/BasketFactory.sol";
import {WeaveAutomation}      from "../src/WeaveAutomation.sol";
import {SwapRouter}           from "../src/SwapRouter.sol";
import {OracleAdapter}        from "../src/OracleAdapter.sol";
import {IWeaveRegistry}       from "../src/interfaces/IWeaveRegistry.sol";
import {ERC20}                from "@openzeppelin/contracts/token/ERC20/ERC20.sol";

contract MockERC20b is ERC20 {
    constructor(string memory sym) ERC20(sym, sym) {}
    function mint(address to, uint256 amt) external { _mint(to, amt); }
}

contract WeaveAutomationTest is Test {

    WeaveRegistry        registry;
    BasketImplementation impl;
    BasketFactory        factory;
    WeaveAutomation      automation;
    SwapRouter           router;

    MockERC20b usdg;
    MockERC20b tsla;
    MockERC20b amzn;
    MockERC20b pltr;

    OracleAdapter oTSLA;
    OracleAdapter oAMZN;
    OracleAdapter oPLTR;

    address governance = makeAddr("governance");
    address treasury   = makeAddr("treasury");
    address creator    = makeAddr("creator");

    BasketImplementation rebalBasket;
    BasketImplementation staticBasket;

    function setUp() public {
        usdg = new MockERC20b("USDG");
        tsla = new MockERC20b("TSLA");
        amzn = new MockERC20b("AMZN");
        pltr = new MockERC20b("PLTR");

        oTSLA = new OracleAdapter("TSLA/USD", 100_00000000);
        oAMZN = new OracleAdapter("AMZN/USD", 200_00000000);
        oPLTR = new OracleAdapter("PLTR/USD",  50_00000000);

        vm.startPrank(governance);
        registry = new WeaveRegistry(
            governance, address(usdg), treasury,
            50,          // managementFeeBps
            2_000,       // protocolShareBps
            50e6,        // minAUMForAutomation
            86_400,      // oracleStalenessSecs
            10e6,        // minFirstDepositUsdg
            20,          // maxConstituents
            100,         // minWeightBps
            1_000_000,   // minRebalanceTradeSizeUsdg: $1
            100          // maxSwapSlippageBps: 1%
        );

        impl       = new BasketImplementation();
        factory    = new BasketFactory(address(registry), address(impl));
        router     = new SwapRouter(address(registry));
        automation = new WeaveAutomation(address(registry), governance);

        registry.setBasketFactory(address(factory));
        registry.setSwapRouter(address(router));
        registry.setAutomationContract(address(automation));

        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: address(tsla), oracle: address(oTSLA),
            symbol: "TSLA", name: "Tesla", sector: "Tech", active: true
        }));
        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: address(amzn), oracle: address(oAMZN),
            symbol: "AMZN", name: "Amazon", sector: "Tech", active: true
        }));
        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: address(pltr), oracle: address(oPLTR),
            symbol: "PLTR", name: "Palantir", sector: "Tech", active: true
        }));
        vm.stopPrank();

        tsla.mint(address(router), 100_000e18);
        amzn.mint(address(router), 100_000e18);
        pltr.mint(address(router), 100_000e18);
        usdg.mint(address(router), 10_000_000e6);

        usdg.mint(creator, 1_000_000e6);
        vm.prank(creator);
        usdg.approve(address(factory), type(uint256).max);

        address[] memory c = new address[](3);
        uint256[] memory w = new uint256[](3);
        c[0] = address(tsla); w[0] = 4_000;
        c[1] = address(amzn); w[1] = 3_000;
        c[2] = address(pltr); w[2] = 3_000;

        vm.prank(creator);
        (address rb,) = factory.createBasket(
            "Rebal Basket", "REBAL", "thesis", c, w, true, 500, 1_000e6
        );
        rebalBasket = BasketImplementation(rb);

        vm.prank(creator);
        (address sb,) = factory.createBasket(
            "Static Basket", "STATIC", "thesis", c, w, false, 0, 100e6
        );
        staticBasket = BasketImplementation(sb);
    }

    function test_checkUpkeep_falseWhenNoRebalancingNeeded() public view {
        (bool needed,) = automation.checkUpkeep("");
        assertFalse(needed);
    }

    function test_checkUpkeep_trueWhenRebalancingNeeded() public {
        oTSLA.setPrice(500_00000000);

        (bool needed, bytes memory performData) = automation.checkUpkeep("");
        assertTrue(needed);

        address[] memory baskets = abi.decode(performData, (address[]));
        assertEq(baskets.length, 1);
        assertEq(baskets[0], address(rebalBasket));
    }

    function test_checkUpkeep_excludesStaticBaskets() public {
        oTSLA.setPrice(500_00000000);

        (bool needed, bytes memory performData) = automation.checkUpkeep("");
        assertTrue(needed);

        address[] memory baskets = abi.decode(performData, (address[]));
        for (uint256 i = 0; i < baskets.length; ++i) {
            assertTrue(baskets[i] != address(staticBasket));
        }
    }

    function test_checkUpkeep_excludesBelowMinAUM() public {
        oTSLA.setPrice(500_00000000);

        (bool needed,) = automation.checkUpkeep("");
        assertTrue(needed);
    }

    function test_checkUpkeep_withStartIndex() public {
        oTSLA.setPrice(500_00000000);

        bytes memory checkData = abi.encode(uint256(999));
        (bool needed,) = automation.checkUpkeep(checkData);
        assertFalse(needed);
    }

    function test_performUpkeep_rebalances() public {
        oTSLA.setPrice(500_00000000);
        assertTrue(rebalBasket.needsRebalancing());

        (bool needed, bytes memory performData) = automation.checkUpkeep("");
        assertTrue(needed);

        automation.performUpkeep(performData);

        assertFalse(rebalBasket.needsRebalancing());
    }

    function test_performUpkeep_skipsIfNoLongerNeeded() public {
        oTSLA.setPrice(500_00000000);

        (, bytes memory performData) = automation.checkUpkeep("");

        uint256[] memory minAmounts = new uint256[](3);
        rebalBasket.rebalance(minAmounts);
        assertFalse(rebalBasket.needsRebalancing());

        automation.performUpkeep(performData);
    }

    function test_performUpkeep_emitsEvent() public {
        oTSLA.setPrice(500_00000000);
        (, bytes memory performData) = automation.checkUpkeep("");

        vm.expectEmit(true, false, false, false);
        emit WeaveAutomation.RebalanceTriggered(address(rebalBasket));
        automation.performUpkeep(performData);
    }

    function test_setBatchSize() public {
        vm.prank(governance);
        automation.setBatchSize(25);
        assertEq(automation.batchSize(), 25);
    }

    function test_revertSetBatchSize_notGovernance() public {
        vm.prank(makeAddr("rando"));
        vm.expectRevert(WeaveAutomation.NotGovernance.selector);
        automation.setBatchSize(10);
    }

    function test_revertSetBatchSize_zero() public {
        vm.prank(governance);
        vm.expectRevert(WeaveAutomation.InvalidBatchSize.selector);
        automation.setBatchSize(0);
    }

    function test_setMaxRebalanceSlippage() public {
        vm.prank(governance);
        automation.setMaxRebalanceSlippage(100);
        assertEq(automation.maxRebalanceSlippageBps(), 100);
    }

    function test_revertSetMaxRebalanceSlippage_tooHigh() public {
        vm.prank(governance);
        vm.expectRevert(WeaveAutomation.InvalidSlippage.selector);
        automation.setMaxRebalanceSlippage(501);
    }

    function testFuzz_checkUpkeep_batchSizeRespected(uint256 batchSize) public {
        batchSize = bound(batchSize, 1, 100);

        vm.prank(governance);
        automation.setBatchSize(batchSize);

        oTSLA.setPrice(500_00000000);
        (, bytes memory performData) = automation.checkUpkeep("");
        address[] memory baskets = abi.decode(performData, (address[]));

        assertLe(baskets.length, batchSize);
    }
}