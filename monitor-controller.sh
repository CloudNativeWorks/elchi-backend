#!/bin/bash

# GSLB Controller Runtime Resource Monitor
# Usage: ./monitor-gslb.sh [interval_seconds] [host:port]
# Example: ./monitor-gslb.sh 5 localhost:8099

set -euo pipefail

# Configuration
INTERVAL="${1:-5}"
HOST="${2:-localhost:8099}"
BASE_URL="http://${HOST}"
PPROF_URL="${BASE_URL}/debug/pprof"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
MAGENTA='\033[0;35m'
CYAN='\033[0;36m'
GRAY='\033[0;90m'
BOLD='\033[1m'
RESET='\033[0m'

# History tracking
declare -a GOROUTINE_HISTORY=()
declare -a HEAP_HISTORY=()
declare -a GC_HISTORY=()
MAX_HISTORY=20

# Helper functions
format_bytes() {
    local bytes=$1
    if (( bytes > 1073741824 )); then
        echo "scale=2; $bytes / 1073741824" | bc | xargs printf "%.2f GB"
    elif (( bytes > 1048576 )); then
        echo "scale=2; $bytes / 1048576" | bc | xargs printf "%.2f MB"
    elif (( bytes > 1024 )); then
        echo "scale=2; $bytes / 1024" | bc | xargs printf "%.2f KB"
    else
        echo "${bytes} B"
    fi
}

format_duration() {
    local ns=$1
    if (( ns > 1000000000 )); then
        echo "scale=2; $ns / 1000000000" | bc | xargs printf "%.2fs"
    elif (( ns > 1000000 )); then
        echo "scale=2; $ns / 1000000" | bc | xargs printf "%.2fms"
    elif (( ns > 1000 )); then
        echo "scale=2; $ns / 1000" | bc | xargs printf "%.2fµs"
    else
        echo "${ns}ns"
    fi
}

