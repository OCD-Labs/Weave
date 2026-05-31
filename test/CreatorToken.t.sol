// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test} from "forge-std/Test.sol";
import {CreatorToken} from "../src/CreatorToken.sol";
import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";

/// @notice Minimal ERC-20 mock for USDG in creator token tests.
contract MockUSDG is ERC20 {
    constructor() ERC20("USD G", "USDG") {}

    function mint(address to, uint256 amount) external {
        _mint(to, amount);
    }
}

contract CreatorTokenTest is Test {
    CreatorToken token;
    MockUSDG usdg;

    address basket = makeAddr("basket");
    address creator = makeAddr("creator");
    address alice = makeAddr("alice");
    address bob = makeAddr("bob");

    uint256 constant TOTAL_SUPPLY = 1_000_000 * 1e18;

    function setUp() public {
        usdg = new MockUSDG();
        token = new CreatorToken(
            basket,
            address(usdg),
            creator,
            "Weave Creator: Test",
            "wCT-TEST"
        );
    }

    function test_initialSupplyMintedToCreator() public view {
        assertEq(token.totalSupply(), TOTAL_SUPPLY);
        assertEq(token.balanceOf(creator), TOTAL_SUPPLY);
        assertEq(token.basket(), basket);
        assertEq(token.usdg(), address(usdg));
    }

    function test_snapshotRevenue() public {
        uint256 amount = 1_000e6; // $1000 USDG
        usdg.mint(basket, amount);

        vm.startPrank(basket);
        usdg.approve(address(token), amount);
        token.snapshotRevenue(amount);
        vm.stopPrank();

        assertEq(token.snapshotCount(), 1);
        assertEq(usdg.balanceOf(address(token)), amount);
    }

    function test_revertSnapshotRevenue_notBasket() public {
        usdg.mint(alice, 1_000e6);
        vm.startPrank(alice);
        usdg.approve(address(token), 1_000e6);
        vm.expectRevert(CreatorToken.OnlyBasket.selector);
        token.snapshotRevenue(1_000e6);
        vm.stopPrank();
    }

    function test_revertSnapshotRevenue_zeroAmount() public {
        vm.prank(basket);
        vm.expectRevert(CreatorToken.ZeroAmount.selector);
        token.snapshotRevenue(0);
    }

    function test_claimableRevenue_fullSupplyHolder() public {
        uint256 amount = 1_000e6;
        usdg.mint(basket, amount);

        vm.startPrank(basket);
        usdg.approve(address(token), amount);
        token.snapshotRevenue(amount);
        vm.stopPrank();

        // Creator holds 100% of supply so claimable = full amount.
        uint256 claimable = token.claimableRevenue(creator, 1);
        assertEq(claimable, amount);
    }

    function test_claimableRevenue_splitHolders() public {
        // Creator transfers half to alice.
        vm.prank(creator);
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        token.transfer(alice, TOTAL_SUPPLY / 2);

        uint256 amount = 1_000e6;
        usdg.mint(basket, amount);

        vm.startPrank(basket);
        usdg.approve(address(token), amount);
        token.snapshotRevenue(amount);
        vm.stopPrank();

        uint256 creatorClaimable = token.claimableRevenue(creator, 1);
        uint256 aliceClaimable = token.claimableRevenue(alice, 1);

        // Each holds 50% so each gets half. Allow 1 wei rounding.
        assertApproxEqAbs(creatorClaimable, 500e6, 1);
        assertApproxEqAbs(aliceClaimable, 500e6, 1);
        // Total claimed never exceeds snapshot amount.
        assertLe(creatorClaimable + aliceClaimable, amount);
    }

    function test_claim() public {
        uint256 amount = 1_000e6;
        usdg.mint(basket, amount);

        vm.startPrank(basket);
        usdg.approve(address(token), amount);
        token.snapshotRevenue(amount);
        vm.stopPrank();

        uint256 before = usdg.balanceOf(creator);

        vm.prank(creator);
        token.claim(1);

        assertEq(usdg.balanceOf(creator), before + amount);
    }

    function test_revertClaim_alreadyClaimed() public {
        uint256 amount = 1_000e6;
        usdg.mint(basket, amount);

        vm.startPrank(basket);
        usdg.approve(address(token), amount);
        token.snapshotRevenue(amount);
        vm.stopPrank();

        vm.startPrank(creator);
        token.claim(1);

        vm.expectRevert(
            abi.encodeWithSelector(
                CreatorToken.AlreadyClaimed.selector,
                creator,
                1
            )
        );
        token.claim(1);
        vm.stopPrank();
    }

    function test_revertClaim_nonExistentSnapshot() public {
        vm.prank(creator);
        vm.expectRevert(
            abi.encodeWithSelector(
                CreatorToken.SnapshotDoesNotExist.selector,
                99
            )
        );
        token.claim(99);
    }

    function test_claimAll_multipleSnapshots() public {
        uint256 perSnap = 500e6;

        for (uint256 i = 0; i < 3; ++i) {
            usdg.mint(basket, perSnap);
            vm.startPrank(basket);
            usdg.approve(address(token), perSnap);
            token.snapshotRevenue(perSnap);
            vm.stopPrank();
        }

        assertEq(token.snapshotCount(), 3);

        uint256 before = usdg.balanceOf(creator);

        vm.prank(creator);
        token.claimAll();

        // Creator held 100% through all 3 snapshots so gets all 1500e6.
        assertEq(usdg.balanceOf(creator), before + perSnap * 3);
    }

    function test_revertClaimAll_nothingToClaim() public {
        // No snapshots yet.
        vm.prank(creator);
        vm.expectRevert(CreatorToken.NothingToClaim.selector);
        token.claimAll();
    }

    function test_transferAfterSnapshot_doesNotAffectPastClaim() public {
        uint256 amount = 1_000e6;
        usdg.mint(basket, amount);

        vm.startPrank(basket);
        usdg.approve(address(token), amount);
        token.snapshotRevenue(amount); // snapshot 1 at block N: creator holds 100%
        vm.stopPrank();

        // Roll to a new block BEFORE transferring so the snapshot's block
        // is captured with creator's full balance, and the transfer checkpoint
        // is recorded at a later block that the snapshot lookup ignores.
        vm.roll(block.number + 1);

        // Creator transfers all tokens to alice AFTER snapshot 1.
        vm.prank(creator);
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        token.transfer(alice, TOTAL_SUPPLY);

        // Alice should get 0 for snapshot 1 — she held nothing at that block.
        assertEq(token.claimableRevenue(alice, 1), 0);

        // Creator still gets full amount for snapshot 1.
        assertEq(token.claimableRevenue(creator, 1), amount);
    }

    function test_newHolder_earnsFromSnapshotAfterTransfer() public {
        // Creator transfers half to alice first.
        vm.prank(creator);
        // forge-lint: disable-next-line(erc20-unchecked-transfer)
        token.transfer(alice, TOTAL_SUPPLY / 2);

        // Roll one block so checkpoint is at a different block than the snapshot.
        vm.roll(block.number + 1);

        uint256 amount = 1_000e6;
        usdg.mint(basket, amount);

        vm.startPrank(basket);
        usdg.approve(address(token), amount);
        token.snapshotRevenue(amount);
        vm.stopPrank();

        // Both hold 50% at snapshot time.
        assertApproxEqAbs(token.claimableRevenue(creator, 1), 500e6, 1);
        assertApproxEqAbs(token.claimableRevenue(alice, 1), 500e6, 1);
    }

    function test_burn_redeemsProportion() public {
        // Fund the revenue pool with some USDG.
        uint256 poolAmount = 1_000e6;
        usdg.mint(basket, poolAmount);

        vm.startPrank(basket);
        usdg.approve(address(token), poolAmount);
        token.snapshotRevenue(poolAmount);
        vm.stopPrank();

        // Creator claims first so pool = 0, then we fund the burn pool directly.
        vm.prank(creator);
        token.claim(1);

        // Fund the contract directly to simulate accumulated unclaimed revenue.
        uint256 burnPool = 500e6;
        usdg.mint(address(token), burnPool);

        uint256 burnAmount = TOTAL_SUPPLY / 2;
        uint256 expectedRedeem = token.redeemableOnBurn(burnAmount);

        uint256 before = usdg.balanceOf(creator);

        vm.prank(creator);
        token.burn(burnAmount);

        assertEq(usdg.balanceOf(creator), before + expectedRedeem);
        assertEq(token.totalSupply(), TOTAL_SUPPLY - burnAmount);
    }

    function testFuzz_claimableNeverExceedsSnapshot(
        uint256 transferAmount,
        uint256 revenueAmount
    ) public {
        transferAmount = bound(transferAmount, 0, TOTAL_SUPPLY);
        revenueAmount = bound(revenueAmount, 1, 1_000_000e6);

        if (transferAmount > 0) {
            vm.prank(creator);
            // forge-lint: disable-next-line(erc20-unchecked-transfer)
            token.transfer(alice, transferAmount);
        }

        usdg.mint(basket, revenueAmount);
        vm.startPrank(basket);
        usdg.approve(address(token), revenueAmount);
        token.snapshotRevenue(revenueAmount);
        vm.stopPrank();

        uint256 creatorClaim = token.claimableRevenue(creator, 1);
        uint256 aliceClaim = token.claimableRevenue(alice, 1);

        // Combined claims must never exceed the snapshot amount.
        assertLe(creatorClaim + aliceClaim, revenueAmount);
    }

    function testFuzz_multipleSnapshotsNeverOverpay(
        uint256 snapCount,
        uint256 perSnap
    ) public {
        snapCount = bound(snapCount, 1, 10);
        perSnap = bound(perSnap, 1e6, 100_000e6);

        uint256 totalRevenue = 0;
        for (uint256 i = 0; i < snapCount; ++i) {
            usdg.mint(basket, perSnap);
            vm.startPrank(basket);
            usdg.approve(address(token), perSnap);
            token.snapshotRevenue(perSnap);
            vm.stopPrank();
            totalRevenue += perSnap;
        }

        uint256 before = usdg.balanceOf(creator);

        vm.prank(creator);
        token.claimAll();

        // Creator holds 100% throughout so gets exactly totalRevenue. Never more.
        assertEq(usdg.balanceOf(creator) - before, totalRevenue);
    }
}
