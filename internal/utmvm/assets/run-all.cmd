@echo off
REM Runs every probe and writes one report to the Desktop.
REM
REM Two rules this script exists to obey:
REM
REM   1. No `pause`. This runs under `utmctl exec`, which has no console to
REM      dismiss a prompt, so a pause blocks forever and the caller times out
REM      with no output.
REM   2. The exit code must mean something. A report full of failures that
REM      exits 0 is indistinguishable from success to anything scripting this.
REM
REM Binaries sit beside this script at the medium root, not in a subdirectory:
REM go-diskfs mangles Joliet names inside nested directories into UCS-2 garbage
REM while root entries survive intact.
setlocal enabledelayedexpansion
set DIR=%~dp0
set OUT=%USERPROFILE%\Desktop\irgo-probe-report.txt
set FAILED=0

echo irgo Windows probe report > "%OUT%"
ver >> "%OUT%"
echo PROCESSOR_ARCHITECTURE=%PROCESSOR_ARCHITECTURE% >> "%OUT%"
echo. >> "%OUT%"

call :probe "native capabilities (ARM64 native)"      nativeprobe-arm64.exe
call :probe "glaze app:// scheme (ARM64 native)"      glaze-verify-arm64.exe
call :probe "glaze Events bridge (ARM64 native)"      glaze-verifyevents-arm64.exe
call :probe "native capabilities (x64 emulated)"      nativeprobe-amd64.exe
call :probe "glaze app:// scheme (x64 emulated)"      glaze-verify-amd64.exe

echo. >> "%OUT%"
if %FAILED% EQU 0 (
  echo ALL PROBES PASSED >> "%OUT%"
) else (
  echo %FAILED% PROBE^(S^) FAILED >> "%OUT%"
)

type "%OUT%"
exit /b %FAILED%

:probe
REM %~1 = heading, %~2 = executable. A missing binary is a failure, not a skip:
REM the payload is generated, so absence means the build or the image is wrong.
echo ===== %~1 ===== >> "%OUT%"
if not exist "%DIR%%~2" (
  echo MISSING: %~2 >> "%OUT%"
  set /a FAILED+=1
  echo. >> "%OUT%"
  exit /b 0
)
"%DIR%%~2" >> "%OUT%" 2>&1
if errorlevel 1 (
  echo [exit code %errorlevel%] >> "%OUT%"
  set /a FAILED+=1
)
echo. >> "%OUT%"
exit /b 0
