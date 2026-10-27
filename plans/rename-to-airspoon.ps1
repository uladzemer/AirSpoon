# Переименование D:\github\AirLive -> D:\github\AirSpoon с переносом сессий и памяти Claude Code.
# Запускать в PowerShell ПОСЛЕ полного закрытия VS Code (иначе папка занята).
$ErrorActionPreference = 'Stop'
$old = 'D:\github\AirLive'
$new = 'D:\github\AirSpoon'
if (Test-Path $new) { throw "Папка $new уже существует" }
Rename-Item -Path $old -NewName 'AirSpoon'

# История диалогов и память Claude Code привязаны к пути папки — копируем их под новое имя.
# Старую копию не удаляем: пусть лежит как резерв.
$p = Join-Path $env:USERPROFILE '.claude\projects'
robocopy (Join-Path $p 'd--github-AirLive') (Join-Path $p 'd--github-AirSpoon') /E /XO /NFL /NDL /NJH /NJS | Out-Null
if ($LASTEXITCODE -ge 8) { throw "robocopy завершился с ошибкой $LASTEXITCODE" }

Write-Host "Готово: $new. Открываю VS Code..."
code $new
