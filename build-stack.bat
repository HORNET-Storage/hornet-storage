@echo off
setlocal EnableExtensions
set "RELAY_ROOT=%~dp0"
for %%I in ("%RELAY_ROOT%..") do set "SUITE_ROOT=%%~fI"
set "AIRLOCK_ROOT=%SUITE_ROOT%\airlock"
set "SIDECAR_ROOT=%SUITE_ROOT%\hornets-hyperswarm"
set "PANEL_ROOT=%SUITE_ROOT%\hornets-relay-panel"
set "OUT=%RELAY_ROOT%dist\hornets-relay-dev"

for %%P in ("%AIRLOCK_ROOT%" "%SIDECAR_ROOT%" "%PANEL_ROOT%" "%SUITE_ROOT%\nosis-cli") do (
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
set "CGO_ENABLED=1"
pushd "%AIRLOCK_ROOT%"
go build -buildvcs=false -trimpath -o "%OUT%\bin\airlock.exe" . || exit /b 1
popd
set "CGO_ENABLED="
pushd "%SIDECAR_ROOT%"
if exist package-lock.json (
  call npx --yes npm@11.10.0 ci
) else (
  call npx --yes npm@11.10.0 install
)
if errorlevel 1 exit /b 1
call npx --yes npm@11.10.0 run build || exit /b 1
popd

pushd "%PANEL_ROOT%"
call corepack enable || exit /b 1
call corepack prepare yarn@1.22.22 --activate || exit /b 1
call yarn install --frozen-lockfile || exit /b 1
set "NODE_OPTIONS=--openssl-legacy-provider"
call yarn lessc --js --clean-css="--s1 --advanced" src\styles\themes\main.less public\themes\main.css || exit /b 1
call yarn craco build || exit /b 1
popd

copy /y "%SIDECAR_ROOT%\dist\hornets-hyperswarm.exe" "%OUT%\bin\hornets-hyperswarm.exe" >nul || exit /b 1
xcopy /e /i /y "%SIDECAR_ROOT%\dist\prebuilds" "%OUT%\bin\prebuilds" >nul || exit /b 1
copy /y "%RELAY_ROOT%config.example.yaml" "%OUT%\relay\config.example.yaml" >nul || exit /b 1
copy /y "%AIRLOCK_ROOT%\config.example.yaml" "%OUT%\airlock\config.example.yaml" >nul || exit /b 1
xcopy /e /i /y "%RELAY_ROOT%release\bundle\*" "%OUT%" >nul || exit /b 1
xcopy /e /i /y "%PANEL_ROOT%\build\*" "%OUT%\relay\web" >nul || exit /b 1

echo Built self-contained stack at %OUT%
exit /b 0
