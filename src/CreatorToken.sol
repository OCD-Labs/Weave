// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ICreatorToken}  from "./interfaces/ICreatorToken.sol";
import {ERC20}          from "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import {IERC20}         from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20}      from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {Math}           from "@openzeppelin/contracts/utils/math/Math.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";

/// @notice ERC-7641 intrinsic revenue-share token for a single Weave basket.
/// Fixed supply of 1,000,000 units minted entirely to the basket creator at deployment.
/// Revenue (USDG) is distributed proportionally to token holders via snapshots.
/// Each snapshot records the USDG amount added and the total supply at that moment.
/// Holders claim their proportional share per snapshot. Past snapshots are claimable
/// by whoever held the tokens at that snapshot's block number.
contract CreatorToken is ICreatorToken, ERC20, ReentrancyGuard {
    using SafeERC20 for IERC20;

    address public immutable override basket;
    address public immutable override usdg;

    /// @notice Fixed supply — never changes after construction.
    uint256 public constant TOTAL_SUPPLY = 1_000_000 * 1e18;

    /// @notice Monotonically increasing snapshot counter. Starts at 0, first snapshot is ID 1.
    uint256 public override snapshotCount;

    struct Snapshot {
        uint256 usdgAmount;    // USDG added to the pool at this snapshot
        uint256 totalSupply;   // creator token total supply at snapshot time (always TOTAL_SUPPLY)
        uint256 blockNumber;   // block at which this snapshot was recorded
    }

    /// @notice snapshotId → snapshot data
    mapping(uint256 => Snapshot) private _snapshots;

    /// @notice holder → snapshotId → whether they already claimed this snapshot
    mapping(address => mapping(uint256 => bool)) private _claimed;

    /// @notice Checkpoint struct for tracking balance history.
    struct Checkpoint {
        uint256 fromBlock;
        uint256 balance;
    }

    /// @notice holder → ordered list of balance checkpoints
    mapping(address => Checkpoint[]) private _checkpoints;

    error OnlyBasket();
    error AlreadyClaimed(address account, uint256 snapshotId);
    error SnapshotDoesNotExist(uint256 snapshotId);
    error ZeroAmount();
    error NothingToClaim();
    error InsufficientBalance(uint256 requested, uint256 available);
    error ZeroAddress();

    event RevenueSnapshoted(uint256 indexed snapshotId, uint256 usdgAmount, uint256 blockNumber);
    event RevenueClaimed(address indexed account, uint256 indexed snapshotId, uint256 usdgAmount);
    event TokensBurned(address indexed account, uint256 amount, uint256 usdgRedeemed);

    modifier onlyBasket() {
        if (msg.sender != basket) revert OnlyBasket();
        _;
    }

    constructor(
        address _basket,
        address _usdg,
        address _creator,
        string memory _name,
        string memory _symbol
    ) ERC20(_name, _symbol) {
        if (_basket == address(0)) revert ZeroAddress();
        if (_usdg   == address(0)) revert ZeroAddress();
        
        basket = _basket;
        usdg   = _usdg;

        // Mint entire supply to the creator. Supply never changes after this.
        _mint(_creator, TOTAL_SUPPLY);

        // Record the creator's initial checkpoint so snapshot lookups work from block 0.
        _writeCheckpoint(_creator, TOTAL_SUPPLY);
    }

    /// @notice Called by the basket when it distributes the creator's cut of a management fee.
    /// Pulls USDG from the basket (caller must have approved this contract) and records a snapshot.
    function snapshotRevenue(uint256 usdgAmount) external override onlyBasket {
        if (usdgAmount == 0) revert ZeroAmount();

        // Pull the USDG from the basket into this contract's revenue pool.
        IERC20(usdg).safeTransferFrom(msg.sender, address(this), usdgAmount);

        uint256 snapId = ++snapshotCount;

        _snapshots[snapId] = Snapshot({
            usdgAmount:  usdgAmount,
            totalSupply: TOTAL_SUPPLY,   // fixed — always the same, but stored for clarity
            blockNumber: block.number
        });

        emit RevenueSnapshoted(snapId, usdgAmount, block.number);
    }

    /// @notice How much USDG account can claim for a specific snapshot.
    /// Uses the holder's balance at the snapshot's block number.
    function claimableRevenue(address account, uint256 snapshotId)
        public
        view
        override
        returns (uint256)
    {
        if (snapshotId == 0 || snapshotId > snapshotCount) return 0;
        if (_claimed[account][snapshotId]) return 0;

        Snapshot storage snap = _snapshots[snapshotId];
        uint256 holderBalance = _balanceAtBlock(account, snap.blockNumber);
        if (holderBalance == 0) return 0;

        // Proportional share: holderBalance / TOTAL_SUPPLY * usdgAmount.
        // Math.mulDiv prevents overflow on the intermediate product. Floors toward zero.
        return Math.mulDiv(snap.usdgAmount, holderBalance, snap.totalSupply);
    }

    /// @notice Claim USDG for a single snapshot.
    function claim(uint256 snapshotId) public override nonReentrant {
        if (snapshotId == 0 || snapshotId > snapshotCount) revert SnapshotDoesNotExist(snapshotId);
        if (_claimed[msg.sender][snapshotId]) revert AlreadyClaimed(msg.sender, snapshotId);

        uint256 amount = claimableRevenue(msg.sender, snapshotId);
        if (amount == 0) revert NothingToClaim();

        // Mark claimed before transfer — checks-effects-interactions.
        _claimed[msg.sender][snapshotId] = true;

        IERC20(usdg).safeTransfer(msg.sender, amount);

        emit RevenueClaimed(msg.sender, snapshotId, amount);
    }

    /// @notice Sweep all unclaimed snapshots in one transaction.
    /// Silently skips snapshots where the caller has nothing to claim.
    function claimAll() external override nonReentrant {
        uint256 total = snapshotCount;
        uint256 accumulated;

        for (uint256 i = 1; i <= total; ++i) {
            if (_claimed[msg.sender][i]) continue;

            uint256 amount = claimableRevenue(msg.sender, i);
            if (amount == 0) continue;

            // Mark before accumulating — no external calls inside this loop.
            _claimed[msg.sender][i] = true;
            accumulated += amount;

            emit RevenueClaimed(msg.sender, i, amount);
        }

        if (accumulated == 0) revert NothingToClaim();

        // Single transfer for all accumulated claims — saves gas vs per-snapshot transfers.
        IERC20(usdg).safeTransfer(msg.sender, accumulated);
    }

    /// @notice How much USDG would be redeemable if amount tokens were burned right now.
    /// Based on the current USDG balance of this contract (the revenue pool residual).
    function redeemableOnBurn(uint256 amount) public view override returns (uint256) {
        uint256 poolBalance = IERC20(usdg).balanceOf(address(this));
        if (poolBalance == 0 || totalSupply() == 0) return 0;
        // floors toward zero
        return Math.mulDiv(poolBalance, amount, totalSupply());
    }

    /// @notice Burn creator tokens and redeem a proportional share of the revenue pool.
    /// This is the deflationary mechanism in ERC-7641 — burning reduces total supply
    /// and entitles the burner to their share of accumulated unclaimed USDG in the pool.
    function burn(uint256 amount) external override nonReentrant {
        if (amount == 0) revert ZeroAmount();
        if (balanceOf(msg.sender) < amount) {
            revert InsufficientBalance(amount, balanceOf(msg.sender));
        }

        uint256 redeemable = redeemableOnBurn(amount);

        // Burn first — reduces supply before any transfer.
        _burn(msg.sender, amount);

        if (redeemable > 0) {
            IERC20(usdg).safeTransfer(msg.sender, redeemable);
        }

        emit TokensBurned(msg.sender, amount, redeemable);
    }

    /// @notice Every token transfer writes checkpoints for both sender and receiver
    /// so that future snapshot claims can look up the correct historical balance.
    function _update(address from, address to, uint256 amount) internal override {
        super._update(from, to, amount);

        // Write updated checkpoints after the transfer has settled.
        if (from != address(0)) _writeCheckpoint(from, balanceOf(from));
        if (to   != address(0)) _writeCheckpoint(to,   balanceOf(to));
    }

    /// @notice Records the current balance of account at the current block.
    /// If the last checkpoint is already from this block, overwrites it (same block,
    /// multiple transfers — only the final balance matters for snapshot purposes).
    function _writeCheckpoint(address account, uint256 balance) internal {
        Checkpoint[] storage ckpts = _checkpoints[account];
        uint256 len = ckpts.length;

        if (len > 0 && ckpts[len - 1].fromBlock == block.number) {
            // Same block — overwrite rather than append. Only the end-of-block balance counts.
            ckpts[len - 1].balance = balance;
        } else {
            ckpts.push(Checkpoint({fromBlock: block.number, balance: balance}));
        }
    }

    /// @notice Binary search through checkpoints to find the holder's balance
    /// at or before a given block number. Returns 0 if they had no balance then.
    function _balanceAtBlock(address account, uint256 blockNumber)
        internal
        view
        returns (uint256)
    {
        Checkpoint[] storage ckpts = _checkpoints[account];
        uint256 len = ckpts.length;
        if (len == 0) return 0;

        // Fast path: if blockNumber is at or after the latest checkpoint, return current balance.
        if (ckpts[len - 1].fromBlock <= blockNumber) return ckpts[len - 1].balance;

        // Fast path: if blockNumber is before the first checkpoint, they held nothing.
        if (ckpts[0].fromBlock > blockNumber) return 0;

        // Binary search for the rightmost checkpoint with fromBlock <= blockNumber.
        uint256 low  = 0;
        uint256 high = len - 1;
        while (low < high) {
            // floors the midpoint — avoids infinite loop when high = low + 1
            uint256 mid = Math.average(low, high + 1);
            if (ckpts[mid].fromBlock <= blockNumber) {
                low = mid;
            } else {
                high = mid - 1;
            }
        }
        return ckpts[low].balance;
    }

    /// @notice Convenience view: returns the checkpoint array length for an account.
    /// Useful for off-chain tooling to understand checkpoint history depth.
    function checkpointCount(address account) external view returns (uint256) {
        return _checkpoints[account].length;
    }
}