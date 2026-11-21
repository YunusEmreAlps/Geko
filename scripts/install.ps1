# Install Geko on Windows from its GitHub release:
#
#   irm https://raw.githubusercontent.com/YunusEmreAlps/geko/master/scripts/install.ps1 | iex
#
# GEKO_VERSION picks a release (default: the latest) and GEKO_INSTALL_DIR the
# directory (default: %LOCALAPPDATA%\Programs\Geko), which is added to the
# user PATH unless GEKO_NO_MODIFY_PATH is set. The archive is checked against
# the release's checksums.txt before anything is installed.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$repo = 'YunusEmreAlps/geko'

$version = $env:GEKO_VERSION
if (-not $version) {
    $version = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest").tag_name
}
$version = $version.TrimStart('v')
$arch = 'amd64'
if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { $arch = 'arm64' }
$archive = "geko_${version}_windows_$arch.zip"
$base = "https://github.com/$repo/releases/download/v$version"
$dir = $env:GEKO_INSTALL_DIR
if (-not $dir) { $dir = Join-Path $env:LOCALAPPDATA 'Programs\Geko' }

$work = Join-Path ([IO.Path]::GetTempPath()) ("geko-install-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $work | Out-Null
try {
    Write-Host "Downloading $archive"
    $zip = Join-Path $work $archive
    Invoke-WebRequest "$base/$archive" -OutFile $zip -UseBasicParsing
    $sums = (Invoke-WebRequest "$base/checksums.txt" -UseBasicParsing).Content
    if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }
    $expected = $null
    foreach ($line in ($sums -split "`n")) {
        $fields = $line.Trim() -split '\s+'
        if ($fields.Count -eq 2 -and $fields[1] -eq $archive) { $expected = $fields[0] }
    }
    if (-not $expected) { throw "checksums.txt does not list $archive" }
    $actual = (Get-FileHash -Algorithm SHA256 $zip).Hash.ToLowerInvariant()
    if ($actual -ne $expected.ToLowerInvariant()) { throw "checksum mismatch for $archive" }
    Expand-Archive $zip -DestinationPath (Join-Path $work 'files') -Force
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    Copy-Item (Join-Path $work 'files\geko.exe') (Join-Path $dir 'geko.exe') -Force
} finally {
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}

Write-Host "Installed Geko $version to $(Join-Path $dir 'geko.exe')"
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not $env:GEKO_NO_MODIFY_PATH -and (($userPath -split ';') -notcontains $dir)) {
    $entries = @($userPath, $dir) | Where-Object { $_ }
    [Environment]::SetEnvironmentVariable('Path', ($entries -join ';'), 'User')
    Write-Host "Added $dir to your user PATH; open a new terminal to use it."
}
Write-Host 'Try it with: geko demo'
