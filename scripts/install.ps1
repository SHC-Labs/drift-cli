# drift install bootstrapper for Windows. Detects arch, downloads the
# matching binary from GitHub Releases, drops it at
# %USERPROFILE%\.local\bin\drift.exe, runs `drift install` to register
# the service + write configs.
#
# Usage:
#   iwr https://mcp.driftlabs.io/install.ps1 | iex
#   $env:DRIFT_TOKEN = "drift_v1_xxx"; iwr https://mcp.driftlabs.io/install.ps1 | iex
#
# Verifies SHA-256 checksum of the downloaded archive. Refuses to
# install on mismatch. Cosign signature verification lands once cosign
# is broadly available on Windows; until then the checksum is the
# trust anchor.

$ErrorActionPreference = 'Stop'

$DriftRepo = if ($env:DRIFT_REPO) { $env:DRIFT_REPO } else { 'SHC-Labs/drift' }
$DriftVersion = if ($env:DRIFT_VERSION) { $env:DRIFT_VERSION } else { 'latest' }
$DriftInstallDir = if ($env:DRIFT_INSTALL_DIR) { $env:DRIFT_INSTALL_DIR } else { "$env:USERPROFILE\.local\bin" }

function Log($msg) { Write-Host "drift install: $msg" }
function Fatal($msg) { Write-Error "drift install: $msg"; exit 1 }

# Detect arch. Windows ARM64 is deferred per the goreleaser ignore
# list; amd64 is the only target shipped in v1.
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { Fatal 'Windows ARM64 not yet supported. Track at github.com/SHC-Labs/drift/issues' }
    default { Fatal "unsupported arch: $($env:PROCESSOR_ARCHITECTURE)" }
}
Log "detected: windows/$arch"

# Resolve version.
if ($DriftVersion -eq 'latest') {
    $rel = Invoke-RestMethod "https://api.github.com/repos/$DriftRepo/releases/latest"
    $DriftVersion = $rel.tag_name
    if (-not $DriftVersion) { Fatal 'could not resolve latest version from GitHub' }
}
$versionNum = $DriftVersion -replace '^v', ''

$archive = "drift_${versionNum}_windows_${arch}.zip"
$baseUrl = "https://github.com/$DriftRepo/releases/download/$DriftVersion"
$archiveUrl = "$baseUrl/$archive"
$checksumsUrl = "$baseUrl/checksums.txt"

$tmpDir = Join-Path $env:TEMP "drift-install-$(Get-Random)"
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null
try {
    Log "downloading $archiveUrl"
    $archivePath = Join-Path $tmpDir $archive
    Invoke-WebRequest -Uri $archiveUrl -OutFile $archivePath -UseBasicParsing

    Log 'verifying checksum'
    $checksumsPath = Join-Path $tmpDir 'checksums.txt'
    Invoke-WebRequest -Uri $checksumsUrl -OutFile $checksumsPath -UseBasicParsing
    $expected = (Get-Content $checksumsPath | Select-String "  $archive$").ToString().Split(' ')[0]
    if (-not $expected) { Fatal "no checksum for $archive in checksums.txt" }
    $actual = (Get-FileHash -Algorithm SHA256 -Path $archivePath).Hash.ToLower()
    if ($actual -ne $expected.ToLower()) {
        Fatal "checksum mismatch: got $actual, want $expected"
    }
    Log 'checksum verified'

    Log 'extracting'
    Expand-Archive -Path $archivePath -DestinationPath $tmpDir -Force

    if (-not (Test-Path $DriftInstallDir)) {
        New-Item -ItemType Directory -Path $DriftInstallDir -Force | Out-Null
    }
    $exeSrc = Join-Path $tmpDir 'drift.exe'
    $exeDst = Join-Path $DriftInstallDir 'drift.exe'
    Move-Item -Path $exeSrc -Destination $exeDst -Force
    Log "installed to $exeDst"

    # PATH advice. Power users often install to a custom dir not in PATH.
    $userPath = [Environment]::GetEnvironmentVariable('PATH', 'User')
    if ($userPath -notlike "*$DriftInstallDir*") {
        Log "WARNING: $DriftInstallDir is not in your User PATH. Add via:"
        Log "  [Environment]::SetEnvironmentVariable('PATH', `$env:PATH + ';$DriftInstallDir', 'User')"
    }

    Log 'running drift install'
    & $exeDst install
}
finally {
    if (Test-Path $tmpDir) {
        Remove-Item -Recurse -Force $tmpDir
    }
}
