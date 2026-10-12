#!/usr/bin/env bash
# Samples the runner's memory, pressure, and largest processes every 5 s into
# $1, and prints one summary line every 30 s, so a job that stalls or loses
# its runner shows the resource trend up to that point. Times are UTC.
set -u
out=$1
mkdir -p "$(dirname "$out")"
# Pressure stall: the share of the last 10 s in which some, or all, tasks
# waited on that resource.
psi() { [ -r "/proc/pressure/$1" ] || return 0; awk -v k="$1" '{ split($2, a, "="); v[$1]=a[2] } END { printf "%s=%s/%s ", k, v["some"], v["full"] }' "/proc/pressure/$1"; }
i=0
while true; do
  now=$(date -u +%H:%M:%S)
  mem=$(awk '/^MemAvailable/ { a=$2 } /^SwapTotal/ { st=$2 } /^SwapFree/ { sf=$2 } /^Dirty/ { d=$2 }
    END { printf "avail=%dM swap=%dM dirty=%dM", a/1024, (st-sf)/1024, d/1024 }' /proc/meminfo)
  line="$now $mem load=$(cut -d' ' -f1 /proc/loadavg) $(psi cpu)$(psi memory)$(psi io)"
  top=$(ps -eo rss=,comm= --sort=-rss | head -4 | awk '{ printf "%s:%dM ", $2, $1/1024 }')
  {
    echo "$line top: $top"
    if (( i % 6 == 0 )); then
      docker stats --no-stream --format '  {{.Name}} cpu={{.CPUPerc}} mem={{.MemUsage}}' 2>/dev/null
    fi
  } >> "$out"
  if (( i % 6 == 0 )); then
    echo "[monitor] $line top: $top"
  fi
  i=$((i + 1))
  sleep 5
done
