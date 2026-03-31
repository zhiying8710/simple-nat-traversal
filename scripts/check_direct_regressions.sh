#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_HANDOFF_SMOKE="${RUN_HANDOFF_SMOKE:-0}"
RUN_DIRECT_SMOKE="${RUN_DIRECT_SMOKE:-0}"

export HTTP_PROXY=
export HTTPS_PROXY=
export ALL_PROXY=
export http_proxy=
export https_proxy=
export all_proxy=

log() {
  printf '[direct-regressions] %s\n' "$*"
}

run_test() {
  local pattern="$1"
  log "cargo test -p minipunch-agent ${pattern}"
  cargo test -p minipunch-agent "${pattern}" --lib
}

cd "$ROOT_DIR"

run_test inbound_reorder_buffer_releases_contiguous_payloads_after_gap_is_filled
run_test inbound_reorder_buffer_ignores_packets_that_are_too_far_ahead
run_test selective_ack_ranges_compress_contiguous_sequences
run_test selective_ack_ranges_cap_the_number_of_blocks
run_test selective_ack_scoreboard_merges_and_counts_new_coverage
run_test selective_ack_scoreboard_advances_with_cumulative_ack
run_test selective_retransmit_holes_only_include_missing_sequences_before_highest_sack
run_test timed_out_retransmit_prioritizes_selective_hole_before_tail_packet
run_test loss_recovery_selector_prefers_first_selective_hole_during_recovery_progress
run_test loss_recovery_selector_uses_duplicate_ack_budget_to_reach_later_hole
run_test congestion_controller_uses_slow_start_then_congestion_avoidance
run_test timeout_retransmit_selects_oldest_expired_packet_only
run_test partial_ack_during_fast_recovery_retransmits_next_gap
run_test duplicate_acks_trigger_fast_retransmit_for_missing_front_packet
run_test selective_ack_removes_out_of_order_packets_from_outstanding_queue
run_test selective_ack_progress_still_triggers_fast_retransmit_for_front_gap
run_test single_ack_with_three_new_selective_ranges_triggers_fast_retransmit
run_test selective_ack_range_evidence_accumulates_across_ack_updates_for_same_gap
run_test duplicate_ack_budget_can_progress_to_later_selective_hole

log "cargo check --workspace"
cargo check --workspace

if [[ "$RUN_HANDOFF_SMOKE" == "1" ]]; then
  log "running optional handoff smoke"
  bash scripts/smoke_auto_handoff_fallback.sh
fi

if [[ "$RUN_DIRECT_SMOKE" == "1" ]]; then
  log "running optional direct-only smoke"
  bash scripts/smoke_direct_only.sh
fi

log "direct regression checks passed"
