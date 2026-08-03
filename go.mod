module rocksys

go 1.24.1

require (
    github.com/iotames/easyserver v0.0.0
    github.com/iotames/easyconf v0.0.0
)

replace (
    github.com/iotames/easyserver => ./easyserver
    github.com/iotames/easyconf => ./easyconf
)
