<#
.SYNOPSIS
  Собирает AirSpoon Sender одной командой: bin\AirSpoonSender.exe (и bin\testreceiver.exe).

.DESCRIPTION
  1. рисует значки (tools/genicon -> assets/*.ico);
  2. собирает ресурсы .exe (значки, манифест PerMonitorV2 + Common Controls v6,
     версия) в cmd/airspoon/rsrc_windows_amd64.syso через go-winres (чистый Go,
     без cgo; ставится в .tools при первом запуске);
  3. go vet и go test (можно пропустить: -SkipTests);
  4. go build с -H windowsgui: без аргументов программа работает в трее,
     с аргументами (list, mirror) подключается к консоли запустившего её cmd/PowerShell.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File build.ps1
  powershell -ExecutionPolicy Bypass -File build.ps1 -SkipTests
#>
param(
    [switch]$SkipTests,
    # Дополнительно собрать bin\AirSpoonSender.testhooks.exe: та же программа с
    # тестовыми переменными окружения AIRSPOON_TEST_* (в обычном exe их нет).
    [switch]$TestHooks
)

$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

$goDir = 'C:\Program Files\Go\bin'
if (Test-Path -LiteralPath $goDir) { $env:PATH = "$goDir;$env:PATH" }
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw 'Не найден Go. Установите Go 1.26+ (https://go.dev/dl/) или поправьте путь в build.ps1.'
}
$env:CGO_ENABLED = '0'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'

# Одна версия для main.version, манифеста и всех полей ресурсов EXE.
$version = 'dev'
$resourceVersion = '0.0.0.0'
if ($env:AIRSPOON_VERSION -and $env:AIRSPOON_VERSION.Trim() -ne '') {
    $v = $env:AIRSPOON_VERSION.Trim()
    if ($v -notmatch '^\d+\.\d+\.\d+$') {
        throw "Неверный формат переменной окружения AIRSPOON_VERSION: '$v'. Требуется формат X.Y.Z (например, 0.2.0)."
    }
    # Компоненты версии PE — беззнаковые 16-битные числа.
    foreach ($part in $v.Split('.')) {
        $component = 0
        if (-not [int]::TryParse($part, [ref]$component) -or $component -gt 65535) {
            throw "Компоненты AIRSPOON_VERSION должны быть в диапазоне 0..65535: '$v'."
        }
    }
    $version = $v
    $resourceVersion = "$v.0"
}

function Step($text) { Write-Host "==> $text" -ForegroundColor Cyan }
function Check($what) { if ($LASTEXITCODE -ne 0) { throw "$what завершилась с ошибкой (код $LASTEXITCODE)" } }

Step 'Значки'
go run ./tools/genicon
Check 'Генерация значков'

Step 'Ресурсы .exe (значки, манифест, версия)'
$tools = Join-Path $PSScriptRoot '.tools'
$winres = Join-Path $tools 'go-winres.exe'
if (-not (Test-Path -LiteralPath $winres)) {
    $env:GOBIN = $tools
    go install github.com/tc-hib/go-winres@v0.3.3
    Check 'Установка go-winres'
    Remove-Item Env:\GOBIN
}
# Временный JSON рядом с шаблоном сохраняет относительные пути к значкам.
$resourceSpec = Get-Content -LiteralPath winres/winres.json -Raw -Encoding UTF8 | ConvertFrom-Json
$resourceSpec.RT_MANIFEST.'#1'.'0409'.identity.version = $resourceVersion
$versionSpec = $resourceSpec.RT_VERSION.'#1'.'0000'
$versionSpec.fixed.file_version = $resourceVersion
$versionSpec.fixed.product_version = $resourceVersion
foreach ($locale in $versionSpec.info.PSObject.Properties) {
    $locale.Value.FileVersion = $resourceVersion
    $locale.Value.ProductVersion = $resourceVersion
}
$resourceInput = Join-Path $PSScriptRoot "winres/generated-$([guid]::NewGuid()).json"
try {
    [IO.File]::WriteAllText($resourceInput, ($resourceSpec | ConvertTo-Json -Depth 20), [Text.UTF8Encoding]::new($false))
    & $winres make --in $resourceInput --out cmd/airspoon/rsrc --arch amd64
    Check 'go-winres'
} finally {
    if (Test-Path -LiteralPath $resourceInput) { Remove-Item -LiteralPath $resourceInput }
}

if (-not $SkipTests) {
    Step 'go vet'
    go vet ./...
    Check 'go vet'
    Step 'go test'
    go test ./...
    Check 'go test'
    go test -tags testhooks ./cmd/airspoon
    Check 'go test (тег testhooks)'
}

Step 'Сборка bin\AirSpoonSender.exe'
New-Item -ItemType Directory -Force -Path bin | Out-Null
$senderLdflags = "-H windowsgui -s -w -X main.version=$version"
go build -trimpath -ldflags $senderLdflags -o bin\AirSpoonSender.exe ./cmd/airspoon
Check 'Сборка AirSpoonSender.exe'
go build -trimpath -o bin\testreceiver.exe ./cmd/testreceiver
Check 'Сборка testreceiver.exe'
if ($TestHooks) {
    go build -trimpath -tags testhooks -ldflags $senderLdflags -o bin\AirSpoonSender.testhooks.exe ./cmd/airspoon
    Check 'Сборка AirSpoonSender.testhooks.exe'
}

# Консольный клиент теперь внутри AirSpoonSender.exe; старый файл не нужен.
if (Test-Path -LiteralPath bin\airspoon.exe) { Remove-Item -LiteralPath bin\airspoon.exe -Force }

$exe = Get-Item -LiteralPath bin\AirSpoonSender.exe
Write-Host ("Готово: {0} ({1:N1} МБ)" -f $exe.FullName, ($exe.Length / 1MB)) -ForegroundColor Green
