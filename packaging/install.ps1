<#
.SYNOPSIS
  Installs CredOrbit on Windows.

.DESCRIPTION
  Downloads a CredOrbit release from GitHub, verifies its SHA-256 against the
  release's checksums.txt and installs credorbit.exe to
  %LOCALAPPDATA%\Programs\CredOrbit. The user PATH is changed only when
  -AddToPath is given. No administrator rights are needed.

.PARAMETER Version
  Tag to install, e.g. v1.2.3. Default: the latest release.

.PARAMETER Repo
  GitHub repository (owner/name). Default: $env:CREDORBIT_REPO or PiniPP/credorbit.

.PARAMETER InstallDir
  Target directory. Default: %LOCALAPPDATA%\Programs\CredOrbit.

.PARAMETER AddToPath
  Add the install directory to the current user's PATH.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File install.ps1 -AddToPath
#>
[CmdletBinding()]
param(
    [string]$Version = $env:CREDORBIT_VERSION,
    [string]$Repo = $(if ($env:CREDORBIT_REPO) { $env:CREDORBIT_REPO } else { 'PiniPP/credorbit' }),
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\CredOrbit'),
    [switch]$AddToPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

function Say([string]$Message) { Write-Host "credorbit-install: $Message" }

if ($Repo -notmatch '^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+$') {
    throw "Repo must look like owner/name (got '$Repo')"
}

$headers = @{ 'User-Agent' = 'credorbit-installer'; 'Accept' = 'application/vnd.github+json' }
if ($env:GITHUB_TOKEN) { $headers['Authorization'] = "Bearer $($env:GITHUB_TOKEN)" }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default {
        if ($env:PROCESSOR_ARCHITEW6432 -eq 'AMD64') { 'amd64' }
        else { throw "Unsupported architecture '$($env:PROCESSOR_ARCHITECTURE)'" }
    }
}

if (-not $Version) {
    Say "looking up the latest release of $Repo"
    $latest = Invoke-RestMethod -UseBasicParsing -Headers $headers -Uri "https://api.github.com/repos/$Repo/releases/latest"
    $Version = $latest.tag_name
    if (-not $Version) { throw 'Could not determine the latest release; pass -Version.' }
}
if (-not $Version.StartsWith('v')) { $Version = "v$Version" }
$num = $Version.Substring(1)

$archive = "credorbit_${num}_windows_${arch}.zip"
$base = "https://github.com/$Repo/releases/download/$Version"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("credorbit-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null

try {
    Say "downloading $archive ($Version)"
    $zip = Join-Path $tmp $archive
    $sums = Join-Path $tmp 'checksums.txt'
    Invoke-WebRequest -UseBasicParsing -Headers $headers -Uri "$base/$archive" -OutFile $zip
    Invoke-WebRequest -UseBasicParsing -Headers $headers -Uri "$base/checksums.txt" -OutFile $sums

    $expected = $null
    foreach ($line in Get-Content -LiteralPath $sums) {
        $parts = $line -split '\s+', 2
        if ($parts.Count -eq 2 -and $parts[1].TrimStart('*') -eq $archive) { $expected = $parts[0].ToLowerInvariant(); break }
    }
    if (-not $expected) { throw "$archive is not listed in checksums.txt" }
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $zip).Hash.ToLowerInvariant()
    if ($expected -ne $actual) { throw "Checksum mismatch for $archive (expected $expected, got $actual)" }
    Say 'checksum verified'

    $extract = Join-Path $tmp 'x'
    Expand-Archive -LiteralPath $zip -DestinationPath $extract
    $exe = Join-Path $extract 'credorbit.exe'
    if (-not (Test-Path -LiteralPath $exe)) { throw 'Archive does not contain credorbit.exe' }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $dest = Join-Path $InstallDir 'credorbit.exe'
    Copy-Item -LiteralPath $exe -Destination "$dest.new" -Force
    Move-Item -LiteralPath "$dest.new" -Destination $dest -Force
    $completion = Join-Path $extract 'completions\credorbit.ps1'
    if (Test-Path -LiteralPath $completion) {
        Copy-Item -LiteralPath $completion -Destination (Join-Path $InstallDir 'credorbit-completion.ps1') -Force
    }
    Say "installed $dest"
}
finally {
    Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$onPath = @($userPath -split ';' | Where-Object { $_ -and ($_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\')) }).Count -gt 0
if ($AddToPath) {
    if (-not $onPath) {
        $newPath = if ($userPath) { "$userPath;$InstallDir" } else { $InstallDir }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        $env:Path = "$env:Path;$InstallDir"
        Say "added $InstallDir to your user PATH (open a new terminal to pick it up)"
    }
}
elseif (-not $onPath) {
    Say "note: $InstallDir is not on your PATH. Re-run with -AddToPath, or call it by full path."
}

& $dest version
