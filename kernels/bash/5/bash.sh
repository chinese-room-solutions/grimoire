#!/usr/bin/env bash
# Bash kernel runner — the host side of Grimoire's NDJSON kernel protocol.
#
# Loop: read one request (an id line, then a base64 line of the block's code) from
# stdin, run the code in THIS shell so variables/cwd persist across blocks like
# notebook cells, then emit the block's output and exit status as NDJSON events on
# stdout. The host reads those events until the terminal "exit".
#
# Output model: stdout and stderr are merged into one stream in write order, like a
# terminal, so the output reads chronologically. (stderr isn't shown as a separate
# colour — "stderr" doesn't mean "error" across languages; the exit-status footer
# carries success/failure.) Captured to a temp file and emitted when the block
# finishes, which keeps framing trivial and the ordering correct.

# json_escape STRING — print STRING as a JSON string value (without quotes),
# escaping backslash, double-quote, and control characters. Reads its argument so
# embedded newlines survive.
json_escape() {
  local s=$1 out= i c
  for (( i=0; i<${#s}; i++ )); do
    c=${s:i:1}
    case $c in
      '\') out+='\\' ;;
      '"') out+='\"' ;;
      $'\n') out+='\n' ;;
      $'\r') out+='\r' ;;
      $'\t') out+='\t' ;;
      *)
        # Escape remaining control characters (0x00-0x1f) as \u00XX.
        if [[ $c < $'\x20' ]]; then
          printf -v out '%s\\u%04x' "$out" "'$c"
        else
          out+=$c
        fi
        ;;
    esac
  done
  printf '%s' "$out"
}

# emit_output ID FILE — emit the block's captured output as one event, if any.
emit_output() {
  local id=$1 file=$2 data
  [[ -s $file ]] || return 0
  data=$(cat "$file"; printf x); data=${data%x}   # preserve trailing newlines
  printf '{"id":"%s","type":"output","data":"%s"}\n' "$id" "$(json_escape "$data")"
}

out=$(mktemp)
trap 'rm -f "$out"' EXIT

while IFS= read -r id; do
  IFS= read -r b64 || break
  code=$(printf '%s' "$b64" | base64 --decode 2>/dev/null)

  start=$(date +%s%3N 2>/dev/null || echo 0)
  : >"$out"
  # Merge stderr into stdout so the output keeps its chronological order.
  eval "$code" >"$out" 2>&1 </dev/null
  rc=$?
  end=$(date +%s%3N 2>/dev/null || echo 0)
  dur=$(( end - start )); (( dur < 0 )) && dur=0

  emit_output "$id" "$out"
  printf '{"id":"%s","type":"exit","code":%d,"dur_ms":%d}\n' "$id" "$rc" "$dur"
done
