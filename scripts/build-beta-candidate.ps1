param([Parameter(Mandatory=$true)][string]$OutputDirectory)
$ErrorActionPreference='Stop'
$repoRoot=(Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Set-Location -LiteralPath $repoRoot
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Use a fresh candidate output directory; existing evidence is never overwritten.' }
$version='v1.0.1-beta.1'
$commit=(& git rev-parse HEAD).Trim()
$branch=(& git branch --show-current).Trim()
if($branch -ne 'codex/v1.0.1-m01'){throw 'Unexpected construction branch'}
$go=Join-Path $repoRoot '.tools/go/bin/go.exe'
$env:GOCACHE=Join-Path $repoRoot '.tools/go-build-cache'
$env:GOMODCACHE=Join-Path $repoRoot '.tools/go-mod-cache'
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
$assetPaths=@()
$previousGOOS=$env:GOOS; $previousGOARCH=$env:GOARCH; $previousCGO=$env:CGO_ENABLED
try {
 $env:CGO_ENABLED='0'
 foreach($arch in @('amd64','arm64')) {
  foreach($role in @('server','agent')) {
   $env:GOOS='linux';$env:GOARCH=$arch
   $asset=Join-Path $OutputDirectory "404-probe-$role-linux-$arch"
   & $go build -buildvcs=true -trimpath "-ldflags=-s -w -X 404-probe/internal/buildinfo.Version=$version -X 404-probe/internal/buildinfo.Commit=$commit" -o $asset "./cmd/$role"
   if($LASTEXITCODE -ne 0){throw "Candidate build failed: $asset"}
   $metadata=(& $go version -m $asset) -join "`n"
   if(!$metadata.Contains("vcs.revision=$commit") -or !$metadata.Contains('vcs.modified=true')){throw 'This pre-commit candidate must honestly retain dirty VCS metadata'}
   [IO.File]::WriteAllText("$asset.buildinfo.txt",$metadata+[Environment]::NewLine)
   $assetPaths+=$asset
  }
 }
 $env:GOOS='windows';$env:GOARCH='amd64'
 $local=Join-Path $OutputDirectory 'server-dirty-local.exe'
 & $go build -buildvcs=true "-ldflags=-X 404-probe/internal/buildinfo.Version=$version -X 404-probe/internal/buildinfo.Commit=$commit" -o $local ./cmd/server
 if($LASTEXITCODE -ne 0){throw 'Local candidate build failed'}
 $actual=(& $local version --json) | ConvertFrom-Json
 if(!$actual.dirty -or $actual.version -ne 'dirty'){throw 'Dirty candidate identity was concealed'}
 $env:GOOS=$previousGOOS;$env:GOARCH=$previousGOARCH
 & $go run ./cmd/release-metadata --version $version --commit $commit --output $OutputDirectory @assetPaths
 if($LASTEXITCODE -ne 0){throw 'Candidate metadata generation failed'}
 Copy-Item -LiteralPath (Join-Path $repoRoot 'install.sh') -Destination (Join-Path $OutputDirectory 'install.sh')
 $installerSHA=(Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $OutputDirectory 'install.sh')).Hash.ToLowerInvariant()
 [IO.File]::AppendAllText((Join-Path $OutputDirectory 'SHA256SUMS'),"$installerSHA  install.sh`n")
 $identity=[ordered]@{requested_version=$version;branch=$branch;head=$commit;actual_local_version=$actual;dirty=$true;release_ready=$false;local_sha256=(Get-FileHash -Algorithm SHA256 -LiteralPath $local).Hash.ToLowerInvariant();formal_gate='clean worktree + real approved commit + exact release tag pointing to HEAD + clean VCS binaries; rerun build-release-assets.sh after explicit publish approval'}
 [IO.File]::WriteAllText((Join-Path $OutputDirectory 'CANDIDATE-IDENTITY.json'),($identity | ConvertTo-Json -Depth 8))
 [IO.File]::WriteAllText((Join-Path $OutputDirectory 'NOT-FOR-RELEASE.md'),"Engineering test candidates from a dirty worktree. NOT installable trusted releases. Metadata/checksums are preparation previews. Do not upload these assets; rebuild after the approved commit/tag and clean VCS gate.`n")
} finally {$env:GOOS=$previousGOOS;$env:GOARCH=$previousGOARCH;$env:CGO_ENABLED=$previousCGO}
