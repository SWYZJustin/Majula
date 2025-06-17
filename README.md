# Majula
![Logo](./Majula_Cover.png)


**Majula** is a light weight system that will enable peer-to-peer connection and message passing. It will be able to support topic-subscription (already), RPC (already), FRP (not yet) and nginx (target partial functionalities, not yet). The name of 'Majula' comes from the warmest site inside the dark souls series.

Next goal: Communication between client and node to separate them apart && TCP message handling (sequence...)

## Installation & Running

Ensure you have Go installed

```bash
go build
./Majula.exe
```

## Code
If you want to take a look at the code, check **Node.go, Channel.go, TCPChannelWorker.go, RPC.go**, almost all the important elements are inside those files.
