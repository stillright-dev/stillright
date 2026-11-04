#!/usr/bin/env bash
# Runs every scenario in $SR_SCENARIOS (a directory or a single file) with
# stillright, writing reports to $SR_REPORTS_DIR and a Markdown summary to
# $GITHUB_STEP_SUMMARY when set. Used by action.yml; runnable locally.
#
# Exit code: 2 if any scenario could not be verified, else 1 if any failed,
# else 0. Every scenario runs even after one fails.
set -uo pipefail

scenarios="${SR_SCENARIOS:-stillright}"
reports="${SR_REPORTS_DIR:-stillright-reports}"
bin="${SR_BIN:-stillright}"
summary="${GITHUB_STEP_SUMMARY:-}"
read -r -a extra <<< "${SR_ARGS:-}"

files=()
if [[ -f "$scenarios" ]]; then
  files=("$scenarios")
elif [[ -d "$scenarios" ]]; then
  while IFS= read -r -d '' f; do files+=("$f"); done < <(find "$scenarios" -maxdepth 1 -type f \( -name '*.yaml' -o -name '*.yml' \) -print0 | sort -z)
fi
if [[ ${#files[@]} -eq 0 ]]; then
  echo "stillright: no scenarios found in '$scenarios' (expected *.yaml files)" >&2
  [[ -n "$summary" ]] && printf '### ⚠️ stillright: no scenarios found in `%s`\n\n' "$scenarios" >> "$summary"
  exit 2
fi

mkdir -p "$reports"
worst=0
for f in "${files[@]}"; do
  name="$(basename "${f%.*}")"
  echo "::group::stillright run $f"
  summary_args=()
  [[ -n "$summary" ]] && summary_args=(--summary "$summary")
  "$bin" run "$f" \
    --report-json "$reports/$name.json" \
    --report-html "$reports/$name.html" \
    ${summary_args[@]+"${summary_args[@]}"} ${extra[@]+"${extra[@]}"}
  code=$?
  echo "::endgroup::"
  case "$code" in
    0) echo "PASS  $f" ;;
    1) echo "::error file=$f::stillright: an invariant or effect failed in $f (see the job summary and $reports/$name.html)"
       [[ $worst -eq 0 ]] && worst=1 ;;
    *) echo "::error file=$f::stillright: $f could not be verified (exit $code)"
       worst=2 ;;
  esac
done

[[ -n "${GITHUB_OUTPUT:-}" ]] && echo "exit-code=$worst" >> "$GITHUB_OUTPUT"
exit "$worst"
