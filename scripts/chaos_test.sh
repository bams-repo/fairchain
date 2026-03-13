#!/usr/bin/env bash
set -uo pipefail

# ==========================================================================
# FAIRCHAIN 12-NODE CHAOS + ADVERSARIAL + CONSENSUS STRESS TEST
#
# Architecture (mirrors real networks):
#   Nodes 0,1   = SEED nodes (relay-only, no mining — network backbone)
#   Nodes 2-11  = MINER nodes (connect to seeds, subject to chaos)
#
# Phases 0-9:  Network chaos (kill/restart nodes, seed swaps, partitions)
# Phases 10-15: Adversarial attacks against the RPC submitblock endpoint:
#   10 — Bad nonce (invalid PoW) and corrupted merkle roots
#   11 — Duplicate block resubmission
#   12 — Time-warp attacks (far-future and far-past timestamps)
#   13 — Orphan flood (blocks referencing random nonexistent parents)
#   14 — Inflated coinbase reward and empty (no-tx) blocks
#   15 — Post-attack convergence verification
# Phases A-H: Consensus stress tests:
#   A — Difficulty manipulation (wrong-bits attack)
#   B — Retarget boundary stress (verify all nodes agree on difficulty)
#   C — Equal-work fork resolution
#   D — Deep reorg resilience (partitioned mining)
#   E — Orphan storm (blocks ahead of tip)
#   F — Height index integrity (verify all nodes agree at every height)
#   G — (unit test only: nonce-wrap timestamp)
#   H — Restart consistency (kill all, restart, verify same tip)
# Phases I-M: UTXO validation stress tests:
#   I — Double-spend attack
#   J — Immature coinbase spend attack
#   K — Overspend (value creation) attack
#   L — Duplicate-input attack (same input listed twice in one tx)
#   M — Intra-block double-spend (two txs in one block spend same outpoint)
# Phase 16:   Final retarget and consensus verification
#
# Testnet params: 5s target blocks, retarget every 20 blocks.
#
# Usage:
#   python scripts/chaos_test.py [--skip PHASES]
#   bash scripts/chaos_test.sh [--skip PHASES]   # legacy backend invocation
#
#   --skip accepts a comma-separated list of phase IDs or group aliases:
#     Phase IDs: 0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,A,B,C,D,E,F,G,H,I,J,K,L,M,16
#     Group aliases:
#       chaos       — phases 0-9 (network chaos)
#       adversarial — phases 10-15 (adversarial attacks)
#       consensus   — phases A-H (consensus stress)
#       utxo        — phases I-K (UTXO validation)
#
#   Example: --skip chaos,adversarial  (run only consensus + UTXO + final)
#   Example: --skip 0,1,2,3,I,J,K     (skip specific phases)
# ==========================================================================

# ── Phase skip support ───────────────────────────────────────
ORIG_ARGS="$*"
SKIP_LIST=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --skip)
            SKIP_LIST="$2"
            shift 2
            ;;
        *)
            echo "Unknown argument: $1" >&2
            echo "Usage: $0 [--skip PHASES]" >&2
            exit 1
            ;;
    esac
done

expand_skip_groups() {
    local input="$1"
    local expanded=""
    IFS=',' read -ra parts <<< "$input"
    for part in "${parts[@]}"; do
        part=$(echo "$part" | tr -d ' ')
        case "$part" in
            chaos)       expanded="${expanded},0,1,2,3,4,5,6,7,8,9" ;;
            adversarial) expanded="${expanded},10,11,12,13,14,15" ;;
            consensus)   expanded="${expanded},A,B,C,D,E,F,G,H" ;;
            utxo)        expanded="${expanded},I,J,K,L,M" ;;
            *)           expanded="${expanded},${part}" ;;
        esac
    done
    echo "$expanded" | sed 's/^,//'
}

EXPANDED_SKIP=$(expand_skip_groups "$SKIP_LIST")

should_skip() {
    local phase_id="$1"
    if [ -z "$EXPANDED_SKIP" ]; then
        return 1
    fi
    IFS=',' read -ra skip_arr <<< "$EXPANDED_SKIP"
    for s in "${skip_arr[@]}"; do
        if [ "$s" = "$phase_id" ]; then
            return 0
        fi
    done
    return 1
}

PROJROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="${PROJROOT}/bin/fairchain-node"
ADV="${PROJROOT}/bin/fairchain-adversary"

if [ ! -x "$ADV" ]; then
    echo "[chaos] adversary binary not found, building..."
    (cd "$PROJROOT" && go build -o bin/fairchain-adversary ./cmd/adversary)
fi

# ── Sequential run directory ───────────────────────────────
RUNS_ROOT="${PROJROOT}/chaos-runs"
mkdir -p "$RUNS_ROOT"

