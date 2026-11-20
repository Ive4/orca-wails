param(
    [string]$Version = '0.1.0',
    [string]$PublicKey = '',
    [string]$Archive
)
$ErrorActionPreference = 'Stop'
if ($Version -cnotmatch '^\d+\.\d+\.\d+$' -or $PublicKey -cnotmatch '^[A-Za-z0-9+/=]*$') {
    throw 'Invalid version or base64 update public key'
}

Push-Location (Join-Path $PSScriptRoot '..')
try {
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    New-Item -ItemType Directory -Force bin | Out-Null
    $output = 'bin/Orca-Wails.exe'
    if ($Archive) {
        $archivePath = (Resolve-Path -LiteralPath $Archive).Path
        $expected = ((Get-Content -LiteralPath "$archivePath.sha256" -Raw) -split '\s+')[0].ToLowerInvariant()
        $digest = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($expected -cne $digest) { throw 'Core archive checksum mismatch' }
        $zip = [IO.Compression.ZipFile]::OpenRead($archivePath)
        try {
            $entry = $zip.GetEntry('core-manifest.json')
            if (!$entry -or $entry.Length -gt 65536) { throw 'Missing or oversized core manifest' }
            $reader = [IO.StreamReader]::new($entry.Open())
            try { $manifest = $reader.ReadToEnd() | ConvertFrom-Json } finally { $reader.Dispose() }
        } finally { $zip.Dispose() }
        if ($manifest.target -ne 'x86_64-pc-windows-msvc') { throw 'Incorrect core target' }
        $identity = @{
            version = $manifest.version
            package_revision = [string]$manifest.package_revision
            target = $manifest.target
            sha256 = $digest
        }
        $bundle = 'bin/Orca-Wails'
        New-Item -ItemType Directory -Force $bundle | Out-Null
        Copy-Item -LiteralPath $archivePath -Destination "$bundle/bootstrap-core.zip"
        $identity | ConvertTo-Json | Set-Content -Encoding utf8NoBOM "$bundle/bootstrap-core.json"
        $output = "$bundle/Orca-Wails.exe"
    }
    go build -tags production -trimpath -ldflags "-s -w -H windowsgui -X main.version=$Version -X main.appUpdatePublicKey=$PublicKey" -o $output .
    if ($LASTEXITCODE -ne 0) { throw "go build failed: $LASTEXITCODE" }
    Write-Output $output
} finally {
    Pop-Location
}
