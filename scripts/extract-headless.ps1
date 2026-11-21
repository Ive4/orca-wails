#Requires -Version 7.2
<#
.SYNOPSIS
Extract a pinned upstream Windows installer into a standalone Node/orcad package.
.DESCRIPTION
Does not execute the Electron installer, does not copy Electron, and never silently
falls back to Electron. Every download and every upstream template file is hashed.
The output is published only after verify-core.ps1 passes under a non-breakaway Job.
#>
[CmdletBinding()]
param(
    [string]$VersionFile = (Join-Path $PSScriptRoot '../core-version.json'),
    [string]$OutputDirectory = (Join-Path $PSScriptRoot '../dist-core'),
    [string]$InstallerPath,
    [string]$SevenZip = 'C:\Program Files\7-Zip\7z.exe'
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if (-not $IsWindows) { throw 'This extraction/qualification recipe currently targets Windows x64.' }
if (-not (Test-Path -LiteralPath $SevenZip -PathType Leaf)) {
    $command = Get-Command 7z -ErrorAction SilentlyContinue
    if (-not $command) { throw 'Install 7-Zip, or pass -SevenZip with its executable path.' }
    $SevenZip = $command.Source
}

function Assert-Sha256([string]$Path, [string]$Expected) {
    if ($Expected -cnotmatch '^[a-f0-9]{64}$') { throw "A pinned SHA256 is required: $Path" }
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "Required file is missing: $Path" }
    if ((Get-Item -LiteralPath $Path).Attributes -band [IO.FileAttributes]::ReparsePoint) {
        throw "Reparse points are not package inputs: $Path"
    }
    $actual = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -cne $Expected) { throw "SHA256 mismatch for $Path (expected $Expected, got $actual)." }
}

function Download-Pinned([string]$Url, [string]$Path, [string]$Sha256) {
    if (-not $Url.StartsWith('https://', [StringComparison]::Ordinal)) { throw 'HTTPS is required.' }
    if ($Sha256 -cnotmatch '^[a-f0-9]{64}$') { throw "A pinned SHA256 is required: $Url" }
    $cache = $env:ORCA_CORE_DOWNLOAD_CACHE
    if (-not $cache) { $cache = Join-Path $PSScriptRoot '../dist-core/upstream-cache' }
    $cache = [IO.Path]::GetFullPath($cache)
    [void][IO.Directory]::CreateDirectory($cache)
    $cached = Join-Path $cache $Sha256
    $cacheHit = Test-Path -LiteralPath $cached -PathType Leaf
    if ($cacheHit) {
        if ((Get-Item -LiteralPath $cached).Attributes -band [IO.FileAttributes]::ReparsePoint) {
            throw "Reparse points are not package inputs: $cached"
        }
        $cacheHit = (Get-FileHash -LiteralPath $cached -Algorithm SHA256).Hash -ieq $Sha256
    }
    if (-not $cacheHit) {
        $partial = Join-Path $cache "$Sha256.$([Guid]::NewGuid().ToString('N')).part"
        try {
            Invoke-WebRequest -Uri $Url -OutFile $partial -MaximumRedirection 8 -TimeoutSec 600
            Assert-Sha256 $partial $Sha256
            [IO.File]::Move($partial, $cached, $true)
        } finally {
            [IO.File]::Delete($partial)
        }
    }
    Copy-Item -LiteralPath $cached -Destination $Path
    Assert-Sha256 $Path $Sha256
}