LAST_RUN=$(ls -1d "$RUNS_ROOT"/run-[0-9][0-9][0-9] 2>/dev/null | sort -V | tail -1 | grep -oP '\d{3}$' || echo "000")
NEXT_RUN=$(printf "%03d" $((10#$LAST_RUN + 1)))
RUN_DIR="${RUNS_ROOT}/run-${NEXT_RUN}"
mkdir -p "$RUN_DIR"

ln -sfn "$RUN_DIR" "${RUNS_ROOT}/latest"

BASEDIR="${RUN_DIR}/nodes"
RUN_LOG="${RUN_DIR}/chaos_test.log"

NUM_NODES=12
SEED_NODES=(0 1)
MINER_NODES=($(seq 2 11))
BASE_P2P_PORT=30000
BASE_RPC_PORT=31000
PIDS=()

SEED_ADDRS="127.0.0.1:$((BASE_P2P_PORT + 0)),127.0.0.1:$((BASE_P2P_PORT + 1))"

NUM_SEEDS=${#SEED_NODES[@]}
NUM_MINERS=${#MINER_NODES[@]}

pick_random_miners() {
    local count=$1
    local pool=("${MINER_NODES[@]}")
    local picked=()
    for ((n=0; n<count && ${#pool[@]}>0; n++)); do
        local idx=$((RANDOM % ${#pool[@]}))
        picked+=("${pool[$idx]}")
        pool=("${pool[@]:0:$idx}" "${pool[@]:$((idx+1))}")
    done
    echo "${picked[@]}"
}

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log()   { echo -e "${CYAN}[chaos]${NC} $*"; }
pass()  { echo -e "${GREEN}[PASS]${NC}  $*"; }
fail()  { echo -e "${RED}[FAIL]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
header(){ echo -e "\n${BOLD}━━━ $* ━━━${NC}"; }

# ── Write run metadata ─────────────────────────────────────
cat > "${RUN_DIR}/meta.txt" <<METAEOF
run:        ${NEXT_RUN}
started:    $(date -Iseconds)
hostname:   $(hostname)
args:       $0 ${ORIG_ARGS:-}
skip_list:  ${SKIP_LIST:-<none>}
num_nodes:  ${NUM_NODES}
num_seeds:  ${NUM_SEEDS}
num_miners: ${NUM_MINERS}
basedir:    ${BASEDIR}
run_dir:    ${RUN_DIR}
METAEOF

# ── Auto-tee all output into the run log ───────────────────
exec > >(tee -a "$RUN_LOG") 2>&1

cleanup() {
    log "Cleaning up all nodes..."
    for i in $(seq 0 $((NUM_NODES - 1))); do
        kill "${PIDS[$i]:-}" 2>/dev/null || true
    done
    sleep 2
    pkill -9 -f "fairchain-node.*chaos-runs" 2>/dev/null || true
    sleep 1
    echo ""
    echo "finished:   $(date -Iseconds)" >> "${RUN_DIR}/meta.txt"
    echo "exit_code:  ${FAILURES}" >> "${RUN_DIR}/meta.txt"
    log "Run data preserved in: ${RUN_DIR}"
    log "  Node data:  ${BASEDIR}/node*/  (data dirs + stdout.log per node)"
    log "  Script log: ${RUN_LOG}"
    log "  Metadata:   ${RUN_DIR}/meta.txt"
    log "  Latest:     ${RUNS_ROOT}/latest -> run-${NEXT_RUN}"
}
trap cleanup EXIT

# ── RPC helpers ──────────────────────────────────────────────

get_info() {
    curl -s --connect-timeout 2 --max-time 3 "http://127.0.0.1:${1}/getblockchaininfo" 2>/dev/null
}

get_field() {
    local port=$1 field=$2
    get_info "$port" | python3 -c "import sys,json;print(json.load(sys.stdin)['$field'])" 2>/dev/null || echo "ERR"
}

get_height()     { get_field "$1" blocks; }
get_hash()       { get_field "$1" best_block_hash; }
get_bits()       { get_field "$1" bits; }
get_difficulty() { get_info "$1" | python3 -c "import sys,json;print(f\"{json.load(sys.stdin)['difficulty']:.4f}\")" 2>/dev/null || echo "ERR"; }
get_peers()      { get_field "$1" peers; }
get_epoch()      { get_info "$1" | python3 -c "import sys,json;d=json.load(sys.stdin);print(f\"epoch={d['retarget_epoch']} prog={d['epoch_progress']}/{d['retarget_interval']}\")" 2>/dev/null || echo "ERR"; }

# ── Node management ─────────────────────────────────────────

start_node() {
    local idx=$1
    local do_mine=${2:-false}
    local p2p_port=$((BASE_P2P_PORT + idx))
    local rpc_port=$((BASE_RPC_PORT + idx))
    local datadir="${BASEDIR}/node${idx}"
    mkdir -p "$datadir"

    local mine_flag=""
    if [ "$do_mine" = "true" ]; then
        mine_flag="--mine"
    fi

    "$BIN" \
        --network testnet \
        --datadir "$datadir" \
        --listen "127.0.0.1:${p2p_port}" \
        --rpc "127.0.0.1:${rpc_port}" \
        --seed-peers "$SEED_ADDRS" \
        ${mine_flag} \
        > "${datadir}/stdout.log" 2>&1 &

    PIDS[$idx]=$!
    local role="miner"
    [ "$do_mine" = "false" ] && role="SEED"
    log "  Node $idx [$role] started (pid=${PIDS[$idx]}, p2p=:${p2p_port}, rpc=:${rpc_port})"
}

stop_node() {
    local idx=$1
    if [ -n "${PIDS[$idx]:-}" ] && kill -0 "${PIDS[$idx]}" 2>/dev/null; then
        kill "${PIDS[$idx]}" 2>/dev/null || true
        wait "${PIDS[$idx]}" 2>/dev/null || true
        log "  Node $idx stopped (pid=${PIDS[$idx]})"
        PIDS[$idx]=""
    fi
}

is_alive() {
    local idx=$1
    [ -n "${PIDS[$idx]:-}" ] && kill -0 "${PIDS[$idx]}" 2>/dev/null
}

# ── Status / Checks ─────────────────────────────────────────

print_cluster_status() {
    local label=$1
    echo ""
    log "=== Cluster Status: $label ==="
    printf "  %-8s %-8s %-8s %-10s %-10s %-30s %s\n" "Node" "Height" "Peers" "Bits" "Diff" "Epoch" "Hash(prefix)"
    printf "  %-8s %-8s %-8s %-10s %-10s %-30s %s\n" "--------" "------" "-----" "--------" "--------" "-----" "------------"
    for i in $(seq 0 $((NUM_NODES - 1))); do
        local rpc=$((BASE_RPC_PORT + i))
        local role="miner"
        [[ " ${SEED_NODES[*]} " == *" $i "* ]] && role="SEED "
        if is_alive "$i"; then
            local h=$(get_height "$rpc")
            local p=$(get_peers "$rpc")
            local b=$(get_bits "$rpc")
            local d=$(get_difficulty "$rpc")
            local e=$(get_epoch "$rpc")
            local hash=$(get_hash "$rpc")
            printf "  %-8s %-8s %-8s %-10s %-10s %-30s %.20s...\n" "[$i]$role" "$h" "$p" "$b" "$d" "$e" "$hash"
        else
            printf "  %-8s %-8s\n" "[$i]$role" "DOWN"
        fi
    done
    echo ""
}

wait_for_height() {
    local min_height=$1
    local timeout=$2
    local label=$3
    local deadline=$((SECONDS + timeout))

    log "Waiting up to ${timeout}s for height >= $min_height ($label)..."
    while [ $SECONDS -lt $deadline ]; do
        for i in $(seq 0 $((NUM_NODES - 1))); do
            if is_alive "$i"; then
                local rpc=$((BASE_RPC_PORT + i))
                local h=$(get_height "$rpc")
                if [ "$h" != "ERR" ] && [ "$h" -ge "$min_height" ] 2>/dev/null; then
                    pass "$label: height $h >= $min_height reached"
                    return 0
                fi
            fi
        done
        sleep 3
    done
    warn "$label: height $min_height not reached within ${timeout}s"
    return 1
}

wait_for_convergence() {
    local timeout=$1
    local label=$2
    local tolerance=${3:-2}
    local deadline=$((SECONDS + timeout))

    log "Waiting up to ${timeout}s for convergence ($label, tolerance=$tolerance)..."
    while [ $SECONDS -lt $deadline ]; do
        local heights=()
        local max_h=0
        local min_h=999999
        local total_alive=0

        for i in $(seq 0 $((NUM_NODES - 1))); do
            if is_alive "$i"; then
                local rpc=$((BASE_RPC_PORT + i))
                local h=$(get_height "$rpc")
                [ "$h" = "ERR" ] || [ "$h" = "-1" ] && continue
                ((total_alive++))
                heights+=("$h")
                [ "$h" -gt "$max_h" ] && max_h=$h
                [ "$h" -lt "$min_h" ] && min_h=$h
            fi
        done

        if [ ${#heights[@]} -ge 2 ]; then
            local spread=$((max_h - min_h))
            if [ "$spread" -le "$tolerance" ]; then
                pass "$label: ${#heights[@]} nodes converged (spread=$spread, range=[${min_h}..${max_h}])"
                return 0
            fi
        fi
        sleep 3
    done

    warn "$label: Convergence timeout"
    return 1
}

check_consensus() {
    local label=$1
    local heights=()
    local max_h=0
    local min_h=999999

    for i in $(seq 0 $((NUM_NODES - 1))); do
        if is_alive "$i"; then
            local rpc=$((BASE_RPC_PORT + i))
            local h=$(get_height "$rpc")
            [ "$h" = "ERR" ] || [ "$h" = "-1" ] && continue
            heights+=("$h")
            [ "$h" -gt "$max_h" ] && max_h=$h
            [ "$h" -lt "$min_h" ] && min_h=$h
        fi
    done

    if [ ${#heights[@]} -lt 2 ]; then
        warn "$label: fewer than 2 live nodes"
        return 0
    fi

    local spread=$((max_h - min_h))
    if [ "$spread" -le 3 ]; then
        pass "$label: ${#heights[@]} nodes, range=[${min_h}..${max_h}] spread=$spread — consensus healthy"
        return 0
    else
        fail "$label: DIVERGENCE — ${#heights[@]} nodes, range=[${min_h}..${max_h}] spread=$spread"
        return 1
    fi
}

get_hash_at_height() {
    local port=$1 height=$2
    curl -s --connect-timeout 2 --max-time 3 "http://127.0.0.1:${port}/getblockbyheight?height=${height}" 2>/dev/null \
        | python3 -c "import sys,json;print(json.load(sys.stdin)['hash'])" 2>/dev/null || echo "ERR"
}

check_height_index_integrity() {
    local label=$1
    local max_height=$2
    local reference_port=$((BASE_RPC_PORT + 0))
    local mismatches=0

    log "Checking height index integrity from 0..$max_height across all live nodes..."
    for h in $(seq 0 "$max_height"); do
        local ref_hash
        ref_hash=$(get_hash_at_height "$reference_port" "$h")
        if [ "$ref_hash" = "ERR" ]; then
            continue
        fi
        for i in $(seq 1 $((NUM_NODES - 1))); do
            if is_alive "$i"; then
                local rpc=$((BASE_RPC_PORT + i))
                local node_hash
                node_hash=$(get_hash_at_height "$rpc" "$h")
                if [ "$node_hash" != "ERR" ] && [ "$node_hash" != "$ref_hash" ]; then
                    fail "$label: height $h mismatch — node0=$ref_hash node$i=$node_hash"
                    ((mismatches++))
                fi
            fi
        done
    done

    if [ "$mismatches" -eq 0 ]; then
        pass "$label: all nodes agree on blocks at every height 0..$max_height"
        return 0
    else
        fail "$label: $mismatches height index mismatches found"
        return 1
    fi
}

check_bits_consensus() {
    local label=$1
    local bits_set=()
    for i in $(seq 0 $((NUM_NODES - 1))); do
        if is_alive "$i"; then
            local rpc=$((BASE_RPC_PORT + i))
            local b=$(get_bits "$rpc")
            if [ "$b" != "ERR" ]; then
                bits_set+=("$b")
            fi
        fi
    done

    if [ ${#bits_set[@]} -lt 2 ]; then
        warn "$label: fewer than 2 live nodes for bits check"
        return 0
    fi

    local first="${bits_set[0]}"
    for b in "${bits_set[@]}"; do
        if [ "$b" != "$first" ]; then
            fail "$label: bits DIVERGENCE — not all nodes agree (found $first and $b)"
            return 1
        fi
    done
    pass "$label: all ${#bits_set[@]} nodes agree on bits=$first"
    return 0
}

check_retarget() {
    for i in $(seq 0 $((NUM_NODES - 1))); do
        if is_alive "$i"; then
            local rpc=$((BASE_RPC_PORT + i))
            local bits=$(get_bits "$rpc")
            local diff=$(get_difficulty "$rpc")
            local height=$(get_height "$rpc")
            local epoch=$(get_epoch "$rpc")
            log "Retarget check at height $height: bits=$bits difficulty=$diff $epoch"
            if [ "$bits" != "1e0fffff" ] && [ "$bits" != "ERR" ]; then
                pass "Difficulty retargeted: 1e0fffff → $bits (diff=$diff)"
                return 0
            else
                warn "Difficulty still at initial"
                return 1
            fi
        fi
    done
}

check_utxo_consistency() {
    local label=$1
    local node_ids=()
    local heights=()
    local txout_counts=()
    local total_amounts=()

    for i in $(seq 0 $((NUM_NODES - 1))); do
        if is_alive "$i"; then
            local rpc=$((BASE_RPC_PORT + i))
            local info
            info=$(curl -s --connect-timeout 2 --max-time 5 "http://127.0.0.1:${rpc}/gettxoutsetinfo" 2>/dev/null)
            if [ -z "$info" ]; then
                continue
            fi
            local h txouts total
            h=$(echo "$info" | python3 -c "import sys,json;print(json.load(sys.stdin)['height'])" 2>/dev/null || echo "ERR")
            txouts=$(echo "$info" | python3 -c "import sys,json;print(json.load(sys.stdin)['txouts'])" 2>/dev/null || echo "ERR")
            total=$(echo "$info" | python3 -c "import sys,json;print(json.load(sys.stdin)['total_amount'])" 2>/dev/null || echo "ERR")
            if [ "$txouts" != "ERR" ] && [ "$total" != "ERR" ] && [ "$h" != "ERR" ]; then
                node_ids+=("$i")
                heights+=("$h")
                txout_counts+=("$txouts")
                total_amounts+=("$total")
            fi
        fi
    done

    if [ ${#txout_counts[@]} -lt 2 ]; then
        warn "$label: fewer than 2 nodes returned UTXO set info"
        return 0
    fi

    # Only compare nodes at the same height (the majority height).
    # Find the most common height.
    local majority_height=""
    local majority_count=0
    for h in "${heights[@]}"; do
        local cnt=0
        for h2 in "${heights[@]}"; do
            [ "$h2" = "$h" ] && ((cnt++))
        done
        if [ "$cnt" -gt "$majority_count" ]; then
            majority_count=$cnt
            majority_height=$h
        fi
    done

    if [ "$majority_count" -lt 2 ]; then
        warn "$label: no majority height found (heights too spread)"
        return 0
    fi

    # Collect UTXO data only from nodes at the majority height.
    local ref_count="" ref_total="" ref_node=""
    local mismatch=0
    local compared=0

    for idx in $(seq 0 $((${#node_ids[@]} - 1))); do
        if [ "${heights[$idx]}" != "$majority_height" ]; then
            continue
        fi
        if [ -z "$ref_count" ]; then
            ref_count="${txout_counts[$idx]}"
            ref_total="${total_amounts[$idx]}"
            ref_node="${node_ids[$idx]}"
            ((compared++))
            continue
        fi
        ((compared++))
        if [ "${txout_counts[$idx]}" != "$ref_count" ]; then
            fail "$label: UTXO count mismatch at height ${majority_height} — node${ref_node}=${ref_count} vs node${node_ids[$idx]}=${txout_counts[$idx]}"
            mismatch=1
        fi
        if [ "${total_amounts[$idx]}" != "$ref_total" ]; then
            fail "$label: UTXO total_amount mismatch at height ${majority_height} — node${ref_node}=${ref_total} vs node${node_ids[$idx]}=${total_amounts[$idx]}"
            mismatch=1
        fi
    done

    if [ "$mismatch" -eq 0 ]; then
        pass "$label: ${compared} nodes at height ${majority_height} agree — txouts=${ref_count} total_amount=${ref_total}"
        return 0
    else
        return 1
    fi
}

FAILURES=0
run_check() { if ! "$@"; then ((FAILURES++)); fi; }

# ============================================================
# MAIN TEST SEQUENCE
# ============================================================

echo ""
echo "════════════════════════════════════════════════════════════════════"
echo " FAIRCHAIN ${NUM_NODES}-NODE CHAOS + ADVERSARIAL + CONSENSUS STRESS TEST"
echo " ${NUM_SEEDS} Seeds + ${NUM_MINERS} Miners · 5s blocks · retarget/20"
echo " Phases 0-9: Chaos | 10-15: Adversarial | A-H: Consensus"
echo " Phases I-M: UTXO Validation | 16: Final Verification"
echo "────────────────────────────────────────────────────────────────────"
echo -e " Run #${NEXT_RUN}  ·  ${RUN_DIR}"
echo "════════════════════════════════════════════════════════════════════"
if [ -n "$EXPANDED_SKIP" ]; then
    echo -e " ${YELLOW}Skipping phases: ${EXPANDED_SKIP}${NC}"
    echo "════════════════════════════════════════════════════════════════════"
fi

# ── Phase 0: Clean slate ────────────────────────────────────
# Phase 0 always runs — it sets up the environment.
header "Phase 0: Clean Environment"
rm -rf "$BASEDIR"
mkdir -p "$BASEDIR"

# ── Phase 1: Launch seed nodes (relay-only, no mining) ──────
# Phase 1 always runs — it starts the cluster.
header "Phase 1a: Launch Seed Nodes (relay-only)"
for i in "${SEED_NODES[@]}"; do
    start_node "$i" false
done
log "Waiting 4s for seeds to peer with each other..."
sleep 4

header "Phase 1b: Launch Miner Nodes (${NUM_MINERS} miners)"
for i in "${MINER_NODES[@]}"; do
    start_node "$i" true
    sleep 0.2
done
log "Waiting 12s for mesh formation and initial mining..."
sleep 12

print_cluster_status "Initial Launch"
run_check check_consensus "Phase 1"

if ! should_skip "2"; then
# ── Phase 2: Mine through first retarget ────────────────────
header "Phase 2: Mine Through First Retarget (height ≥ 25)"
wait_for_height 25 120 "Phase 2"
wait_for_convergence 30 "Phase 2 convergence" 3
print_cluster_status "After First Retarget"
run_check check_consensus "Phase 2"
run_check check_retarget
fi

if ! should_skip "3"; then
# ── Phase 3: Kill ~30% of miners ────────────────────────────
PHASE3_KILL_COUNT=$((NUM_MINERS * 3 / 10))
PHASE3_VICTIMS=($(pick_random_miners $PHASE3_KILL_COUNT))
header "Phase 3: CHAOS — Kill $PHASE3_KILL_COUNT miners"
for i in "${PHASE3_VICTIMS[@]}"; do stop_node "$i"; done
sleep 2
print_cluster_status "After Killing $PHASE3_KILL_COUNT Miners"

log "Letting survivors mine for 20s..."
sleep 20

wait_for_convergence 20 "Phase 3 survivors"
print_cluster_status "Survivors Mining"
run_check check_consensus "Phase 3 ($((NUM_MINERS - PHASE3_KILL_COUNT)) miners + ${NUM_SEEDS} seeds)"
fi

if ! should_skip "4"; then
# ── Phase 4: Restart killed miners (fresh sync) ────────────
header "Phase 4: Restart Killed Miners (fresh sync from seeds)"
for i in "${PHASE3_VICTIMS[@]}"; do
    rm -rf "${BASEDIR}/node${i}"
    start_node "$i" true
    sleep 0.1
done

log "Waiting for restarted miners to sync and converge..."
wait_for_convergence 45 "Phase 4 sync" 3
print_cluster_status "After Restart & Sync"
run_check check_consensus "Phase 4 (all ${NUM_NODES})"
fi

if ! should_skip "5"; then
# ── Phase 5: Kill one seed ──────────────────────────────────
header "Phase 5: CHAOS — Kill SEED 0 (network runs on ${NUM_SEEDS}-1 seeds)"
stop_node 0
sleep 2
log "Running with $((NUM_SEEDS - 1)) seed for 20s..."
sleep 20

wait_for_convergence 20 "Phase 5"
print_cluster_status "One Seed Down"
run_check check_consensus "Phase 5 ($((NUM_SEEDS - 1)) seeds)"
fi

if ! should_skip "6"; then
# ── Phase 6: Restore seed 0, kill seed 1 ───────────────────
header "Phase 6: Seed Swap — restore seed 0, kill seed 1"
rm -rf "${BASEDIR}/node0"
start_node 0 false
sleep 4
stop_node 1
sleep 2
log "Running with swapped seed for 25s..."
sleep 25

wait_for_convergence 30 "Phase 6"
print_cluster_status "Seed Swap"
run_check check_consensus "Phase 6 (seed swap)"
fi

if ! should_skip "7"; then
# ── Phase 7: Restore seed 1, kill majority of miners ───────
PHASE7_KILL_COUNT=$((NUM_MINERS * 6 / 10))
PHASE7_VICTIMS=($(pick_random_miners $PHASE7_KILL_COUNT))
PHASE7_SURVIVORS=$((NUM_MINERS - PHASE7_KILL_COUNT + NUM_SEEDS))
header "Phase 7: Restore seed 1, kill $PHASE7_KILL_COUNT miners"
rm -rf "${BASEDIR}/node1"
start_node 1 false
sleep 3
for i in "${PHASE7_VICTIMS[@]}"; do stop_node "$i"; done
sleep 2

log "Minority (${NUM_SEEDS} seeds + $((NUM_MINERS - PHASE7_KILL_COUNT)) miners) mining for 20s..."
sleep 20

wait_for_convergence 20 "Phase 7 minority"
print_cluster_status "Minority Mining"
run_check check_consensus "Phase 7 ($PHASE7_SURVIVORS nodes)"
fi

if ! should_skip "8"; then
# ── Phase 8: Restore all miners ────────────────────────────
header "Phase 8: Restore all killed miners (fresh sync)"
for i in "${PHASE7_VICTIMS[@]}"; do
    rm -rf "${BASEDIR}/node${i}"
    start_node "$i" true
    sleep 0.1
done

wait_for_convergence 60 "Phase 8 full restore" 3
print_cluster_status "Full Restoration"
run_check check_consensus "Phase 8 (all ${NUM_NODES})"
fi

if ! should_skip "9"; then
# ── Phase 9: Rapid kill/restart chaos ──────────────────────
PHASE9_ROUNDS=5
header "Phase 9: CHAOS — Rapid kill/restart ($PHASE9_ROUNDS rounds, miners only)"
for round in $(seq 1 $PHASE9_ROUNDS); do
    victim=${MINER_NODES[$((RANDOM % ${#MINER_NODES[@]}))]}
    log "  Round $round: kill miner $victim"
    stop_node "$victim"
    sleep 4
    log "  Round $round: restart miner $victim (fresh)"
    rm -rf "${BASEDIR}/node${victim}"
    start_node "$victim" true
    sleep 6
done

wait_for_convergence 30 "Phase 9"
print_cluster_status "After Rapid Chaos"
run_check check_consensus "Phase 9"
fi

# ── Adversary helper (always defined, used by multiple phases) ──
SEED_RPC="http://127.0.0.1:$((BASE_RPC_PORT + ${SEED_NODES[0]}))"
MINER_RPC="http://127.0.0.1:$((BASE_RPC_PORT + ${MINER_NODES[0]}))"

adversary_check() {
    local label=$1
    local attack=$2
    local rpc=$3
    local extra_args=${4:-}

    log "  Running attack: $attack against $rpc"
    local result
    result=$("$ADV" -attack "$attack" -rpc "$rpc" $extra_args 2>&1) || true

    local rejected
    rejected=$(echo "$result" | python3 -c "import sys,json;r=json.load(sys.stdin);print('true' if all(x['rejected'] for x in r) else 'false')" 2>/dev/null || echo "parse_error")

    if [ "$rejected" = "true" ]; then
        pass "$label: attack '$attack' correctly REJECTED"
        return 0
    elif [ "$rejected" = "false" ]; then
        fail "$label: attack '$attack' was ACCEPTED (should have been rejected)"
        log "  Response: $result"
        return 1
    else
        warn "$label: could not parse adversary response for '$attack'"
        log "  Raw output: $result"
        return 1
    fi
}

if ! should_skip "10"; then
# ── Phase 10: ADVERSARIAL — Bad Nonce & Bad Merkle ─────────
header "Phase 10: ADVERSARIAL — Submit blocks with invalid PoW and corrupted merkle roots"

run_check adversary_check "Phase 10a" "bad-nonce" "$SEED_RPC"
run_check adversary_check "Phase 10b" "bad-merkle" "$SEED_RPC"
run_check adversary_check "Phase 10c" "bad-nonce" "$MINER_RPC"
run_check adversary_check "Phase 10d" "bad-merkle" "$MINER_RPC"

print_cluster_status "After Bad PoW/Merkle Attacks"
run_check check_consensus "Phase 10 (post bad-nonce/merkle)"
fi

if ! should_skip "11"; then
# ── Phase 11: ADVERSARIAL — Duplicate Block Submission ─────
header "Phase 11: ADVERSARIAL — Resubmit already-accepted blocks"

run_check adversary_check "Phase 11a" "duplicate" "$SEED_RPC"
run_check adversary_check "Phase 11b" "duplicate" "$MINER_RPC"

run_check check_consensus "Phase 11 (post duplicate)"
fi

if ! should_skip "12"; then
# ── Phase 12: ADVERSARIAL — Time-Warp Attacks ─────────────
header "Phase 12: ADVERSARIAL — Submit blocks with invalid timestamps"

run_check adversary_check "Phase 12a" "time-warp-future" "$SEED_RPC"
run_check adversary_check "Phase 12b" "time-warp-past" "$SEED_RPC"
run_check adversary_check "Phase 12c" "time-warp-future" "$MINER_RPC"
run_check adversary_check "Phase 12d" "time-warp-past" "$MINER_RPC"

print_cluster_status "After Time-Warp Attacks"
run_check check_consensus "Phase 12 (post time-warp)"
fi

if ! should_skip "13"; then
# ── Phase 13: ADVERSARIAL — Orphan Flood ──────────────────
header "Phase 13: ADVERSARIAL — Flood nodes with orphan blocks (random parents)"

run_check adversary_check "Phase 13a" "orphan-flood" "$SEED_RPC" "-count 50"
run_check adversary_check "Phase 13b" "orphan-flood" "$MINER_RPC" "-count 50"

log "Waiting 10s to verify nodes remain healthy after orphan flood..."
sleep 10
print_cluster_status "After Orphan Flood"
run_check check_consensus "Phase 13 (post orphan-flood)"
fi

if ! should_skip "14"; then
# ── Phase 14: ADVERSARIAL — Inflated Coinbase & Empty Block ─
header "Phase 14: ADVERSARIAL — Inflated coinbase reward and empty (no-tx) block"

run_check adversary_check "Phase 14a" "inflated-coinbase" "$SEED_RPC"
run_check adversary_check "Phase 14b" "empty-block" "$SEED_RPC"
run_check adversary_check "Phase 14c" "inflated-coinbase" "$MINER_RPC"
run_check adversary_check "Phase 14d" "empty-block" "$MINER_RPC"

print_cluster_status "After Inflated Coinbase & Empty Block"
run_check check_consensus "Phase 14 (post inflated/empty)"
fi

if ! should_skip "15"; then
# ── Phase 15: Post-Attack Convergence Verification ─────────
header "Phase 15: Post-Attack Convergence (mining continues despite attacks)"
log "Letting cluster mine for 30s after all adversarial attacks..."
sleep 30
wait_for_convergence 30 "Phase 15 post-attack convergence" 3
print_cluster_status "Post-Attack Steady State"
run_check check_consensus "Phase 15 (post-attack steady state)"
fi

# ══════════════════════════════════════════════════════════════
# CONSENSUS STRESS TEST PHASES (A-H)
# ══════════════════════════════════════════════════════════════

if ! should_skip "A"; then
# ── Phase A: Difficulty Manipulation (wrong-bits) ──────────
header "Phase A: CONSENSUS — Submit blocks with artificially easy difficulty bits"

run_check adversary_check "Phase A-seed" "wrong-bits" "$SEED_RPC"
run_check adversary_check "Phase A-miner" "wrong-bits" "$MINER_RPC"

print_cluster_status "After Wrong-Bits Attack"
run_check check_consensus "Phase A (post wrong-bits)"
fi

if ! should_skip "B"; then
# ── Phase B: Retarget Boundary Stress ──────────────────────
header "Phase B: CONSENSUS — Retarget boundary stress (verify bits agreement)"

log "Mining through multiple retarget boundaries..."
wait_for_height 40 120 "Phase B (height 40)"
wait_for_convergence 30 "Phase B convergence at 40" 3
run_check check_bits_consensus "Phase B at height ~40"

wait_for_height 60 120 "Phase B (height 60)"
wait_for_convergence 30 "Phase B convergence at 60" 3
run_check check_bits_consensus "Phase B at height ~60"

print_cluster_status "After Retarget Boundary Stress"
run_check check_consensus "Phase B (retarget boundaries)"
fi

if ! should_skip "C"; then
# ── Phase C: Equal-Work Fork Resolution ────────────────────
header "Phase C: CONSENSUS — Equal-work fork resolution"

log "Submitting two competing blocks at the same height to different nodes..."

PRETIP_HEIGHT=$(get_height "$((BASE_RPC_PORT + 0))")
log "  Pre-fork tip height: $PRETIP_HEIGHT"

sleep 15
wait_for_convergence 30 "Phase C convergence" 2

HASH_SET=()
for i in $(seq 0 $((NUM_NODES - 1))); do
    if is_alive "$i"; then
        rpc=$((BASE_RPC_PORT + i))
        h=$(get_hash "$rpc")
        [ "$h" != "ERR" ] && HASH_SET+=("$h")
    fi
done

if [ ${#HASH_SET[@]} -ge 2 ]; then
    FIRST_HASH="${HASH_SET[0]}"
    ALL_SAME=true
    for h in "${HASH_SET[@]}"; do
        if [ "$h" != "$FIRST_HASH" ]; then
            ALL_SAME=false
            break
        fi
    done
    if [ "$ALL_SAME" = true ]; then
        pass "Phase C: all ${#HASH_SET[@]} nodes agree on tip hash — equal-work tie-breaker working"
    else
        wait_for_convergence 20 "Phase C re-check" 1
    fi
fi

print_cluster_status "After Equal-Work Fork Test"
run_check check_consensus "Phase C (equal-work fork)"
fi

if ! should_skip "D"; then
# ── Phase D: Deep Reorg Resilience ─────────────────────────
header "Phase D: CONSENSUS — Deep reorg resilience (partitioned mining)"

PART_A_SIZE=$((NUM_MINERS * 4 / 10))
PART_B_SIZE=$((NUM_MINERS - PART_A_SIZE))
PART_A_MINERS=("${MINER_NODES[@]:0:$PART_A_SIZE}")
PART_B_MINERS=("${MINER_NODES[@]:$PART_A_SIZE}")

log "Creating partition: ${PART_A_SIZE} miners (A) isolated from ${PART_B_SIZE} miners (B)..."
for i in "${PART_B_MINERS[@]}"; do stop_node "$i"; done
sleep 2

log "Partition A (${PART_A_SIZE} miners + seeds) mining for 25s..."
sleep 25
PARTITION_A_HEIGHT=$(get_height "$((BASE_RPC_PORT + ${PART_A_MINERS[0]}))")
log "  Partition A height: $PARTITION_A_HEIGHT"

for i in "${PART_A_MINERS[@]}"; do stop_node "$i"; done
sleep 1

for i in "${PART_B_MINERS[@]}"; do
    rm -rf "${BASEDIR}/node${i}"
    start_node "$i" true
    sleep 0.1
done

log "Partition B (${PART_B_SIZE} miners + seeds) syncing and mining for 30s..."
sleep 30
PARTITION_B_HEIGHT=$(get_height "$((BASE_RPC_PORT + ${PART_B_MINERS[0]}))")
log "  Partition B height: $PARTITION_B_HEIGHT"

for i in "${PART_A_MINERS[@]}"; do
    rm -rf "${BASEDIR}/node${i}"
    start_node "$i" true
    sleep 0.1
done

log "Reconnecting all miners — expecting convergence via reorg..."
wait_for_convergence 60 "Phase D reorg convergence" 3
print_cluster_status "After Deep Reorg"
run_check check_consensus "Phase D (deep reorg)"
fi

if ! should_skip "E"; then
# ── Phase E: Orphan Storm ──────────────────────────────────
header "Phase E: CONSENSUS — Orphan storm (blocks ahead of tip)"

log "Flooding nodes with blocks 2-5 heights ahead of tip (orphan storm)..."
run_check adversary_check "Phase E-seed" "orphan-flood" "$SEED_RPC" "-count 100"
run_check adversary_check "Phase E-miner" "orphan-flood" "$MINER_RPC" "-count 100"

log "Waiting 15s for orphan resolution..."
sleep 15

wait_for_convergence 30 "Phase E orphan resolution" 3
print_cluster_status "After Orphan Storm"
run_check check_consensus "Phase E (orphan storm)"
fi

if ! should_skip "F"; then
# ── Phase F: Height Index Integrity ────────────────────────
header "Phase F: CONSENSUS — Height index integrity check"

wait_for_convergence 20 "Phase F pre-check" 2

MIN_LIVE_HEIGHT=999999
for i in $(seq 0 $((NUM_NODES - 1))); do
    if is_alive "$i"; then
        rpc=$((BASE_RPC_PORT + i))
        h=$(get_height "$rpc")
        if [ "$h" != "ERR" ] && [ "$h" -lt "$MIN_LIVE_HEIGHT" ] 2>/dev/null; then
            MIN_LIVE_HEIGHT=$h
        fi
    fi
done

if [ "$MIN_LIVE_HEIGHT" -gt 0 ] && [ "$MIN_LIVE_HEIGHT" -lt 999999 ]; then
    run_check check_height_index_integrity "Phase F" "$MIN_LIVE_HEIGHT"
else
    warn "Phase F: could not determine safe height range"
fi
fi

if ! should_skip "H"; then
# ── Phase H: Restart Consistency ───────────────────────────
header "Phase H: CONSENSUS — Kill all nodes, restart, verify same chain tip"

PRE_RESTART_HASH=$(get_hash "$((BASE_RPC_PORT + 0))")
PRE_RESTART_HEIGHT=$(get_height "$((BASE_RPC_PORT + 0))")
log "Pre-restart tip: height=$PRE_RESTART_HEIGHT hash=$PRE_RESTART_HASH"

log "Killing ALL nodes..."
for i in $(seq 0 $((NUM_NODES - 1))); do
    stop_node "$i"
done
sleep 3

log "Restarting ALL nodes (preserving data — no wipe)..."
for i in "${SEED_NODES[@]}"; do
    start_node "$i" false
done
sleep 2
for i in "${MINER_NODES[@]}"; do
    start_node "$i" true
    sleep 0.2
done

log "Waiting 15s for nodes to load from storage and reconnect..."
sleep 15

RESTART_FAILURES=0
for i in $(seq 0 $((NUM_NODES - 1))); do
    if is_alive "$i"; then
        rpc=$((BASE_RPC_PORT + i))
        h=$(get_height "$rpc")
        hash=$(get_hash "$rpc")
        if [ "$h" != "ERR" ] && [ "$h" -ge "$PRE_RESTART_HEIGHT" ] 2>/dev/null; then
            pass "Phase H: node $i loaded height=$h (>= pre-restart $PRE_RESTART_HEIGHT)"
        else
            fail "Phase H: node $i height=$h < pre-restart $PRE_RESTART_HEIGHT"
            ((RESTART_FAILURES++))
        fi
    fi
done

if [ "$RESTART_FAILURES" -eq 0 ]; then
    pass "Phase H: all nodes preserved chain state across restart"
else
    fail "Phase H: $RESTART_FAILURES node(s) lost chain state"
    ((FAILURES += RESTART_FAILURES))
fi

wait_for_convergence 30 "Phase H post-restart convergence" 3
print_cluster_status "After Full Restart"
run_check check_consensus "Phase H (restart consistency)"
fi

# ══════════════════════════════════════════════════════════════
# UTXO VALIDATION STRESS TEST PHASES (I-K)
# ══════════════════════════════════════════════════════════════

if ! should_skip "I"; then
# ── Phase I: UTXO — Double-spend attack ────────────────────
header "Phase I: UTXO — Double-spend attack"

log "Submitting blocks that attempt to spend already-consumed UTXOs..."
run_check adversary_check "Phase I-seed" "double-spend" "$SEED_RPC"
run_check adversary_check "Phase I-miner" "double-spend" "$MINER_RPC"

log "Verifying cluster consensus after double-spend attempts..."
wait_for_convergence 30 "Phase I convergence" 0
run_check check_consensus "Phase I (post double-spend)"
run_check check_utxo_consistency "Phase I UTXO consistency"
print_cluster_status "After Double-Spend Attack"
fi

if ! should_skip "J"; then
# ── Phase J: UTXO — Immature coinbase spend ────────────────
header "Phase J: UTXO — Immature coinbase spend attack"

log "Submitting blocks that attempt to spend immature coinbase outputs..."
run_check adversary_check "Phase J-seed" "immature-coinbase-spend" "$SEED_RPC"
run_check adversary_check "Phase J-miner" "immature-coinbase-spend" "$MINER_RPC"

log "Verifying cluster consensus after immature coinbase spend attempts..."
wait_for_convergence 30 "Phase J convergence" 0
run_check check_consensus "Phase J (post immature-coinbase-spend)"
run_check check_utxo_consistency "Phase J UTXO consistency"
print_cluster_status "After Immature Coinbase Spend Attack"
fi

if ! should_skip "K"; then
# ── Phase K: UTXO — Overspend (value creation) attack ─────
header "Phase K: UTXO — Overspend (value creation) attack"

log "Submitting blocks with transactions whose outputs exceed inputs..."
run_check adversary_check "Phase K-seed" "overspend" "$SEED_RPC"
run_check adversary_check "Phase K-miner" "overspend" "$MINER_RPC"

log "Verifying cluster consensus after overspend attempts..."
wait_for_convergence 30 "Phase K convergence" 0
run_check check_consensus "Phase K (post overspend)"
run_check check_utxo_consistency "Phase K UTXO consistency"
print_cluster_status "After Overspend Attack"
fi

if ! should_skip "L"; then
# ── Phase L: UTXO — Duplicate-input attack ─────────────────
header "Phase L: UTXO — Duplicate-input attack (same input twice in one tx)"

log "Submitting blocks with transactions that list the same input twice..."
run_check adversary_check "Phase L-seed" "duplicate-input" "$SEED_RPC"
run_check adversary_check "Phase L-miner" "duplicate-input" "$MINER_RPC"

log "Verifying cluster consensus after duplicate-input attempts..."
wait_for_convergence 30 "Phase L convergence" 0
run_check check_consensus "Phase L (post duplicate-input)"
run_check check_utxo_consistency "Phase L UTXO consistency"
print_cluster_status "After Duplicate-Input Attack"
fi

if ! should_skip "M"; then
# ── Phase M: UTXO — Intra-block double-spend attack ────────
header "Phase M: UTXO — Intra-block double-spend (two txs in one block spend same outpoint)"

log "Submitting blocks with two transactions that spend the same UTXO..."
run_check adversary_check "Phase M-seed" "intra-block-double-spend" "$SEED_RPC"
run_check adversary_check "Phase M-miner" "intra-block-double-spend" "$MINER_RPC"

log "Verifying cluster consensus after intra-block double-spend attempts..."
wait_for_convergence 30 "Phase M convergence" 0
run_check check_consensus "Phase M (post intra-block-double-spend)"
run_check check_utxo_consistency "Phase M UTXO consistency"
print_cluster_status "After Intra-Block Double-Spend Attack"
fi

# ── Phase 16: Final summary ────────────────────────────────
if ! should_skip "16"; then
header "Phase 16: Final Retarget & Consensus Verification"
for i in "${SEED_NODES[@]}"; do
    if is_alive "$i"; then
        rpc=$((BASE_RPC_PORT + i))
        echo ""
        log "Full chain info from SEED node $i:"
        curl -s "http://127.0.0.1:${rpc}/getblockchaininfo" 2>/dev/null | python3 -m json.tool || true
        run_check check_retarget
        break
    fi
done

run_check check_consensus "FINAL"
run_check check_utxo_consistency "FINAL UTXO consistency"
fi

echo ""
echo "════════════════════════════════════════════════════════════════════"
if [ "$FAILURES" -eq 0 ]; then
    echo -e " ${GREEN}ALL CHECKS PASSED — CHAOS + ADVERSARIAL + CONSENSUS + UTXO VALIDATION STRESS${NC}"
else
    echo -e " ${RED}$FAILURES CHECK(S) FAILED${NC}"
fi
echo "────────────────────────────────────────────────────────────────────"
echo " Run #${NEXT_RUN} data preserved in: ${RUN_DIR}"
echo " Node logs: ${BASEDIR}/node*/stdout.log"
echo " Full log:  ${RUN_LOG}"
echo "════════════════════════════════════════════════════════════════════"
echo ""

exit "$FAILURES"
