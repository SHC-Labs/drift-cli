# drift install bootstrapper for Windows. Detects arch, downloads the
# matching binary from GitHub Releases, drops it at
# %USERPROFILE%\.local\bin\drift.exe, runs `drift install` to register
# the service + write configs.
#
# Usage:
#   iwr -UseBasicParsing https://mcp.driftlabs.io/install.ps1 | iex
#   $env:DRIFT_TOKEN = "drift_xxx"; iwr -UseBasicParsing https://mcp.driftlabs.io/install.ps1 | iex
#
# Verifies SHA-256 checksum of the downloaded archive. Refuses to
# install on mismatch. Cosign signature verification lands once cosign
# is broadly available on Windows; until then the checksum is the
# trust anchor.

$ErrorActionPreference = 'Stop'

$DriftRepo = if ($env:DRIFT_REPO) { $env:DRIFT_REPO } else { 'SHC-Labs/drift-cli' }
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

    if (Test-Path $DriftInstallDir -PathType Leaf) {
        Fatal "DRIFT_INSTALL_DIR ($DriftInstallDir) exists but is a file, not a directory"
    }
    if (-not (Test-Path $DriftInstallDir)) {
        New-Item -ItemType Directory -Path $DriftInstallDir -Force | Out-Null
    }
    $exeSrc = Join-Path $tmpDir 'drift.exe'
    $exeDst = Join-Path $DriftInstallDir 'drift.exe'

    # If a previous drift.exe is at $exeDst and the relay is running, the
    # binary is held open by the running process and Move-Item -Force
    # fails with "Cannot create a file when that file already exists".
    # Tony hit this on the v0.1.10 upgrade. Try a graceful stop via the
    # old binary first; fall back to taskkill so the customer doesn't
    # have to babysit a half-finished install.
    if (Test-Path $exeDst) {
        try {
            & $exeDst relay stop 2>&1 | Out-Null
        } catch { }
        $running = Get-Process -Name 'drift' -ErrorAction SilentlyContinue
        if ($running) {
            Log "stopping running drift.exe processes (PIDs: $($running.Id -join ', '))"
            try { Stop-Process -Name 'drift' -Force -ErrorAction Stop } catch {
                Log "WARNING: could not stop drift.exe: $_"
            }
            # Brief wait so Windows releases the file handle before
            # Move-Item runs. ~500ms is enough on every system tested;
            # capped at 3s with a poll loop in case the OS is slow.
            $deadline = (Get-Date).AddSeconds(3)
            while ((Get-Process -Name 'drift' -ErrorAction SilentlyContinue) -and (Get-Date) -lt $deadline) {
                Start-Sleep -Milliseconds 100
            }
        }
    }

    Move-Item -Path $exeSrc -Destination $exeDst -Force
    Log "installed to $exeDst"

    # Add install dir to User PATH (persistent), broadcast the change
    # so explorer.exe picks up the new value (otherwise newly-spawned
    # PowerShell windows inherit explorer's stale env cache and don't
    # see the new PATH), and update the current session's $env:PATH so
    # `drift install` below + `drift status` from this same window both
    # work without restarting the shell.
    $userPath = [Environment]::GetEnvironmentVariable('PATH', 'User')
    if ($userPath -notlike "*$DriftInstallDir*") {
        $newUserPath = if ($userPath) { "$userPath;$DriftInstallDir" } else { $DriftInstallDir }
        try {
            [Environment]::SetEnvironmentVariable('PATH', $newUserPath, 'User')
            Log "added $DriftInstallDir to User PATH (persistent)"
            # Broadcast WM_SETTINGCHANGE so explorer reloads the User
            # env block. Without this, new shells see the OLD PATH for
            # the rest of the explorer session (until a logout/login or
            # explorer restart). 5s timeout, abort-if-hung flag.
            try {
                if (-not ('Win32.NativeMethods' -as [Type])) {
                    Add-Type -Namespace Win32 -Name NativeMethods -MemberDefinition @'
[System.Runtime.InteropServices.DllImport("user32.dll", SetLastError=true, CharSet=System.Runtime.InteropServices.CharSet.Auto)]
public static extern System.IntPtr SendMessageTimeout(System.IntPtr hWnd, uint Msg, System.UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out System.UIntPtr lpdwResult);
'@
                }
                $HWND_BROADCAST   = [IntPtr]0xffff
                $WM_SETTINGCHANGE = 0x1A
                $SMTO_ABORTIFHUNG = 0x0002
                $result = [UIntPtr]::Zero
                [Win32.NativeMethods]::SendMessageTimeout($HWND_BROADCAST, $WM_SETTINGCHANGE, [UIntPtr]::Zero, 'Environment', $SMTO_ABORTIFHUNG, 5000, [ref]$result) | Out-Null
                Log "broadcast PATH change to running processes"
            } catch {
                Log "WARNING: PATH broadcast failed (new shells may need a logout/login): $_"
            }
        } catch {
            Log "WARNING: could not auto-add $DriftInstallDir to User PATH: $_"
            Log "  Add it manually with:"
            Log "    [Environment]::SetEnvironmentVariable('PATH', `$env:PATH + ';$DriftInstallDir', 'User')"
        }
    }
    if ($env:PATH -notlike "*$DriftInstallDir*") {
        $env:PATH = "$env:PATH;$DriftInstallDir"
    }

    # Hand off to drift quickstart so the customer gets the guided
    # wizard right after the binary lands. PowerShell sessions invoked
    # via `iwr | iex` keep UserInteractive=true, so prompts work
    # without /dev/tty redirection. Non-interactive contexts (CI,
    # automated runners) fall through to plain drift install. The same
    # fallback also lives inside the binary's quickstart command.
    if ([Environment]::UserInteractive) {
        Log 'running drift quickstart (guided setup)'
        & $exeDst quickstart
    } else {
        Log 'running drift install (non-interactive)'
        & $exeDst install
    }
}
finally {
    if (Test-Path $tmpDir) {
        Remove-Item -Recurse -Force $tmpDir
    }
}
