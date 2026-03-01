param(
    [switch]$Apply
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Is-TestProviderArtifact {
    param(
        [Parameter(Mandatory = $true)]
        [psobject]$Provider
    )

    $name = [string]$Provider.name
    $apiUrl = [string]$Provider.apiUrl
    $apiKey = [string]$Provider.apiKey

    $isLocalEphemeral = $apiUrl -match '^http://127\.0\.0\.1:\d+$'
    $isTestProvider = ($name -eq "TestProvider" -and $apiKey -eq "test-api-key")
    $isCustomTestProvider = ($name -eq "CustomTestProvider" -and $apiKey -eq "custom-api-key")

    return $isLocalEphemeral -and ($isTestProvider -or $isCustomTestProvider)
}

function Get-TargetFiles {
    param(
        [Parameter(Mandatory = $true)]
        [string]$BaseDir
    )

    $targets = @()

    foreach ($fixedName in @("claude-code.json", "codex.json")) {
        $fixedPath = Join-Path $BaseDir $fixedName
        if (Test-Path $fixedPath) {
            $targets += $fixedPath
        }
    }

    $providersDir = Join-Path $BaseDir "providers"
    if (Test-Path $providersDir) {
        $providerFiles = Get-ChildItem -Path $providersDir -Filter *.json -File -ErrorAction SilentlyContinue
        foreach ($f in $providerFiles) {
            $targets += $f.FullName
        }
    }

    return $targets
}

function Backup-File {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    $timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
    $backupPath = "$Path.test-artifact-backup-$timestamp"
    Copy-Item -Path $Path -Destination $backupPath -Force
    return $backupPath
}

$homeDir = [Environment]::GetFolderPath("UserProfile")
$baseDir = Join-Path $homeDir ".code-switch"

if (-not (Test-Path $baseDir)) {
    Write-Output "No .code-switch directory found: $baseDir"
    exit 0
}

$files = Get-TargetFiles -BaseDir $baseDir
if ($files.Count -eq 0) {
    Write-Output "No provider files found under: $baseDir"
    exit 0
}

$matchedProvidersTotal = 0
$changedFiles = 0

if ($Apply) {
    Write-Output "Mode: APPLY"
} else {
    Write-Output "Mode: DRY-RUN"
}

foreach ($file in $files) {
    $raw = Get-Content -Path $file -Raw
    if ([string]::IsNullOrWhiteSpace($raw)) {
        continue
    }

    try {
        $jsonObj = $raw | ConvertFrom-Json -Depth 100
    } catch {
        Write-Warning "Skip invalid JSON: $file"
        continue
    }

    if ($null -eq $jsonObj.providers) {
        continue
    }

    $providers = @($jsonObj.providers)
    $keep = @()
    $matchedInFile = @()

    foreach ($p in $providers) {
        if (Is-TestProviderArtifact -Provider $p) {
            $matchedInFile += $p
        } else {
            $keep += $p
        }
    }

    if ($matchedInFile.Count -eq 0) {
        continue
    }

    $changedFiles++
    $matchedProvidersTotal += $matchedInFile.Count

    Write-Output "Match: $file"
    foreach ($m in $matchedInFile) {
        Write-Output ("  - remove provider: name={0}, apiUrl={1}" -f $m.name, $m.apiUrl)
    }

    if (-not $Apply) {
        continue
    }

    $backup = Backup-File -Path $file
    $jsonObj.providers = $keep
    $output = $jsonObj | ConvertTo-Json -Depth 100
    Set-Content -Path $file -Value $output -Encoding UTF8

    Write-Output "  backup: $backup"
    Write-Output "  updated: $file"
}

if ($changedFiles -eq 0) {
    Write-Output "No test artifacts found."
    exit 0
}

if (-not $Apply) {
    Write-Output ("Dry-run summary: matchedProviders={0}, changedFiles={1}" -f $matchedProvidersTotal, $changedFiles)
    Write-Output "Run with -Apply to clean and create backups."
    exit 0
}

Write-Output ("Apply summary: cleanedProviders={0}, changedFiles={1}" -f $matchedProvidersTotal, $changedFiles)
