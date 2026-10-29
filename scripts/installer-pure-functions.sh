#!/usr/bin/env bash
# Extract only named, reviewed pure functions. Never source install.sh itself.
installer_pure_functions() {
  local source="$1"; shift
  [[ "$(tail -n 1 "$source")" == 'main "$@"' ]] || { printf 'FAIL install.sh must end with main "$@"\n' >&2; return 1; }
  awk -v names="$*" '
    BEGIN {n=split(names,a," "); for(i=1;i<=n;i++) wanted[a[i]]=1}
    /^readonly DEFAULT_VERSION=/ {print}
    /^[a-zA-Z_][a-zA-Z_0-9]*\(\) *\{/ {
      name=$1; sub(/\(\)$/, "", name);
      copy=0;
      if(name in wanted) {count[name]++; copy=count[name]==1}
    }
    copy {print}
    copy && /^\}/ {copy=0}
    END {
      for(name in wanted) {
        # The second die is the known helper heredoc, not installer scope.
        expected=name=="die"?2:1;
        if(count[name]!=expected) {printf "FAIL ambiguous/missing function %s: %d definitions (expected %d)\n",name,count[name],expected > "/dev/stderr"; exit 1}
      }
    }' "$source"
}
