package profiler

import commonv3 "skywalking.apache.org/repo/goapi/collect/common/v3"

type BaseCommand struct {
	Command      string
	SerialNumber string
}

type ExecuteService interface {
	HandleCommand(rawCommand *commonv3.Command)
}
