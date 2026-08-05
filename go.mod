module rocksys

go 1.25.0

require (
	github.com/iotames/easyconf v1.1.3
	github.com/iotames/easydb v0.0.0
	github.com/iotames/easyserver v1.5.0
	github.com/yuin/gopher-lua v1.1.2
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.55.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/iotames/miniutils v1.0.11 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/net v0.5.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.6.0 // indirect
	modernc.org/libc v1.74.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

replace (
	github.com/iotames/easyconf => ./easyconf
	github.com/iotames/easydb => ./easydb
	github.com/iotames/easyserver => ./easyserver
)
