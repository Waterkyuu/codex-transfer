$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repositoryRoot = Split-Path -Parent $PSScriptRoot
$temporaryDir = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $temporaryDir | Out-Null
try {
    $architecture = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
    if ($architecture -eq 'x64') { $architecture = 'amd64' }
    $asset = "codex-transfer_1.2.3_windows_$architecture.zip"
    $releaseDir = Join-Path $temporaryDir 'release'
    $packageDir = Join-Path $temporaryDir 'package'
    $installDir = Join-Path $temporaryDir 'installed'
    New-Item -ItemType Directory -Path $releaseDir, $packageDir | Out-Null
    Set-Content -LiteralPath (Join-Path $packageDir 'codex-transfer.exe') -Value 'fixture binary'
    $archivePath = Join-Path $releaseDir $asset
    Compress-Archive -Path (Join-Path $packageDir 'codex-transfer.exe') -DestinationPath $archivePath
    $checksum = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    Set-Content -LiteralPath (Join-Path $releaseDir 'checksums.txt') -Value "$checksum  $asset"

    # Downloads stay inside the fixture; PATH and real installation locations are untouched.
    function Invoke-WebRequest {
        param([string]$Uri, [string]$OutFile, [int]$TimeoutSec, [switch]$UseBasicParsing)
        $source = Join-Path $releaseDir ([IO.Path]::GetFileName(([Uri]$Uri).AbsolutePath))
        Copy-Item -LiteralPath $source -Destination $OutFile
    }
    & (Join-Path $repositoryRoot 'scripts/install.ps1') -Version v1.2.3 -InstallDir $installDir -ReleaseBaseUrl https://example.test/download -NoPath
    $binary = Join-Path $installDir 'codex-transfer.exe'
    if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) { throw 'Binary not installed.' }
    $before = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash
    Add-Content -LiteralPath $archivePath -Value 'tampered'
    try {
        & (Join-Path $repositoryRoot 'scripts/install.ps1') -Version v1.2.3 -InstallDir $installDir -ReleaseBaseUrl https://example.test/download -NoPath
        throw 'Corrupt archive was accepted.'
    } catch {
        if ($_.Exception.Message -notmatch 'SHA-256 mismatch') { throw }
    }
    if ((Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash -ne $before) { throw 'Failed update changed the installed binary.' }
    Write-Output 'PowerShell installer checks passed.'
} finally {
    Remove-Item -LiteralPath $temporaryDir -Recurse -Force
}
