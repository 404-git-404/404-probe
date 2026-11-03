param(
  [Parameter(Mandatory=$true)][string]$Evidence,
  [Parameter(Mandatory=$true)][string]$Baseline,
  [Parameter(Mandatory=$true)][string]$Candidate,
  [Parameter(Mandatory=$true)][string]$Go,
  [int]$Rounds=3
)
$ErrorActionPreference='Stop'
if($Rounds -lt 3){throw 'At least three alternating pairs are required'}
$repo=Split-Path -Parent $PSScriptRoot
$output=Join-Path $Evidence 'pairs'
New-Item -ItemType Directory -Force $output | Out-Null
Set-Location -LiteralPath $repo
$env:GOPROXY='off'
$env:V110_GO=$Go
$env:V110_PERF_COUNT='200'
$env:V110_TEMP_ROOT=$output
$order=@()
foreach($mode in @('cards','list')){
  $env:V110_VIEW=$mode
  for($round=1;$round -le $Rounds;$round++){
    foreach($variant in @('baseline','current')){
      # Inspect only process identities/script names; never log raw command lines.
      $busy=@(Get-CimInstance Win32_Process | Where-Object {
        $_.Name -eq 'go.exe' -or $_.Name -match '\.test\.exe$' -or
        ($_.Name -match '^(node|python)\.exe$' -and $_.CommandLine -match '(scripts[\\/](test-|measure-|capture-)|[\\/](vitest|jest|webpack|vite|tsc)([\\/]|\.[cm]?js))')
      } | ForEach-Object {[PSCustomObject]@{pid=$_.ProcessId;name=$_.Name}})
      $stem='{0}-{1:D2}-{2}' -f $mode,$round,$variant
      [PSCustomObject]@{utc=[DateTime]::UtcNow.ToString('o');otherTasks=$busy} |
        ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $output "$stem-quiescence.json") -Encoding utf8
      if($busy.Count){throw "Other task processes detected before $stem"}
      $env:V110_SOURCE=if($variant -eq 'baseline'){$Baseline}else{$Candidate}
      if((git -C $env:V110_SOURCE status --porcelain)){throw "Dirty source: $variant"}
      $env:V110_PERF_OUTPUT=Join-Path $output "$stem.json"
      if(Test-Path -LiteralPath $env:V110_PERF_OUTPUT){throw "Do not overwrite an earlier result: $stem"}
      node (Join-Path $PSScriptRoot 'measure-v110-dashboard-browser.cjs') *> (Join-Path $output "$stem.log")
      $code=$LASTEXITCODE
      $order+=[PSCustomObject]@{mode=$mode;round=$round;variant=$variant;exit=$code}
      $order | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath (Join-Path $output 'execution-order.json') -Encoding utf8
      Write-Output "$stem exit=$code"
      if($code -ne 0){exit $code}
    }
  }
}
