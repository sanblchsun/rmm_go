@echo off
set CGO_ENABLED=1
go build -o agent.exe .
echo done