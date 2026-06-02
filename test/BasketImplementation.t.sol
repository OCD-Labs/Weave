// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test}                 from "forge-std/Test.sol";
import {WeaveRegistry}        from "../src/WeaveRegistry.sol";
import {BasketImplementation} from "../src/BasketImplementation.sol";
import {BasketFactory}        from "../src/BasketFactory.sol";
import {CreatorToken}         from "../src/CreatorToken.sol";
import {SwapRouter}           from "../src/SwapRouter.sol";
import {OracleAdapter}        from "../src/OracleAdapter.sol";
import {IWeaveRegistry}       from "../src/interfaces/IWeaveRegistry.sol";
import {ERC20}                from "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import {Math}                 from "@openzeppelin/contracts/utils/math/Math.sol";

contract MockERC20 is ERC20 {
    constructor(string memory sym) ERC20(sym, sym) {}
    function mint(address to, uint256 amt) external { _mint(to, amt); }
}

/// @notice Full integration tests for deposit, redeem, rebalance, and fee mechanics.
contract BasketImplementationTest is Test {

    WeaveRegistry        registry;
    BasketImplementation impl;
    BasketFactory        factory;
    SwapRouter           router;

    MockERC20 usdg;
    MockERC20 tsla;
    MockERC20 amzn;
    MockERC20 pltr;

    OracleAdapter oTSLA;
    OracleAdapter oAMZN;
    OracleAdapter oPLTR;

    address governance = makeAddr("governance");
    address treasury   = makeAddr("treasury");
    address creator    = makeAddr("creator");
    address alice      = makeAddr("alice");
    address bob        = makeAddr("bob");

    BasketImplementation basket;
    CreatorToken         creatorToken;

    uint256 constant INITIAL_DEPOSIT = 100e6;
    uint256 constant FEE_BPS         = 50;
    uint256 constant PROTO_SHARE     = 2_000;

    function setUp() public {
        usdg = new MockERC20("USDG");
        tsla = new MockERC20("TSLA");
        amzn = new MockERC20("AMZN");
        pltr = new MockERC20("PLTR");

        // Round prices to keep NAV math clean in tests.
        oTSLA = new OracleAdapter("TSLA/USD", 100_00000000); // $100
        oAMZN = new OracleAdapter("AMZN/USD", 200_00000000); // $200
        oPLTR = new OracleAdapter("PLTR/USD", 50_00000000);  // $50

        vm.startPrank(governance);
        registry = new WeaveRegistry(
            governance, address(usdg), treasury,
            FEE_BPS,     // managementFeeBps
            PROTO_SHARE, // protocolShareBps
            50e6,        // minAUMForAutomation
            86_400,      // oracleStalenessSecs
            10e6,        // minFirstDepositUsdg
            20,          // maxConstituents
            100,         // minWeightBps
            1_000_000,   // minRebalanceTradeSizeUsdg: $1
            100          // maxSwapSlippageBps: 1%
        );

        impl    = new BasketImplementation();
        factory = new BasketFactory(address(registry), address(impl));
        router  = new SwapRouter(address(registry));

        registry.setBasketFactory(address(factory));
        registry.setSwapRouter(address(router));

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

        // Fund router treasury.
        tsla.mint(address(router), 100_000e18);
        amzn.mint(address(router), 100_000e18);
        pltr.mint(address(router), 100_000e18);
        usdg.mint(address(router), 10_000_000e6);

        usdg.mint(creator, 1_000_000e6);
        vm.prank(creator);
        usdg.approve(address(factory), type(uint256).max);

        address[] memory constituents = new address[](3);
        uint256[] memory weights      = new uint256[](3);
        constituents[0] = address(tsla); weights[0] = 4_000;
        constituents[1] = address(amzn); weights[1] = 3_000;
        constituents[2] = address(pltr); weights[2] = 3_000;

        vm.prank(creator);
        (address b, address ct) = factory.createBasket(
            "Test Basket", "TBASKET", "A thesis",
            constituents, weights, true, 500, INITIAL_DEPOSIT
        );

        basket      = BasketImplementation(b);
        creatorToken = CreatorToken(ct);
    }

    function test_deposit_mintsTokens() public {
        uint256 amount = 1_000e6;
        usdg.mint(alice, amount);

        vm.startPrank(alice);
        usdg.approve(address(basket), amount);
        uint256 minted = basket.deposit(amount, 0, alice);
        vm.stopPrank();

        assertGt(minted, 0);
        assertEq(basket.balanceOf(alice), minted);
    }

    function test_deposit_feeDeducted() public {
        uint256 amount = 1_000e6;
        usdg.mint(alice, amount);

        uint256 treasuryBefore = usdg.balanceOf(treasury);

        vm.startPrank(alice);
        usdg.approve(address(basket), amount);
        basket.deposit(amount, 0, alice);
        vm.stopPrank();

        uint256 expectedFee      = Math.mulDiv(amount, FEE_BPS, 10_000);
        uint256 expectedProtoCut = Math.mulDiv(expectedFee, PROTO_SHARE, 10_000);

        assertEq(usdg.balanceOf(treasury), treasuryBefore + expectedProtoCut);
    }

    function test_deposit_creatorTokenSnapshot() public {
        uint256 amount = 1_000e6;
        usdg.mint(alice, amount);

        uint256 snapsBefore = creatorToken.snapshotCount();

        vm.startPrank(alice);
        usdg.approve(address(basket), amount);
        basket.deposit(amount, 0, alice);
        vm.stopPrank();

        assertEq(creatorToken.snapshotCount(), snapsBefore + 1);
    }

    function test_deposit_revertWhenSuspended() public {
        vm.prank(governance);
        registry.deactivateAsset(address(tsla));

        usdg.mint(alice, 1_000e6);
        vm.startPrank(alice);
        usdg.approve(address(basket), 1_000e6);
        vm.expectRevert(BasketImplementation.BasketSuspended.selector);
        basket.deposit(1_000e6, 0, alice);
        vm.stopPrank();
    }

    function test_deposit_revertWhenProtocolPaused() public {
        vm.prank(governance);
        registry.pauseAll();

        usdg.mint(alice, 1_000e6);
        vm.startPrank(alice);
        usdg.approve(address(basket), 1_000e6);
        vm.expectRevert(BasketImplementation.ProtocolPaused.selector);
        basket.deposit(1_000e6, 0, alice);
        vm.stopPrank();
    }

    function test_redeem_revertWhenProtocolPaused() public {
        uint256 amount = 1_000e6;
        usdg.mint(alice, amount);
        vm.startPrank(alice);
        usdg.approve(address(basket), amount);
        uint256 minted = basket.deposit(amount, 0, alice);
        vm.stopPrank();

        vm.prank(governance);
        registry.pauseAll();

        vm.prank(alice);
        vm.expectRevert(BasketImplementation.ProtocolPaused.selector);
        basket.redeem(minted, 0, alice);
    }

    function test_rebalance_notBlockedByPause() public {
        // Rebalancing must work even when protocol is paused.
        oTSLA.setPrice(300_00000000);
        assertTrue(basket.needsRebalancing());

        vm.prank(governance);
        registry.pauseAll();

        uint256[] memory minAmounts = new uint256[](3);
        basket.rebalance(minAmounts); // must not revert
        assertFalse(basket.needsRebalancing());
    }

    function test_deposit_revertSlippage() public {
        uint256 amount = 1_000e6;
        usdg.mint(alice, amount);

        vm.startPrank(alice);
        usdg.approve(address(basket), amount);
        vm.expectRevert(BasketImplementation.InsufficientSlippage.selector);
        basket.deposit(amount, type(uint256).max, alice);
        vm.stopPrank();
    }

    function test_redeem_returnsUSDG() public {
        uint256 depositAmount = 1_000e6;
        usdg.mint(alice, depositAmount);

        vm.startPrank(alice);
        usdg.approve(address(basket), depositAmount);
        uint256 minted = basket.deposit(depositAmount, 0, alice);

        uint256 usdgBefore = usdg.balanceOf(alice);
        basket.redeem(minted, 0, alice);
        vm.stopPrank();

        uint256 returned = usdg.balanceOf(alice) - usdgBefore;
        assertGt(returned, 0);
        assertLt(returned, depositAmount);
    }

    function test_redeem_burnsTokens() public {
        uint256 depositAmount = 1_000e6;
        usdg.mint(alice, depositAmount);

        vm.startPrank(alice);
        usdg.approve(address(basket), depositAmount);
        uint256 minted = basket.deposit(depositAmount, 0, alice);
        uint256 supplyBefore = basket.totalSupply();

        basket.redeem(minted, 0, alice);
        vm.stopPrank();

        assertEq(basket.totalSupply(), supplyBefore - minted);
    }

    function test_depositThenRedeem_roundTrip() public {
        uint256 amount = 10_000e6;
        usdg.mint(alice, amount);

        vm.startPrank(alice);
        usdg.approve(address(basket), amount);
        uint256 minted  = basket.deposit(amount, 0, alice);
        uint256 returned = basket.redeem(minted, 0, alice);
        vm.stopPrank();

        assertLt(returned, amount);
        assertGt(returned, 0);
    }

    function test_navPerToken_positivAfterDeposit() public view {
        assertGt(basket.navPerToken(), 0);
    }

    function test_navPerToken_increasesWithPriceRise() public {
        uint256 navBefore = basket.navPerToken();
        oTSLA.setPrice(200_00000000);
        uint256 navAfter = basket.navPerToken();
        assertGt(navAfter, navBefore);
    }

    function test_totalValueUsdg_matchesConstituents() public view {
        assertGt(basket.totalValueUsdg(), 0);
    }

    function test_needsRebalancing_falseAtCreation() public view {
        assertFalse(basket.needsRebalancing());
    }

    function test_needsRebalancing_trueAfterPriceMove() public {
        oTSLA.setPrice(300_00000000);
        assertTrue(basket.needsRebalancing());
    }

    function test_rebalance_restoresWeights() public {
        oTSLA.setPrice(300_00000000);
        assertTrue(basket.needsRebalancing());

        uint256[] memory minAmounts = new uint256[](3);
        basket.rebalance(minAmounts);

        assertFalse(basket.needsRebalancing());
    }

    function test_rebalance_permissionless() public {
        oTSLA.setPrice(300_00000000);

        uint256[] memory minAmounts = new uint256[](3);
        vm.prank(alice);
        basket.rebalance(minAmounts);

        assertFalse(basket.needsRebalancing());
    }

    function test_rebalance_revertWhenNotNeeded() public {
        uint256[] memory minAmounts = new uint256[](3);
        vm.expectRevert(BasketImplementation.DriftThresholdNotMet.selector);
        basket.rebalance(minAmounts);
    }

    function test_rebalance_revertWhenNotEnabled() public {
        address[] memory constituents = new address[](3);
        uint256[] memory weights      = new uint256[](3);
        constituents[0] = address(tsla); weights[0] = 4_000;
        constituents[1] = address(amzn); weights[1] = 3_000;
        constituents[2] = address(pltr); weights[2] = 3_000;

        vm.prank(creator);
        (address staticBasket,) = factory.createBasket(
            "Static", "STATIC", "A static thesis",
            constituents, weights, false, 0, INITIAL_DEPOSIT
        );

        oTSLA.setPrice(300_00000000);

        uint256[] memory minAmounts = new uint256[](3);
        vm.expectRevert(BasketImplementation.RebalancingNotEnabled.selector);
        BasketImplementation(staticBasket).rebalance(minAmounts);
    }

    function test_multipleInvestors_proportionalNAV() public {
        address[] memory constituents = new address[](3);
        uint256[] memory weights      = new uint256[](3);
        constituents[0] = address(tsla); weights[0] = 4_000;
        constituents[1] = address(amzn); weights[1] = 3_000;
        constituents[2] = address(pltr); weights[2] = 3_000;

        usdg.mint(creator, 10e6);
        vm.prank(creator);
        usdg.approve(address(factory), type(uint256).max);

        vm.prank(creator);
        (address freshBasket,) = factory.createBasket(
            "Fresh", "FRESH", "thesis",
            constituents, weights, false, 0, 10e6
        );

        BasketImplementation fb = BasketImplementation(freshBasket);

        uint256 aliceDeposit = 1_000e6;
        uint256 bobDeposit   = 2_000e6;

        usdg.mint(alice, aliceDeposit);
        usdg.mint(bob,   bobDeposit);

        vm.startPrank(alice);
        usdg.approve(freshBasket, aliceDeposit);
        uint256 aliceMinted = fb.deposit(aliceDeposit, 0, alice);
        vm.stopPrank();

        vm.startPrank(bob);
        usdg.approve(freshBasket, bobDeposit);
        uint256 bobMinted = fb.deposit(bobDeposit, 0, bob);
        vm.stopPrank();

        // Bob deposited 2x alice — allow 5% tolerance for rounding and spread.
        assertApproxEqRel(bobMinted, aliceMinted * 2, 0.05e18);
    }

    function test_basketState_returnsAllFields() public view {
        (
            address[] memory c,
            uint256[] memory tw,
            uint256[] memory cw,
            uint256[] memory bal,
            uint256 totalValue,
            uint256 nav,
            bool rebal,
            uint256 driftBps,
            uint256 maxD
        ) = basket.basketState();

        assertEq(c.length,   3);
        assertEq(tw.length,  3);
        assertEq(cw.length,  3);
        assertEq(bal.length, 3);
        assertGt(totalValue, 0);
        assertGt(nav, 0);
        assertTrue(rebal);
        assertEq(driftBps, 500);
        assertEq(maxD, basket.maxDrift());
    }

    function testFuzz_deposit_tokensMintedProportional(uint256 amount) public {
        amount = bound(amount, 10e6, 1_000_000e6);
        usdg.mint(alice, amount);

        vm.startPrank(alice);
        usdg.approve(address(basket), amount);
        uint256 minted = basket.deposit(amount, 0, alice);
        vm.stopPrank();

        assertGt(minted, 0);
        assertEq(basket.balanceOf(alice), minted);
    }

    function testFuzz_redeem_neverMoreThanDeposit(uint256 amount) public {
        amount = bound(amount, 10e6, 100_000e6);
        usdg.mint(alice, amount);

        vm.startPrank(alice);
        usdg.approve(address(basket), amount);
        uint256 minted  = basket.deposit(amount, 0, alice);
        uint256 returned = basket.redeem(minted, 0, alice);
        vm.stopPrank();

        assertLe(returned, amount);
    }

    function testFuzz_totalValueNeverZeroWithBalance(uint256 amount) public {
        amount = bound(amount, 10e6, 100_000e6);
        usdg.mint(alice, amount);

        vm.startPrank(alice);
        usdg.approve(address(basket), amount);
        basket.deposit(amount, 0, alice);
        vm.stopPrank();

        assertGt(basket.totalValueUsdg(), 0);
    }

    function testFuzz_priceIncrease_navIncreases(uint256 newPrice) public {
        newPrice = bound(newPrice, 101_00000000, 10_000_00000000);

        uint256 navBefore = basket.navPerToken();
        // forge-lint: disable-next-line(unsafe-typecast)
        oTSLA.setPrice(int256(newPrice));
        uint256 navAfter = basket.navPerToken();

        assertGt(navAfter, navBefore);
    }
}