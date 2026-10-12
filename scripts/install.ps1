param(
    [string]$Version = $env:CODEX_TRANSFER_VERSION,
    [string]$InstallDir = $env:CODEX_TRANSFER_INSTALL_DIR,
    [string]$ReleaseBaseUrl = $env:CODEX_TRANSFER_RELEASE_BASE_URL,
    [switch]$NoPath
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$architecture = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
if ($architecture -eq 'x64') { $architecture = 'amd64' }
if ($architecture -notin @('amd64', 'arm64')) { throw "Unsupported Windows architecture: $architecture" }
if (-not $Version) {
    $release = Invoke-RestMethod -Uri 'https://api.github.com/repos/Waterkyuu/codex-transfer/releases/latest' -TimeoutSec 60
    $Version = $release.tag_name
}
if ($Version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$') { throw "No valid published release found: $Version" }
if (-not $ReleaseBaseUrl) { $ReleaseBaseUrl = 'https://github.com/Waterkyuu/codex-transfer/releases/download' }
if (-not $InstallDir) {
    if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA is unavailable; specify -InstallDir.' }
    $InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\codex-transfer'
}
$InstallDir = [IO.Path]::GetFullPath($InstallDir)
$asset = "codex-transfer_$($Version.Substring(1))_windows_$architecture.zip"
$releaseUrl = "$($ReleaseBaseUrl.TrimEnd('/'))/$Version"
$temporaryDir = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $temporaryDir | Out-Null
$staged = $null
try {
    $archivePath = Join-Path $temporaryDir $asset
    $checksumPath = Join-Path $temporaryDir 'checksums.txt'
    Invoke-WebRequest -Uri "$releaseUrl/checksums.txt" -OutFile $checksumPath -TimeoutSec 120 -UseBasicParsing
    Invoke-WebRequest -Uri "$releaseUrl/$asset" -OutFile $archivePath -TimeoutSec 120 -UseBasicParsing
    $lines = @(Get-Content -LiteralPath $checksumPath | Where-Object { $_ -match "^[a-fA-F0-9]{64}\s+\*?$([regex]::Escape($asset))$" })
    if ($lines.Count -ne 1) { throw "No unique valid SHA-256 checksum for $asset." }
    $expected = $lines[0].Substring(0, 64).ToLowerInvariant()
    $actual = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected) { throw "SHA-256 mismatch for $asset; previous installation retained." }

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [IO.Compression.ZipFile]::OpenRead($archivePath)
    $binarySource = Join-Path $temporaryDir 'codex-transfer.exe'
    try {
        $entries = @($zip.Entries | Where-Object { $_.FullName -ceq 'codex-transfer.exe' })
        if ($entries.Count -ne 1) { throw 'Missing or duplicate codex-transfer.exe in archive.' }
        [IO.Compression.ZipFileExtensions]::ExtractToFile($entries[0], $binarySource)
    } finally { $zip.Dispose() }

    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    $binaryPath = Join-Path $InstallDir 'codex-transfer.exe'
    if (Test-Path -LiteralPath $binaryPath) {
        $existing = Get-Item -LiteralPath $binaryPath -Force
        if ($existing.PSIsContainer -or ($existing.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            throw 'Existing destination is a directory or link; use its original package manager.'
        }
    }
    $staged = Join-Path $InstallDir ([IO.Path]::GetRandomFileName())
    Copy-Item -LiteralPath $binarySource -Destination $staged
    if (Test-Path -LiteralPath $binaryPath) { [IO.File]::Replace($staged, $binaryPath, $null) }
    else { [IO.File]::Move($staged, $binaryPath) }
    $staged = $null

    if (-not $NoPath) {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        $entries = if ($userPath) { @($userPath -split ';' | ForEach-Object { $_.TrimEnd('\') }) } else { @() }
        if ($entries -notcontains $InstallDir.TrimEnd('\')) {
            $newPath = if ($userPath) { "$userPath;$InstallDir" } else { $InstallDir }
            [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        }
        if (@($env:Path -split ';') -notcontains $InstallDir) { $env:Path = "$env:Path;$InstallDir" }
    }
    Write-Output "Installed codex-transfer $Version to $InstallDir. Open a new terminal to run codex-transfer."
} finally {
    if ($staged -and (Test-Path -LiteralPath $staged)) { Remove-Item -LiteralPath $staged -Force }
    Remove-Item -LiteralPath $temporaryDir -Recurse -Force
}
