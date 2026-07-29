@echo off
setlocal EnableExtensions
set "RELAY_ROOT=%~dp0"
for %%I in ("%RELAY_ROOT%..") do set "SUITE_ROOT=%%~fI"
set "AIRLOCK_ROOT=%SUITE_ROOT%\airlock"
set "SIDECAR_ROOT=%SUITE_ROOT%\hornets-hyperswarm"
set "PANEL_ROOT=%SUITE_ROOT%\hornets-relay-panel"
set "NOSIS_CLI_ROOT=%SUITE_ROOT%\nosis-cli"
set "OUT=%RELAY_ROOT%dist\hornets-relay-dev"

for %%P in ("%AIRLOCK_ROOT%" "%SIDECAR_ROOT%" "%PANEL_ROOT%" "%NOSIS_CLI_ROOT%") do (
  if not exist "%%~P\" (
    echo Missing sibling project: %%~P 1>&2
    exit /b 1
  )
)

if exist "%OUT%" rmdir /s /q "%OUT%"
mkdir "%OUT%\bin" "%OUT%\relay\web" "%OUT%\airlock" || exit /b 1

set "CGO_ENABLED=1"
pushd "%RELAY_ROOT%"
go build -buildvcs=false -trimpath -o "%OUT%\bin\hornets-relay.exe" .\services\server\port || exit /b 1
popd
pushd "%AIRLOCK_ROOT%"
go build -buildvcs=false -trimpath -o "%OUT%\bin\airlock.exe" . || exit /b 1
popd
set "CGO_ENABLED="

pushd "%SIDECAR_ROOT%"
call npx --yes npm@11.10.0 ci || exit /b 1
call npx --yes npm@11.10.0 run build || exit /b 1
popd

pushd "%PANEL_ROOT%"
call corepack enable || exit /b 1
call corepack prepare yarn@1.22.19 --activate || exit /b 1
call yarn install --frozen-lockfile || exit /b 1
set "NODE_OPTIONS=--openssl-legacy-provider"
call yarn lessc --js --clean-css="--s1 --advanced" src\styles\themes\main.less public\themes\main.css || exit /b 1
call yarn craco build || exit /b 1
set "NODE_OPTIONS="
popd

copy /y "%SIDECAR_ROOT%\dist\hornets-hyperswarm.exe" "%OUT%\bin\hornets-hyperswarm.exe" >nul || exit /b 1
xcopy /e /i /h /y "%SIDECAR_ROOT%\dist\prebuilds" "%OUT%\bin\prebuilds" >nul || exit /b 1
copy /y "%RELAY_ROOT%config.example.yaml" "%OUT%\relay\config.example.yaml" >nul || exit /b 1
copy /y "%AIRLOCK_ROOT%\config.example.yaml" "%OUT%\airlock\config.example.yaml" >nul || exit /b 1
xcopy /e /i /h /y "%RELAY_ROOT%release\bundle\*" "%OUT%" >nul || exit /b 1
xcopy /e /i /h /y "%PANEL_ROOT%\build\*" "%OUT%\relay\web" >nul || exit /b 1

call :revision_for "%RELAY_ROOT%" RELAY_REVISION || exit /b 1
call :revision_for "%AIRLOCK_ROOT%" AIRLOCK_REVISION || exit /b 1
call :revision_for "%SIDECAR_ROOT%" HYPERSWARM_REVISION || exit /b 1
call :revision_for "%NOSIS_CLI_ROOT%" NOSIS_CLI_REVISION || exit /b 1
call :revision_for "%PANEL_ROOT%" PANEL_REVISION || exit /b 1

node "%RELAY_ROOT%scripts\compliance\generate.mjs" ^
  --workspace "%SUITE_ROOT%" ^
  --output "%OUT%\compliance" ^
  --artifact hornets-relay-dev ^
  --relay-revision "%RELAY_REVISION%" ^
  --airlock-revision "%AIRLOCK_REVISION%" ^
  --hyperswarm-revision "%HYPERSWARM_REVISION%" ^
  --nosis-cli-revision "%NOSIS_CLI_REVISION%" ^
  --panel-revision "%PANEL_REVISION%" || exit /b 1

(
  echo Relay: %RELAY_REVISION%
  echo Airlock: %AIRLOCK_REVISION%
  echo Hyperswarm: %HYPERSWARM_REVISION%
  echo Nosis CLI: %NOSIS_CLI_REVISION%
  echo Relay panel: %PANEL_REVISION%
) > "%OUT%\BUILD-METADATA.txt"

node "%RELAY_ROOT%scripts\compliance\verify.mjs" "%OUT%" || exit /b 1
echo Built and compliance-verified self-contained stack at %OUT%
exit /b 0


:: Return an immutable commit when clean, otherwise mark the source as a working tree.
:revision_for
set "PROJECT_DIR=%~1"
set "REVISION=working-tree"
set "DIRTY="
for /f "delims=" %%S in ('git -C "%PROJECT_DIR%" status --porcelain 2^>nul') do set "DIRTY=1"
if not defined DIRTY for /f "delims=" %%S in ('git -C "%PROJECT_DIR%" rev-parse HEAD 2^>nul') do set "REVISION=%%S"
set "%~2=%REVISION%"
exit /b 0