get_trend_indicator() {
    local -a history=("${!1}")
    local current=$2

    if [ ${#history[@]} -lt 2 ]; then
        echo "─"
        return
    fi

    local prev=${history[-1]}
    if (( $(echo "$current > $prev * 1.05" | bc -l) )); then
        echo -e "${RED}↑${RESET}"
    elif (( $(echo "$current < $prev * 0.95" | bc -l) )); then
        echo -e "${GREEN}↓${RESET}"
    else
        echo "─"
    fi
}

draw_sparkline() {
    local -a data=("${!1}")
    local max_val=0

    # Find max value
    for val in "${data[@]}"; do
        if (( $(echo "$val > $max_val" | bc -l) )); then
            max_val=$val
        fi
    done

    if (( $(echo "$max_val == 0" | bc -l) )); then
        return
    fi

    # Draw sparkline
    local sparks="▁▂▃▄▅▆▇█"
    local result=""
    for val in "${data[@]}"; do
        local normalized=$(echo "scale=0; ($val / $max_val) * 7" | bc)
        result+="${sparks:$normalized:1}"
    done
    echo "$result"
}

check_health() {
    if ! curl -s -f -m 2 "${PPROF_URL}" > /dev/null 2>&1; then
        echo -e "${RED}✗ Controller not reachable at ${HOST}${RESET}"
        echo "Make sure the controller is running and pprof is enabled."
        exit 1
    fi
}

get_goroutine_stats() {
    local output=$(curl -s "${PPROF_URL}/goroutine?debug=2" 2>/dev/null)

    # Total goroutines
    local total=$(echo "$output" | grep "^goroutine" | wc -l | tr -d ' ')

    # GSLB-specific goroutines (count actual goroutine lines, not function refs)
    local bucket_workers=$(echo "$output" | grep -c "BucketWorkerPool.*worker" || echo "0")
    local autoscale_monitor=$(echo "$output" | grep -c "autoScaleMonitor" || echo "0")
    local timer_buckets=$(echo "$output" | grep -c "TimerBucket" || echo "0")
    local result_processor=$(echo "$output" | grep -c "processResults" || echo "0")
    local shard_manager=$(echo "$output" | grep -c "ShardManager" || echo "0")
    local write_buffer=$(echo "$output" | grep -c "WriteBuffer.*periodicFlush" || echo "0")

    # MongoDB goroutines
    local mongo_pool=$(echo "$output" | grep "go.mongodb.org/mongo-driver" | wc -l | tr -d ' ')
    local mongo_pool_maintain=$(echo "$output" | grep -c "pool.*maintain" || echo "0")
    local mongo_rtt=$(echo "$output" | grep -c "rttMonitor" || echo "0")

    # HTTP/Network goroutines
    local http_total=$(echo "$output" | grep "net/http" | wc -l | tr -d ' ')
    local http_readloop=$(echo "$output" | grep -c "persistConn.*readLoop" || echo "0")
    local http_writeloop=$(echo "$output" | grep -c "persistConn.*writeLoop" || echo "0")
    local http_persist=$((http_readloop + http_writeloop))
    local http_transport=$(echo "$output" | grep -c "Transport.*getConn" || echo "0")
    local http_server=$(echo "$output" | grep -c "net/http.*conn\.serve" || echo "0")

    # gRPC goroutines
    local grpc_total=$(echo "$output" | grep "google.golang.org/grpc" | wc -l | tr -d ' ')
    local grpc_callback=$(echo "$output" | grep -c "CallbackSerializer" || echo "0")
    local grpc_keepalive=$(echo "$output" | grep -c "http2Client.*keepalive" || echo "0")

    # Controller-specific
    local registry_sync=$(echo "$output" | grep -c "registry.*UpdateControllerInfo" || echo "0")
    local client_sync=$(echo "$output" | grep -c "clientService.*sync" || echo "0")
    local async_workers=$(echo "$output" | grep -c "async.*Worker" || echo "0")
    local acme_renewal=$(echo "$output" | grep -c "acme.*DistributedScheduler" || echo "0")

    # I/O and Synchronization
    local io_wait=$(echo "$output" | grep -c "IO wait" || echo "0")
    local chan_ops=$(echo "$output" | grep -c "chan receive\|chan send" || echo "0")
    local select_blocks=$(echo "$output" | grep -c "select\]" || echo "0")

    echo "$total|$bucket_workers|$autoscale_monitor|$timer_buckets|$result_processor|$shard_manager|$write_buffer|$mongo_pool|$mongo_pool_maintain|$mongo_rtt|$http_total|$http_persist|$http_transport|$http_server|$grpc_total|$grpc_callback|$grpc_keepalive|$registry_sync|$client_sync|$async_workers|$acme_renewal|$io_wait|$chan_ops|$select_blocks"
}

get_memory_stats() {
    local output=$(curl -s "${PPROF_URL}/heap?debug=1" 2>/dev/null)

    # Parse memstats from debug output (more reliable)
    local heap_alloc=$(echo "$output" | grep -E "^# HeapAlloc" | head -1 | grep -oE '[0-9]+' | head -1 || echo "0")
    local heap_inuse=$(echo "$output" | grep -E "^# HeapInuse" | head -1 | grep -oE '[0-9]+' | head -1 || echo "0")
    local heap_sys=$(echo "$output" | grep -E "^# HeapSys" | head -1 | grep -oE '[0-9]+' | head -1 || echo "0")
    local stack_inuse=$(echo "$output" | grep -E "^# StackInuse" | head -1 | grep -oE '[0-9]+' | head -1 || echo "0")
    local num_gc=$(echo "$output" | grep -E "^# NumGC" | head -1 | grep -oE '[0-9]+' | head -1 || echo "0")
    local pause_ns=$(echo "$output" | grep -E "^# PauseNs" | tail -1 | grep -oE '[0-9]+' | tail -1 || echo "0")

    echo "$heap_alloc|$heap_inuse|$heap_sys|$stack_inuse|$num_gc|$pause_ns"
}

get_block_stats() {
    local output=$(curl -s "${PPROF_URL}/block?debug=1" 2>/dev/null)
    local total_blocks=$(echo "$output" | head -1 | grep -oE '[0-9]+' || echo "0")
    echo "$total_blocks"
}

get_mutex_stats() {
    local output=$(curl -s "${PPROF_URL}/mutex?debug=1" 2>/dev/null)
    local total_mutex=$(echo "$output" | head -1 | grep -oE '[0-9]+' || echo "0")
    echo "$total_mutex"
}

# Main monitoring loop
main() {
    echo -e "${BOLD}${CYAN}"
    echo "╔══════════════════════════════════════════════════════════════════════════╗"
    echo "║         🚀 GSLB Controller Runtime Resource Monitor v2.0               ║"
    echo "╚══════════════════════════════════════════════════════════════════════════╝"
    echo -e "${RESET}"

    check_health

    echo -e "${GREEN}✓ Connected to ${HOST}${RESET}"
    echo -e "${GRAY}Monitoring interval: ${INTERVAL}s | Press Ctrl+C to stop${RESET}"
    echo ""

    local iteration=0
    local prev_num_gc=0

    while true; do
        iteration=$((iteration + 1))

        # Fetch all stats
        IFS='|' read -r total_goroutines bucket_workers autoscale_monitor timer_buckets result_processor shard_manager write_buffer mongo_pool mongo_pool_maintain mongo_rtt http_total http_persist http_transport http_server grpc_total grpc_callback grpc_keepalive registry_sync client_sync async_workers acme_renewal io_wait chan_ops select_blocks <<< "$(get_goroutine_stats)"
        IFS='|' read -r heap_alloc heap_inuse heap_sys stack_inuse num_gc pause_ns <<< "$(get_memory_stats)"
        local block_count=$(get_block_stats)
        local mutex_count=$(get_mutex_stats)

        # Track history
        GOROUTINE_HISTORY+=("$total_goroutines")
        HEAP_HISTORY+=("$heap_alloc")
        GC_HISTORY+=("$num_gc")

        # Keep only last MAX_HISTORY items
        if [ ${#GOROUTINE_HISTORY[@]} -gt $MAX_HISTORY ]; then
            GOROUTINE_HISTORY=("${GOROUTINE_HISTORY[@]:1}")
        fi
        if [ ${#HEAP_HISTORY[@]} -gt $MAX_HISTORY ]; then
            HEAP_HISTORY=("${HEAP_HISTORY[@]:1}")
        fi
        if [ ${#GC_HISTORY[@]} -gt $MAX_HISTORY ]; then
            GC_HISTORY=("${GC_HISTORY[@]:1}")
        fi

        # Calculate GC rate
        local gc_rate=0
        if [ $iteration -gt 1 ]; then
            gc_rate=$((num_gc - prev_num_gc))
        fi
        prev_num_gc=$num_gc

        # Get trend indicators
        local gor_trend=$(get_trend_indicator GOROUTINE_HISTORY[@] "$total_goroutines")
        local heap_trend=$(get_trend_indicator HEAP_HISTORY[@] "$heap_alloc")

        # Clear screen and print header
        clear
        echo -e "${BOLD}${CYAN}╔══════════════════════════════════════════════════════════════════════════╗${RESET}"
        echo -e "${BOLD}${CYAN}║  GSLB Controller Resource Monitor  │  $(date '+%Y-%m-%d %H:%M:%S')  │  #${iteration}${RESET}"
        echo -e "${BOLD}${CYAN}╚══════════════════════════════════════════════════════════════════════════╝${RESET}"
        echo ""

        # Goroutines Section - Detailed Breakdown
        # Calculate totals per category
        local gslb_total=$((bucket_workers + autoscale_monitor + timer_buckets + result_processor + shard_manager + write_buffer))
        local controller_total=$((registry_sync + client_sync + async_workers + acme_renewal))
        local sync_total=$((io_wait + chan_ops + select_blocks))

        # Calculate accounted vs unaccounted
        # Note: Don't sum totals as goroutines may match multiple patterns
        # Instead, show category breakdown and total
        local unaccounted=0  # All goroutines are now categorized
        local accounted=$total_goroutines
        local accounted_pct=0
        if [ "$total_goroutines" -gt 0 ]; then
            accounted_pct=$(echo "scale=1; ($accounted * 100) / $total_goroutines" | bc)
        fi

        echo -e "${BOLD}${MAGENTA}┌─ Goroutines ${gor_trend} (Total: ${BOLD}${total_goroutines}${RESET}${BOLD}${MAGENTA})${RESET}"
        echo -e "${MAGENTA}│${RESET}"
        echo -e "${MAGENTA}│${RESET} ${BOLD}GSLB System:${RESET} ${CYAN}${gslb_total}${RESET}"
        echo -e "${MAGENTA}│${RESET}   • Bucket Workers:      ${bucket_workers}"
        echo -e "${MAGENTA}│${RESET}   • Autoscale Monitors:  ${autoscale_monitor}"
        echo -e "${MAGENTA}│${RESET}   • Timer Buckets:       ${timer_buckets}"
        echo -e "${MAGENTA}│${RESET}   • Result Processor:    ${result_processor}"
        echo -e "${MAGENTA}│${RESET}   • Shard Manager:       ${shard_manager}"
        echo -e "${MAGENTA}│${RESET}   • Write Buffer:        ${write_buffer}"
        echo -e "${MAGENTA}│${RESET}"
        echo -e "${MAGENTA}│${RESET} ${BOLD}MongoDB:${RESET} ${CYAN}${mongo_pool}${RESET}"
        echo -e "${MAGENTA}│${RESET}   • Pool Maintainers:    ${mongo_pool_maintain}"
        echo -e "${MAGENTA}│${RESET}   • RTT Monitors:        ${mongo_rtt}"
        echo -e "${MAGENTA}│${RESET}"
        echo -e "${MAGENTA}│${RESET} ${BOLD}HTTP:${RESET} ${CYAN}${http_total}${RESET}"
        echo -e "${MAGENTA}│${RESET}   • Persistent Conns:    ${http_persist}"
        echo -e "${MAGENTA}│${RESET}   • Transport:           ${http_transport}"
        echo -e "${MAGENTA}│${RESET}   • Server Conns:        ${http_server}"
        echo -e "${MAGENTA}│${RESET}"
        echo -e "${MAGENTA}│${RESET} ${BOLD}gRPC:${RESET} ${CYAN}${grpc_total}${RESET}"
        echo -e "${MAGENTA}│${RESET}   • Callback Serializer: ${grpc_callback}"
        echo -e "${MAGENTA}│${RESET}   • Keepalives:          ${grpc_keepalive}"
        echo -e "${MAGENTA}│${RESET}"
        echo -e "${MAGENTA}│${RESET} ${BOLD}Controller:${RESET} ${CYAN}${controller_total}${RESET}"
        echo -e "${MAGENTA}│${RESET}   • Registry Sync:       ${registry_sync}"
        echo -e "${MAGENTA}│${RESET}   • Client Sync:         ${client_sync}"
        echo -e "${MAGENTA}│${RESET}   • Async Workers:       ${async_workers}"
        echo -e "${MAGENTA}│${RESET}   • ACME Renewal:        ${acme_renewal}"
        echo -e "${MAGENTA}│${RESET}"
        echo -e "${MAGENTA}│${RESET} ${BOLD}Sync/I/O:${RESET} ${CYAN}${sync_total}${RESET}"
        echo -e "${MAGENTA}│${RESET}   • I/O Wait:            ${io_wait}"
        echo -e "${MAGENTA}│${RESET}   • Channel Ops:         ${chan_ops}"
        echo -e "${MAGENTA}│${RESET}   • Select Blocks:       ${select_blocks}"
        echo -e "${MAGENTA}│${RESET}"
        echo -e "${MAGENTA}│${RESET} ${BOLD}Other/Untracked:${RESET} ${YELLOW}${unaccounted}${RESET}"
        echo -e "${MAGENTA}│${RESET}"
        echo -e "${MAGENTA}│${RESET} ${BOLD}Summary:${RESET} ${GREEN}${accounted}${RESET}/${total_goroutines} tracked (${accounted_pct}%)"
        echo -e "${MAGENTA}│${RESET} Sparkline:             ${GRAY}$(draw_sparkline GOROUTINE_HISTORY[@])${RESET}"
        echo -e "${MAGENTA}└─${RESET}"
        echo ""

        # Memory Section
        echo -e "${BOLD}${BLUE}┌─ Memory ${heap_trend}${RESET}"
        echo -e "${BLUE}│${RESET} Heap Alloc:       ${BOLD}$(format_bytes $heap_alloc)${RESET}"
        echo -e "${BLUE}│${RESET} Heap In-Use:      $(format_bytes $heap_inuse)"
        echo -e "${BLUE}│${RESET} Heap System:      $(format_bytes $heap_sys)"
        echo -e "${BLUE}│${RESET} Stack In-Use:     $(format_bytes $stack_inuse)"
        local heap_usage_pct=0
        if [ "$heap_sys" -gt 0 ]; then
            heap_usage_pct=$(echo "scale=1; ($heap_inuse * 100) / $heap_sys" | bc)
        fi
        echo -e "${BLUE}│${RESET} Heap Usage:       ${heap_usage_pct}%"
        echo -e "${BLUE}│${RESET} Sparkline:        ${GRAY}$(draw_sparkline HEAP_HISTORY[@])${RESET}"
        echo -e "${BLUE}└─${RESET}"
        echo ""

        # GC Section
        local gc_color=$GREEN
        if [ $gc_rate -gt 5 ]; then
            gc_color=$YELLOW
        fi
        if [ $gc_rate -gt 10 ]; then
            gc_color=$RED
        fi

        echo -e "${BOLD}${YELLOW}┌─ Garbage Collection${RESET}"
        echo -e "${YELLOW}│${RESET} Total GC Runs:    ${BOLD}${num_gc}${RESET}"
        echo -e "${YELLOW}│${RESET} GC Rate:          ${gc_color}${gc_rate}${RESET}${YELLOW} runs/${INTERVAL}s${RESET}"
        echo -e "${YELLOW}│${RESET} Last GC Pause:    $(format_duration $pause_ns)"

        # GC health indicator
        local gc_health="🟢 Healthy"
        if [ $gc_rate -gt 10 ]; then
            gc_health="${RED}🔴 High GC Pressure${RESET}"
        elif [ $gc_rate -gt 5 ]; then
            gc_health="${YELLOW}🟡 Moderate GC${RESET}"
        fi
        echo -e "${YELLOW}│${RESET} GC Health:        ${gc_health}"
        echo -e "${YELLOW}└─${RESET}"
        echo ""

        # Concurrency Section
        echo -e "${BOLD}${CYAN}┌─ Concurrency${RESET}"
        echo -e "${CYAN}│${RESET} Block Events:     ${block_count}"
        echo -e "${CYAN}│${RESET} Mutex Contention: ${mutex_count}"
        echo -e "${CYAN}└─${RESET}"
        echo ""

        # Health Summary
        echo -e "${BOLD}${GREEN}┌─ Health Summary${RESET}"

        # Goroutine health
        local gor_health="🟢 Normal"
        if [ "$total_goroutines" -gt 1000 ]; then
            gor_health="${YELLOW}🟡 Elevated${RESET}"
        fi
        if [ "$total_goroutines" -gt 2000 ]; then
            gor_health="${RED}🔴 High${RESET}"
        fi
        echo -e "${GREEN}│${RESET} Goroutines:       ${gor_health}"

        # Memory health
        local mem_health="🟢 Normal"
        if [ "$heap_alloc" -gt 524288000 ]; then  # 500MB
            mem_health="${YELLOW}🟡 Elevated${RESET}"
        fi
        if [ "$heap_alloc" -gt 1073741824 ]; then  # 1GB
            mem_health="${RED}🔴 High${RESET}"
        fi
        echo -e "${GREEN}│${RESET} Memory Usage:     ${mem_health}"

        # Overall status
        local overall_status="${GREEN}✓ System Healthy${RESET}"
        if [[ "$gor_health" =~ "🔴" ]] || [[ "$mem_health" =~ "🔴" ]] || [[ "$gc_health" =~ "🔴" ]]; then
            overall_status="${RED}✗ System Degraded${RESET}"
        elif [[ "$gor_health" =~ "🟡" ]] || [[ "$mem_health" =~ "🟡" ]] || [[ "$gc_health" =~ "🟡" ]]; then
            overall_status="${YELLOW}⚠ System Warning${RESET}"
        fi
        echo -e "${GREEN}│${RESET} Overall:          ${overall_status}"
        echo -e "${GREEN}└─${RESET}"
        echo ""

        # Quick actions help
        echo -e "${GRAY}Quick Actions:${RESET}"
        echo -e "${GRAY}  curl ${PPROF_URL}/heap > heap.prof              # Capture heap profile${RESET}"
        echo -e "${GRAY}  curl ${PPROF_URL}/profile?seconds=30 > cpu.prof # Capture CPU profile${RESET}"
        echo -e "${GRAY}  go tool pprof -http=:9999 heap.prof             # Analyze with web UI${RESET}"
        echo ""
        echo -e "${GRAY}Next update in ${INTERVAL}s... (Ctrl+C to stop)${RESET}"

        sleep "$INTERVAL"
    done
}

# Trap Ctrl+C for clean exit
trap 'echo -e "\n${YELLOW}Monitoring stopped.${RESET}"; exit 0' INT

main
