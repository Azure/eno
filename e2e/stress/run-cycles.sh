#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
cd "$SCRIPT_DIR"

CYCLES=${CYCLES:-5}
WAIT_SECONDS=${WAIT_SECONDS:-10}
KUBECTL=${KUBECTL:-kubectl}
ENO_STRESS=${ENO_STRESS:-./eno-stress}
PLAN=${PLAN:-./plan.yaml}
STATE=${STATE:-./state.json}
RESULTS_FILE=${RESULTS_FILE:-./results.txt}
STATE_HISTORY=${STATE_HISTORY:-./state-history}
KUBECONFIG=${KUBECONFIG:-./e2e-underlay-kubeconfig/kubeconfig-cx-1/kubeconfig-cx-1}

mkdir -p "$STATE_HISTORY"
touch "$RESULTS_FILE"

log() {
	printf '[%s] %s\n' "$(date --iso-8601=seconds)" "$*" | tee -a "$RESULTS_FILE"
}

run_logged() {
	set +e
	"$@" 2>&1 | tee -a "$RESULTS_FILE"
	local status=${PIPESTATUS[0]}
	set -e
	return "$status"
}

stress_namespaces() {
	"$KUBECTL" --kubeconfig "$KUBECONFIG" get namespaces \
		-o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.deletionTimestamp}{"\n"}{end}' |
		awk '$1 ~ /^6a[0-9a-f]{16}$/ {print}'
}

wait_for_no_stress_namespaces() {
	while true; do
		local namespaces
		namespaces=$(stress_namespaces)
		if [[ -z "$namespaces" ]]; then
			log 'No stress namespaces remain.'
			return
		fi

		local total terminating active
		total=$(awk 'NF {count++} END {print count+0}' <<<"$namespaces")
		terminating=$(awk 'NF >= 2 && $2 != "<no" {count++} END {print count+0}' <<<"$namespaces")
		active=$((total - terminating))
		log "Waiting for stress namespaces: total=$total terminating=$terminating active=$active"
		sleep "$WAIT_SECONDS"
	done
}

archive_state() {
	[[ -f "$STATE" ]] || return 0
	local run_id timestamp
	run_id=$(jq -r '.runID // "unknown-run"' "$STATE" 2>/dev/null || printf 'unknown-run')
	timestamp=$(date -u +%Y%m%dT%H%M%SZ)
	mv "$STATE" "$STATE_HISTORY/${run_id}-${timestamp}.json"
}

cleanup_state() {
	[[ -f "$STATE" ]] || return 0
	log "Cleaning namespaces recorded in $STATE"
	run_logged "$ENO_STRESS" cleanup --state "$STATE" --kubeconfig "$KUBECONFIG"
}

on_exit() {
	local status=$?
	trap - EXIT INT TERM
	if [[ -f "$STATE" ]]; then
		cleanup_state || true
	fi
	exit "$status"
}
trap on_exit EXIT INT TERM

if ! [[ "$CYCLES" =~ ^[1-9][0-9]*$ ]]; then
	log "CYCLES must be a positive integer, got: $CYCLES"
	exit 2
fi
if [[ ! -x "$ENO_STRESS" ]]; then
	log "Stress binary is not executable: $ENO_STRESS"
	exit 2
fi
if [[ ! -f "$PLAN" ]]; then
	log "Plan does not exist: $PLAN"
	exit 2
fi
if [[ ! -f "$KUBECONFIG" ]]; then
	log "Kubeconfig does not exist: $KUBECONFIG"
	exit 2
fi

log "Validating plan before $CYCLES cycles."
run_logged "$ENO_STRESS" validate --plan "$PLAN" --kubeconfig "$KUBECONFIG"

if [[ -f "$STATE" ]]; then
	log 'Found existing state; cleaning and archiving it before the first cycle.'
	cleanup_state
	archive_state
fi
wait_for_no_stress_namespaces

successful=0
failed=0
for ((cycle = 1; cycle <= CYCLES; cycle++)); do
	log "========== cycle $cycle/$CYCLES: prepare =========="
	if ! run_logged "$ENO_STRESS" prepare --plan "$PLAN" --state "$STATE" --kubeconfig "$KUBECONFIG"; then
		log "Cycle $cycle prepare failed."
		failed=$((failed + 1))
		cleanup_state || true
		archive_state
		wait_for_no_stress_namespaces
		continue
	fi

	log "========== cycle $cycle/$CYCLES: run =========="
	if run_logged "$ENO_STRESS" run --state "$STATE" --kubeconfig "$KUBECONFIG"; then
		log "Cycle $cycle run completed successfully."
		successful=$((successful + 1))
	else
		log "Cycle $cycle run failed."
		failed=$((failed + 1))
	fi

	log "========== cycle $cycle/$CYCLES: cleanup =========="
	cleanup_state || log "Cycle $cycle cleanup returned an error."
	archive_state
	wait_for_no_stress_namespaces
done

log "All cycles finished: successful=$successful failed=$failed"
trap - EXIT INT TERM