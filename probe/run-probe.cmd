@echo off
REM Runs the native capability probe and leaves the output on the Desktop.
set OUT=%USERPROFILE%\Desktop\nativeprobe-output.txt
echo Running ARM64 probe... 
"%~dp0nativeprobe-arm64.exe" > "%OUT%" 2>&1
echo. >> "%OUT%"
echo ---- x64 build under emulation ---- >> "%OUT%"
"%~dp0nativeprobe-amd64.exe" >> "%OUT%" 2>&1
type "%OUT%"
echo.
echo Saved to %OUT%
pause
