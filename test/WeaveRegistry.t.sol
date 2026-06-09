// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test, console} from "forge-std/Test.sol";
import {WeaveRegistry}  from "../src/WeaveRegistry.sol";
import {OracleAdapter}  from "../src/OracleAdapter.sol";
import {IWeaveRegistry} from "../src/interfaces/IWeaveRegistry.sol";

contract WeaveRegistryTest is Test {

    WeaveRegistry registry;
    OracleAdapter oracle;

    address governance = makeAddr("governance");
    address treasury   = makeAddr("treasury");
    address usdg       = makeAddr("usdg");
    address token      = makeAddr("token");
    address factory    = makeAddr("factory");
    address notGov     = makeAddr("notGov");

    function setUp() public {
        registry = new WeaveRegistry(
            governance,
            usdg,
            treasury,
            50,
            2_000,
            100_000_000,
            86_400,
            10_000_000,
            20,
            100,
            1_000_000,
            100
        );
    
        oracle = new OracleAdapter("TEST / USD", 100_00000000);
    }

    function test_constructorSetsParams() public view {
        assertEq(registry.governance(),                governance);
        assertEq(registry.usdg(),                     usdg);
        assertEq(registry.protocolTreasury(),          treasury);
        assertEq(registry.managementFeeBps(),          50);
        assertEq(registry.protocolShareBps(),          2_000);
        assertEq(registry.creatorShareBps(),           8_000);
        assertEq(registry.maxConstituents(),           20);
        assertEq(registry.minWeightBps(),              100);
        assertEq(registry.minRebalanceTradeSizeUsdg(), 1_000_000);
        assertEq(registry.maxSwapSlippageBps(),        100);
        assertFalse(registry.paused());
    }

    function test_revertConstructor_zeroGovernance() public {
        vm.expectRevert(WeaveRegistry.ZeroAddress.selector);
        new WeaveRegistry(address(0), usdg, treasury, 50, 2_000, 1e8, 86400, 1e7, 20, 100, 1e6, 100);
    }

    function test_revertConstructor_feeTooHigh() public {
        vm.expectRevert(WeaveRegistry.InvalidFeeBps.selector);
        new WeaveRegistry(governance, usdg, treasury, 1_001, 2_000, 1e8, 86400, 1e7, 20, 100, 1e6, 100);
    }

    function test_pauseAll() public {
        assertFalse(registry.paused());
        vm.prank(governance);
        registry.pauseAll();
        assertTrue(registry.paused());
    }

    function test_unpauseAll() public {
        vm.startPrank(governance);
        registry.pauseAll();
        registry.unpauseAll();
        vm.stopPrank();
        assertFalse(registry.paused());
    }

    function test_revertPauseAll_notGovernance() public {
        vm.prank(notGov);
        vm.expectRevert(WeaveRegistry.NotGovernance.selector);
        registry.pauseAll();
    }

    function test_addAsset() public {
        vm.prank(governance);
        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: token,
            oracle:       address(oracle),
            symbol:       "TEST",
            name:         "Test Token",
            sector:       "Technology",
            active:       true
        }));

        IWeaveRegistry.AssetConfig memory cfg = registry.assets(token);
        assertEq(cfg.tokenAddress, token);
        assertEq(cfg.symbol,       "TEST");
        assertTrue(cfg.active);
    }

    function test_revertAddAsset_notGovernance() public {
        vm.prank(notGov);
        vm.expectRevert(WeaveRegistry.NotGovernance.selector);
        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: token,
            oracle:       address(oracle),
            symbol:       "TEST",
            name:         "Test Token",
            sector:       "Technology",
            active:       true
        }));
    }

    function test_revertAddAsset_duplicate() public {
        vm.startPrank(governance);
        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: token,
            oracle:       address(oracle),
            symbol:       "TEST",
            name:         "Test Token",
            sector:       "Technology",
            active:       true
        }));

        vm.expectRevert(abi.encodeWithSelector(WeaveRegistry.AssetAlreadyExists.selector, token));
        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: token,
            oracle:       address(oracle),
            symbol:       "TEST",
            name:         "Test Token",
            sector:       "Technology",
            active:       true
        }));
        vm.stopPrank();
    }

    function test_deactivateAsset() public {
        vm.startPrank(governance);
        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: token,
            oracle:       address(oracle),
            symbol:       "TEST",
            name:         "Test",
            sector:       "Tech",
            active:       true
        }));
        registry.deactivateAsset(token);
        vm.stopPrank();

        assertFalse(registry.assets(token).active);
    }

    function test_reactivateAsset() public {
        vm.startPrank(governance);
        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: token,
            oracle:       address(oracle),
            symbol:       "TEST",
            name:         "Test",
            sector:       "Tech",
            active:       true
        }));
        registry.deactivateAsset(token);
        assertFalse(registry.assets(token).active);
    
        registry.reactivateAsset(token);
        assertTrue(registry.assets(token).active);
        vm.stopPrank();
    }
    
    function test_reactivateAsset_NotFound_Reverts() public {
        vm.prank(governance);
        vm.expectRevert(abi.encodeWithSelector(WeaveRegistry.AssetNotFound.selector, address(0xdead)));
        registry.reactivateAsset(address(0xdead));
    }

    function test_getSupportedAssets() public {
        address token2  = makeAddr("token2");
        OracleAdapter oracle2 = new OracleAdapter("TEST2 / USD", 200_00000000);

        vm.startPrank(governance);
        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: token,
            oracle:       address(oracle),
            symbol:       "TEST",
            name:         "Test",
            sector:       "Tech",
            active:       true
        }));
        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: token2,
            oracle:       address(oracle2),
            symbol:       "TEST2",
            name:         "Test2",
            sector:       "Finance",
            active:       true
        }));
        vm.stopPrank();

        IWeaveRegistry.AssetConfig[] memory assets = registry.getSupportedAssets();
        assertEq(assets.length, 2);
        assertEq(assets[0].symbol, "TEST");
        assertEq(assets[1].symbol, "TEST2");
    }

    function test_getAssetPrice() public {
        vm.prank(governance);
        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: token,
            oracle:       address(oracle),
            symbol:       "TEST",
            name:         "Test",
            sector:       "Tech",
            active:       true
        }));

        (uint256 price, uint256 updatedAt) = registry.getAssetPrice(token);
        assertEq(price, 100_00000000);
        assertGt(updatedAt, 0);
    }

    function test_revertGetAssetPrice_stale() public {
        vm.prank(governance);
        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: token,
            oracle:       address(oracle),
            symbol:       "TEST",
            name:         "Test",
            sector:       "Tech",
            active:       true
        }));

        vm.warp(block.timestamp + 86_401);

        vm.expectRevert(abi.encodeWithSelector(WeaveRegistry.StalePrice.selector, token));
        registry.getAssetPrice(token);
    }

    function test_revertGetAssetPrice_deactivated() public {
        vm.startPrank(governance);
        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: token,
            oracle:       address(oracle),
            symbol:       "TEST",
            name:         "Test",
            sector:       "Tech",
            active:       true
        }));
        registry.deactivateAsset(token);
        vm.stopPrank();

        vm.expectRevert(abi.encodeWithSelector(WeaveRegistry.AssetNotActive.selector, token));
        registry.getAssetPrice(token);
    }

    function test_registerBasket() public {
        address basket       = makeAddr("basket");
        address creatorToken = makeAddr("creatorToken");
        address creator      = makeAddr("creator");

        vm.prank(governance);
        registry.setBasketFactory(factory);

        vm.prank(factory);
        registry.registerBasket(basket, creatorToken, creator);

        assertTrue(registry.isBasket(basket));
        IWeaveRegistry.BasketMeta memory meta = registry.basketMeta(basket);
        assertEq(meta.creator,      creator);
        assertEq(meta.creatorToken, creatorToken);
        assertTrue(meta.active);
    }

    function test_revertRegisterBasket_notFactory() public {
        vm.prank(governance);
        registry.setBasketFactory(factory);

        vm.prank(notGov);
        vm.expectRevert(WeaveRegistry.NotFactory.selector);
        registry.registerBasket(makeAddr("b"), makeAddr("ct"), makeAddr("c"));
    }

    function test_revertRegisterBasket_duplicate() public {
        address basket = makeAddr("basket");

        vm.prank(governance);
        registry.setBasketFactory(factory);

        vm.startPrank(factory);
        registry.registerBasket(basket, makeAddr("ct"), makeAddr("c"));

        vm.expectRevert(abi.encodeWithSelector(WeaveRegistry.BasketAlreadyRegistered.selector, basket));
        registry.registerBasket(basket, makeAddr("ct2"), makeAddr("c2"));
        vm.stopPrank();
    }

    function test_twoStepGovernanceTransfer() public {
        address newGov = makeAddr("newGov");

        vm.prank(governance);
        registry.nominateGovernance(newGov);
        assertEq(registry.pendingGovernance(), newGov);

        vm.prank(newGov);
        registry.acceptGovernance();
        assertEq(registry.governance(),        newGov);
        assertEq(registry.pendingGovernance(), address(0));
    }

    function test_revertAcceptGovernance_notNominee() public {
        address newGov = makeAddr("newGov");

        vm.prank(governance);
        registry.nominateGovernance(newGov);

        vm.prank(notGov);
        vm.expectRevert(WeaveRegistry.NotPendingGovernance.selector);
        registry.acceptGovernance();
    }

    function test_feeSplitAlwaysSumsTo10000() public {
        vm.prank(governance);
        registry.setFeeSplit(3_000);

        assertEq(registry.protocolShareBps(), 3_000);
        assertEq(registry.creatorShareBps(),  7_000);
        assertEq(registry.protocolShareBps() + registry.creatorShareBps(), 10_000);
    }

    function test_setMinRebalanceTradeSize() public {
        vm.prank(governance);
        registry.setMinRebalanceTradeSize(5_000_000);
        assertEq(registry.minRebalanceTradeSizeUsdg(), 5_000_000);
    }

    function test_setMaxSwapSlippage() public {
        vm.prank(governance);
        registry.setMaxSwapSlippage(200);
        assertEq(registry.maxSwapSlippageBps(), 200);
    }

    function testFuzz_managementFeeCap(uint256 feeBps) public {
        feeBps = bound(feeBps, 0, 1_000);
        vm.prank(governance);
        registry.setManagementFee(feeBps);
        assertEq(registry.managementFeeBps(), feeBps);
    }

    function testFuzz_managementFeeReverts(uint256 feeBps) public {
        feeBps = bound(feeBps, 1_001, type(uint256).max);
        vm.prank(governance);
        vm.expectRevert(WeaveRegistry.InvalidFeeBps.selector);
        registry.setManagementFee(feeBps);
    }

    function testFuzz_feeSplitInvariant(uint256 protocolBps) public {
        protocolBps = bound(protocolBps, 0, 10_000);
        vm.prank(governance);
        registry.setFeeSplit(protocolBps);
        assertEq(registry.protocolShareBps() + registry.creatorShareBps(), 10_000);
    }

    function testFuzz_oracleStalenessWindow(uint256 warpSecs) public {
        vm.prank(governance);
        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: token,
            oracle:       address(oracle),
            symbol:       "TEST",
            name:         "Test",
            sector:       "Tech",
            active:       true
        }));

        warpSecs = bound(warpSecs, 0, 86_400);
        vm.warp(block.timestamp + warpSecs);

        (uint256 price,) = registry.getAssetPrice(token);
        assertGt(price, 0);
    }
}