function Safe-RelativePath([string]$Root, [string]$Relative) {
    if ([string]::IsNullOrWhiteSpace($Relative) -or $Relative.Contains('\') -or
        $Relative.Contains(':') -or $Relative.StartsWith('/') -or
        ($Relative.Split('/') | Where-Object { $_ -in @('', '.', '..') })) {
        throw "Invalid archive-relative path: $Relative"
    }
    foreach ($part in $Relative.Split('/')) {
        if ($part -match '[<>:"|?*\x00-\x1f]' -or $part.EndsWith('.') -or $part.EndsWith(' ') -or
            $part -match '^(?i:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\.|$)') {
            throw "Unsafe Windows path component: $part"
        }
    }
    return Join-Path $Root $Relative
}

function Copy-Pinned([string]$Source, [string]$Destination, [string]$Sha256) {
    Assert-Sha256 $Source $Sha256
    New-Item -ItemType Directory -Path (Split-Path -Parent $Destination) -Force | Out-Null
    Copy-Item -LiteralPath $Source -Destination $Destination
    Assert-Sha256 $Destination $Sha256
}

function Expand-With7Zip([string]$Archive, [string]$Destination) {
    New-Item -ItemType Directory -Path $Destination -Force | Out-Null
    & $SevenZip x $Archive "-o$Destination" '-y' '-bso0' '-bsp0' '-bse1'
    if ($LASTEXITCODE -ne 0) { throw "7-Zip extraction failed with exit code $LASTEXITCODE." }
}

$lock = Get-Content -LiteralPath $VersionFile -Raw | ConvertFrom-Json -AsHashtable
$stable = '(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'
if ($lock.schema_version -ne 1 -or $lock.version -cnotmatch "^v$stable$" -or
    $lock.package_revision -cnotmatch "^$stable$" -or $lock.package_revision -eq '0.0.0' -or
    $lock.target -ne 'x86_64-pc-windows-msvc' -or $lock.upstream_target -ne 'win32-x64' -or
    $lock.upstream.repository -ne 'stablyai/orca' -or
    $lock.upstream.commit -cnotmatch '^[a-f0-9]{40}$' -or
    $lock.upstream.asset -ne 'orca-windows-setup.exe') {
    throw 'Unsupported or incomplete core-version.json; review a changed upstream contract explicitly.'
}
$package = "$($lock.package_revision)-$($lock.version.Substring(1))"
$stem = "orca-core-v$package-$($lock.target)"
if ($lock.node.version -cnotmatch '^[0-9]+\.[0-9]+\.[0-9]+$' -or
    $lock.node.archive -cne "node-v$($lock.node.version)-win-x64.zip") {
    throw 'The Node runtime must be the pinned official Windows x64 archive.'
}
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
$scratch = Join-Path ([IO.Path]::GetTempPath()) ('orca-core-build-' + [Guid]::NewGuid().ToString('N'))
$stage = Join-Path $scratch 'package'
New-Item -ItemType Directory -Path $stage -Force | Out-Null
try {
    if ($InstallerPath) {
        $installer = (Resolve-Path -LiteralPath $InstallerPath).Path
        Assert-Sha256 $installer $lock.upstream.sha256
    } else {
        $installer = Join-Path $scratch 'upstream-installer.exe'
        Download-Pinned "https://github.com/stablyai/orca/releases/download/$($lock.version)/$($lock.upstream.asset)" `
            $installer $lock.upstream.sha256
    }
    $outer = Join-Path $scratch 'installer'
    Expand-With7Zip $installer $outer
    $templates = @(Get-ChildItem -LiteralPath $outer -Recurse -File -Filter 'orcad-template.json')
    if ($templates.Count -eq 0) {
        $inner = @(Get-ChildItem -LiteralPath $outer -Recurse -File | Where-Object {
            $_.Name -cmatch '^app-(?:64|x64)\.7z$'
        })
        if ($inner.Count -ne 1) {
            throw 'Expected one x64 NSIS app archive. Upstream installer layout changed; refusing to guess.'
        }
        $unpacked = Join-Path $scratch 'app'
        Expand-With7Zip $inner[0].FullName $unpacked
        $templates = @(Get-ChildItem -LiteralPath $unpacked -Recurse -File -Filter 'orcad-template.json')
    }
    if ($templates.Count -ne 1) { throw 'Expected exactly one official orcad-template manifest.' }
    $templateDir = $templates[0].Directory.FullName
    $resources = Split-Path -Parent $templateDir
    $template = Get-Content -LiteralPath $templates[0].FullName -Raw | ConvertFrom-Json -AsHashtable
    if ($template.schemaVersion -notin @(3, 4) -or -not $template.targets.Contains($lock.upstream_target)) {
        throw 'Unsupported template schema/target; review upstream before repackaging.'
    }
    if (-not (Test-Path -LiteralPath (Join-Path $resources 'app.asar') -PathType Leaf)) {
        throw 'The extracted resources are not the expected official Electron package.'
    }
    $packagedVersionFile = Join-Path $resources 'app.asar.unpacked/out/package.json'
    $packagedVersion = (Get-Content -LiteralPath $packagedVersionFile -Raw | ConvertFrom-Json).version
    if ($packagedVersion -cne $lock.version.Substring(1)) { throw 'Installer version disagrees with the lock.' }

    $slot = Join-Path $stage 'orcad'
    New-Item -ItemType Directory -Path $slot -Force | Out-Null
    foreach ($entry in $template.commonSha256.GetEnumerator()) {
        Copy-Pinned (Safe-RelativePath $templateDir $entry.Key) (Safe-RelativePath $slot $entry.Key) $entry.Value
    }
    $target = $template.targets[$lock.upstream_target]
    $targetRoot = Join-Path $templateDir "targets/$($lock.upstream_target)"
    foreach ($entry in $target.files.GetEnumerator()) {
        $sourceRoot = $targetRoot
        if ($target.Contains('packagedRipgrep') -and $target.packagedRipgrep -eq $true -and
            $entry.Key -ceq 'ripgrep/win32-x64/rg.exe') { $sourceRoot = $resources }
        Copy-Pinned (Safe-RelativePath $sourceRoot $entry.Key) (Safe-RelativePath $slot $entry.Key) $entry.Value
    }
    $browserName = ''
    if ($target.Contains('browserName')) {
        $browserName = $target.browserName
        if ($browserName -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$') { throw 'Invalid bundled browser filename.' }
        $browserRoot = $targetRoot
        if ($target.Contains('packagedBrowser') -and $target.packagedBrowser -eq $true) { $browserRoot = $resources }
        Copy-Pinned (Join-Path $browserRoot $browserName) (Join-Path $slot $browserName) $target.browserSha256
    }
    if ((Get-Content -LiteralPath (Join-Path $slot '.server-target') -Raw).Trim() -cne $lock.upstream_target -or
        (Get-Content -LiteralPath (Join-Path $slot '.runtime-node') -Raw).Trim() -cne $lock.node.executable_sha256) {
        throw 'The extracted slot does not name the locked platform/Node runtime.'
    }

    # A stock Node cannot read app.asar. The official packaged browser bundle is on real disk.
    $renderer = Join-Path $resources 'app.asar.unpacked/out/renderer'
    $webManifest = Join-Path $renderer '.orca-web-client-files.json'
    $webFiles = Get-Content -LiteralPath $webManifest -Raw | ConvertFrom-Json
    if ($webFiles -isnot [array] -or 'web-index.html' -cnotin $webFiles) { throw 'Missing browser client allowlist.' }
    $web = Join-Path $stage 'web'
    foreach ($file in $webFiles) {
        if ($file -isnot [string] -or $file -cnotmatch '^(web-index\.html|(?:assets|cmaps|standard_fonts|wasm)/.+)$' -or
            $file.EndsWith('.map')) { throw 'Unexpected browser client allowlist entry.' }
        $source = Safe-RelativePath $renderer $file
        $dest = Safe-RelativePath $web $file
        New-Item -ItemType Directory -Path (Split-Path -Parent $dest) -Force | Out-Null
        Copy-Item -LiteralPath $source -Destination $dest
    }
    Copy-Item -LiteralPath $webManifest -Destination (Join-Path $web '.orca-web-client-files.json')

    $nodeArchive = Join-Path $scratch $lock.node.archive
    Download-Pinned "https://nodejs.org/dist/v$($lock.node.version)/$($lock.node.archive)" `
        $nodeArchive $lock.node.archive_sha256
    $nodeExpanded = Join-Path $scratch 'node'
    Expand-Archive -LiteralPath $nodeArchive -DestinationPath $nodeExpanded
    $nodeRoot = Join-Path $nodeExpanded $lock.node.archive.Substring(0, $lock.node.archive.Length - 4)
    $nodeRelative = "runtimes/node-$($lock.node.executable_sha256)/node.exe"
    $nodeExe = Join-Path $stage $nodeRelative
    Copy-Pinned (Join-Path $nodeRoot 'node.exe') $nodeExe $lock.node.executable_sha256
    $licenses = Join-Path $stage 'licenses'
    New-Item -ItemType Directory -Path $licenses -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $nodeRoot 'LICENSE') -Destination (Join-Path $licenses 'NODE-LICENSE')

    # Use the exact upstream artifact inventory to compute its native slot version.
    # Pin the source bytes too; never execute a floating `main` build helper.
    $artifactSource = Join-Path $scratch 'orcad-artifacts.ts'
    $artifactKey = 'src/shared/remote-runtime/orcad-distribution/orcad-artifacts.ts'
    Download-Pinned "https://raw.githubusercontent.com/stablyai/orca/$($lock.upstream.commit)/$artifactKey" `
        $artifactSource $lock.source_sha256[$artifactKey]
    Download-Pinned "https://raw.githubusercontent.com/stablyai/orca/$($lock.upstream.commit)/LICENSE" `
        (Join-Path $licenses 'ORCA-LICENSE') $lock.source_sha256['LICENSE']
    $inventoryScript = @'
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const m = require(process.argv[1]);
const root = process.argv[2], target = process.argv[3], browser = process.argv[4];
const mobile = m.orcadMobileWebAssetFilenames(fs.readFileSync(path.join(root, 'out/mobile-web/manifest.json'), 'utf8'));
const hash = crypto.createHash('sha256');
for (const filename of m.orcadArtifactFilenames(target, mobile)) hash.update(fs.readFileSync(path.join(root, filename)));
if (browser) hash.update(fs.readFileSync(path.join(root, browser)));
process.stdout.write(m.ORCAD_VERSION + '+' + hash.digest('hex').slice(0, 12));
'@
    $slotVersion = & $nodeExe -e $inventoryScript $artifactSource $slot $lock.upstream_target $browserName
    if ($LASTEXITCODE -ne 0 -or $slotVersion -cnotmatch '^\d+\.\d+\.\d+\+[a-f0-9]{12}$') {
        throw 'Upstream artifact inventory/version qualification failed.'
    }
    [IO.File]::WriteAllText((Join-Path $slot '.version'), "$slotVersion`n")
    # Preserve installed notices; template ripgrep notices are already included above.
    foreach ($notice in @(Get-ChildItem -LiteralPath @($resources, (Split-Path -Parent $resources)) -File | Where-Object {
        $_.Name -match '^(LICENSE|NOTICE|THIRD.PARTY)'
    })) { Copy-Item -LiteralPath $notice.FullName -Destination (Join-Path $licenses $notice.Name) }
    $manifest = [ordered]@{
        schema_version = 1
        version = $lock.version
        package_revision = $lock.package_revision
        target = $lock.target
        executable = $nodeRelative
        args = @('{core_dir}/orcad/orcad-server.js', '--bind', '{host}', '--port', '{port}',
            '--web-client-root', '{core_dir}/web', '--json')
        working_directory = 'orcad'
        environment = @{ ORCA_USER_DATA = '{data_dir}'; ORCA_VERSION = $slotVersion; ORCA_SERVE_RUNTIME = 'orcad' }
        health_path = '/web-index.html'
        ui_path = '/web-index.html'
        upstream_commit = $lock.upstream.commit
        upstream_installer_sha256 = $lock.upstream.sha256
        node_version = $lock.node.version
        node_sha256 = $lock.node.executable_sha256
        slot_version = $slotVersion
    }
    $manifest | ConvertTo-Json -Depth 20 | Set-Content -LiteralPath (Join-Path $stage 'core-manifest.json') -Encoding utf8NoBOM
    $lock | ConvertTo-Json -Depth 20 | Set-Content -LiteralPath (Join-Path $stage 'upstream-lock.json') -Encoding utf8NoBOM
    $report = Join-Path $OutputDirectory "$stem-validation.json"
    & (Join-Path $PSScriptRoot 'verify-core.ps1') -CoreDirectory $stage -ReportPath $report
    if (-not $?) { throw 'Core smoke test failed; archive will not be produced.' }
    $archive = Join-Path $OutputDirectory "$stem.zip"
    if (Test-Path -LiteralPath $archive) { Remove-Item -LiteralPath $archive -Force }
    [IO.Compression.ZipFile]::CreateFromDirectory($stage, $archive, [IO.Compression.CompressionLevel]::Optimal, $false)
    $digest = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText("$archive.sha256", "$digest  $([IO.Path]::GetFileName($archive))`n")
    Write-Host "Qualified core archive: $archive"
} finally {
    if (Test-Path -LiteralPath $scratch) { Remove-Item -LiteralPath $scratch -Recurse -Force }
}
