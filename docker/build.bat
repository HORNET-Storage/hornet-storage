@echo off
setlocal
set "RELAY_ROOT=%~dp0.."
for %%I in ("%RELAY_ROOT%\..") do set "SUITE_ROOT=%%~fI"
set "IMAGE=%~1"
if not defined IMAGE set "IMAGE=hornetstorage/hornets-relay:local"

for %%P in (hornets-nostr-relay airlock nosis-cli hornets-hyperswarm hornets-relay-panel) do (
  if not exist "%SUITE_ROOT%\%%P\" (
    echo Missing sibling project: %SUITE_ROOT%\%%P 1>&2
    exit /b 1
  )
)

docker build -f "%RELAY_ROOT%\Dockerfile" -t "%IMAGE%" "%SUITE_ROOT%"
exit /b %ERRORLEVEL%
