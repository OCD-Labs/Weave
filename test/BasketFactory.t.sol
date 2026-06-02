// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {Test}                 from "forge-std/Test.sol";
import {WeaveRegistry}        from "../src/WeaveRegistry.sol";
import {BasketImplementation} from "../src/BasketImplementation.sol";
import {BasketFactory}        from "../src/BasketFactory.sol";
import {SwapRouter}           from "../src/SwapRouter.sol";
import {OracleAdapter}        from "../src/OracleAdapter.sol";
import {IWeaveRegistry}       from "../src/interfaces/IWeaveRegistry.sol";
import {ERC20}                from "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import {IBasketFactory}       from "../src/interfaces/IBasketFactory.sol";

contract MockToken is ERC20 {
    constructor(string memory sym) ERC20(sym, sym) {}
    function mint(address to, uint256 amt) external { _mint(to, amt); }
}

contract BasketFactoryTest is Test {

    WeaveRegistry        registry;
    BasketImplementation impl;
    BasketFactory        factory;
    SwapRouter           router;
    MockToken            usdg;

    address governance = makeAddr("governance");
    address creator    = makeAddr("creator");
    address treasury   = makeAddr("treasury");

    MockToken tsla;
    MockToken amzn;
    MockToken pltr;
    MockToken nflx;
    MockToken amd;

    OracleAdapter oTSLA;
    OracleAdapter oAMZN;
    OracleAdapter oPLTR;
    OracleAdapter oNFLX;
    OracleAdapter oAMD;

    uint256 constant INITIAL_DEPOSIT = 100e6; // $100 USDG

    function setUp() public {
        usdg = new MockToken("USDG");
        tsla = new MockToken("TSLA");
        amzn = new MockToken("AMZN");
        pltr = new MockToken("PLTR");
        nflx = new MockToken("NFLX");
        amd  = new MockToken("AMD");

        // Current market prices — 8-decimal USD
        oTSLA = new OracleAdapter("TSLA/USD", 41555000000);  // $415.55
        oAMZN = new OracleAdapter("AMZN/USD", 26206000000);  // $262.06
        oPLTR = new OracleAdapter("PLTR/USD", 15815000000);  // $158.15
        oNFLX = new OracleAdapter("NFLX/USD", 8585000000);   // $85.85
        oAMD  = new OracleAdapter("AMD/USD",  49701000000);  // $497.01

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

        impl    = new BasketImplementation();
        factory = new BasketFactory(address(registry), address(impl));
        router  = new SwapRouter(address(registry));

        registry.setBasketFactory(address(factory));
        registry.setSwapRouter(address(router));

        _addAsset(address(tsla), address(oTSLA), "TSLA", "Tesla",    "Consumer Discretionary");
        _addAsset(address(amzn), address(oAMZN), "AMZN", "Amazon",   "Consumer Discretionary");
        _addAsset(address(pltr), address(oPLTR), "PLTR", "Palantir", "Technology");
        _addAsset(address(nflx), address(oNFLX), "NFLX", "Netflix",  "Communication Services");
        _addAsset(address(amd),  address(oAMD),  "AMD",  "AMD",      "Technology");
        vm.stopPrank();

        // Fund router treasury so swaps succeed.
        tsla.mint(address(router), 1_000e18);
        amzn.mint(address(router), 1_000e18);
        pltr.mint(address(router), 1_000e18);
        nflx.mint(address(router), 1_000e18);
        amd.mint(address(router),  1_000e18);
        usdg.mint(address(router), 1_000_000e6);

        usdg.mint(creator, 10_000e6);
        vm.prank(creator);
        usdg.approve(address(factory), type(uint256).max);
    }

    function test_createBasket_happyPath() public {
        (address basket, address creatorToken) = _createTestBasket();

        assertTrue(registry.isBasket(basket));
        assertEq(registry.basketMeta(basket).creatorToken, creatorToken);
        assertEq(registry.basketMeta(basket).creator,      creator);

        assertGt(BasketImplementation(basket).balanceOf(creator), 0);
    }

    function test_createBasket_emitsEvent() public {
        address[] memory constituents = _defaultConstituents();
        uint256[] memory weights      = _defaultWeights();

        vm.prank(creator);
        vm.expectEmit(false, false, true, false);
        emit IBasketFactory.BasketCreated(address(0), address(0), creator, "Test Basket", false);

        factory.createBasket(
            "Test Basket", "TBASKET", "A test thesis",
            constituents, weights, false, 0, INITIAL_DEPOSIT
        );
    }

    function test_firstDepositNAV() public {
        (address basket,) = _createTestBasket();

        BasketImplementation b = BasketImplementation(basket);
        uint256 nav = b.navPerToken();

        assertGt(nav, 0);
    }

    function test_revertCreate_tooFewConstituents() public {
        address[] memory c = new address[](2);
        uint256[] memory w = new uint256[](2);
        c[0] = address(tsla); w[0] = 5_000;
        c[1] = address(amzn); w[1] = 5_000;

        vm.prank(creator);
        vm.expectRevert(abi.encodeWithSelector(BasketFactory.TooFewConstituents.selector, 2));
        factory.createBasket("X","X","X", c, w, false, 0, INITIAL_DEPOSIT);
    }

    function test_revertCreate_weightSumInvalid() public {
        address[] memory c = _defaultConstituents();
        uint256[] memory w = _defaultWeights();
        w[0] += 1;

        vm.prank(creator);
        vm.expectRevert(abi.encodeWithSelector(BasketFactory.WeightSumInvalid.selector, 10_001));
        factory.createBasket("X","X","X", c, w, false, 0, INITIAL_DEPOSIT);
    }

    function test_revertCreate_weightTooLow() public {
        address[] memory c = _defaultConstituents();
        uint256[] memory w = _defaultWeights();
        w[0] = 50;
        w[1] += 50;

        vm.prank(creator);
        vm.expectRevert(abi.encodeWithSelector(BasketFactory.WeightTooLow.selector, c[0], 50));
        factory.createBasket("X","X","X", c, w, false, 0, INITIAL_DEPOSIT);
    }

    function test_revertCreate_weightTooHigh() public {
        address[] memory c = new address[](3);
        uint256[] memory w = new uint256[](3);
        c[0] = address(tsla); w[0] = 5_001;
        c[1] = address(amzn); w[1] = 2_500;
        c[2] = address(pltr); w[2] = 2_499;

        vm.prank(creator);
        vm.expectRevert(abi.encodeWithSelector(BasketFactory.WeightTooHigh.selector, c[0], 5_001));
        factory.createBasket("X","X","X", c, w, false, 0, INITIAL_DEPOSIT);
    }

    function test_revertCreate_duplicateConstituent() public {
        address[] memory c = new address[](3);
        uint256[] memory w = new uint256[](3);
        c[0] = address(tsla); w[0] = 4_000;
        c[1] = address(tsla); w[1] = 3_000;
        c[2] = address(amzn); w[2] = 3_000;

        vm.prank(creator);
        vm.expectRevert(abi.encodeWithSelector(BasketFactory.DuplicateConstituent.selector, address(tsla)));
        factory.createBasket("X","X","X", c, w, false, 0, INITIAL_DEPOSIT);
    }

    function test_revertCreate_initialDepositTooLow() public {
        address[] memory c = _defaultConstituents();
        uint256[] memory w = _defaultWeights();

        vm.prank(creator);
        vm.expectRevert(abi.encodeWithSelector(
            BasketFactory.InitialDepositTooLow.selector, 1e6, 10e6
        ));
        factory.createBasket("X","X","X", c, w, false, 0, 1e6);
    }

    function test_revertCreate_invalidDriftThreshold_zero() public {
        address[] memory c = _defaultConstituents();
        uint256[] memory w = _defaultWeights();

        vm.prank(creator);
        vm.expectRevert(BasketFactory.InvalidDriftThreshold.selector);
        factory.createBasket("X","X","X", c, w, true, 0, INITIAL_DEPOSIT);
    }

    function test_revertCreate_invalidDriftThreshold_tooHigh() public {
        address[] memory c = _defaultConstituents();
        uint256[] memory w = _defaultWeights();

        vm.prank(creator);
        vm.expectRevert(BasketFactory.InvalidDriftThreshold.selector);
        factory.createBasket("X","X","X", c, w, true, 5_001, INITIAL_DEPOSIT);
    }

    function testFuzz_depositAmountScalesTokens(uint256 depositAmount) public {
        depositAmount = bound(depositAmount, 10e6, 10_000e6);
        usdg.mint(creator, depositAmount);
        vm.prank(creator);
        usdg.approve(address(factory), type(uint256).max);

        (address basket,) = _createTestBasket();
        BasketImplementation b = BasketImplementation(basket);

        uint256 supply1 = b.totalSupply();

        usdg.mint(creator, depositAmount);
        vm.startPrank(creator);
        usdg.approve(basket, depositAmount);
        uint256 minted = b.deposit(depositAmount, 0, creator);
        vm.stopPrank();

        assertGt(minted, 0);
        assertEq(b.totalSupply(), supply1 + minted);
    }

    function _createTestBasket() internal returns (address basket, address creatorToken) {
        vm.prank(creator);
        (basket, creatorToken) = factory.createBasket(
            "Test Basket", "TBASKET", "A diversified tech thesis",
            _defaultConstituents(), _defaultWeights(),
            false, 0, INITIAL_DEPOSIT
        );
    }

    function _defaultConstituents() internal view returns (address[] memory c) {
        c = new address[](5);
        c[0] = address(tsla);
        c[1] = address(amzn);
        c[2] = address(pltr);
        c[3] = address(nflx);
        c[4] = address(amd);
    }

    function _defaultWeights() internal pure returns (uint256[] memory w) {
        w = new uint256[](5);
        w[0] = 2_500;
        w[1] = 2_500;
        w[2] = 2_000;
        w[3] = 2_000;
        w[4] = 1_000;
    }

    function _addAsset(
        address token,
        address oracle,
        string memory symbol,
        string memory name,
        string memory sector
    ) internal {
        registry.addAsset(IWeaveRegistry.AssetConfig({
            tokenAddress: token,
            oracle:       oracle,
            symbol:       symbol,
            name:         name,
            sector:       sector,
            active:       true
        }));
    }
}