module rocksys

go 1.24.1

require (
	github.com/iotames/easyconf v0.0.0
	github.com/iotames/easyserver v0.0.0
	github.com/yuin/gopher-lua v1.1.2
)

require (
	github.com/iotames/miniutils v1.0.11 // indirect
	golang.org/x/net v0.5.0 // indirect
	golang.org/x/text v0.6.0 // indirect
)

replace (
	github.com/iotames/easyconf => ./easyconf
	github.com/iotames/easyserver => ./easyserver
)
