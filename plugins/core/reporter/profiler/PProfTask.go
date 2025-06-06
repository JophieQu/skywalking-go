package profiler

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"time"

	"github.com/apache/skywalking-go/plugins/core/operator"
	"github.com/apache/skywalking-go/plugins/core/reporter"
	commonv3 "skywalking.apache.org/repo/goapi/collect/common/v3"
	pprofv10 "skywalking.apache.org/repo/goapi/collect/language/pprof/v10"
)

const (
	// Name of the pprof task query command
	PprofTaskCommandName = "PprofTaskQuery"
	// TaskDurationMinMinute Monitor duration must greater than 1 minutes
	TaskDurationMinMinute = 1 * time.Minute
	// TaskDurationMaxMinute The duration of the monitoring task cannot be greater than 15 minutes
	TaskDurationMaxMinute = 15 * time.Minute
	// Maximum rate for profile data dumping, measured in Hz (samples per second)
	TaskDumpPeriodMaxRate = 100
)

// Pprof types
const (
	PprofTypeCPU    = "CPU"
	PprofTypeMemory = "MEMORY"
	PprofTypeBlock  = "BLOCK"
	PprofTypeMutex  = "MUTEX"
)

// Pprof file names
const (
	CPUPprofFileName    = "cpu.pprof"
	MemoryPprofFileName = "memory.pprof"
	BlockPprofFileName  = "block.pprof"
	MutexPprofFileName  = "mutex.pprof"
)

type PprofTaskCommand struct {
	BaseCommand
	// Task ID uniquely identifies a profiling task
	taskId string
	// Type of profiling (CPU/Alloc/Block/Mutex)
	profileType string
	// unit is minute
	duration time.Duration
	// Unix timestamp in milliseconds when the task should start
	startTime int64
	// Unix timestamp in milliseconds when the task was created
	createTime int64
	// unit is hz
	dumpPeriod int
}

func (c *PprofTaskCommand) CheckCommand() error {
	if c.profileType == "" {
		c.profileType = PprofTypeCPU
	}
	if c.profileType != PprofTypeCPU && c.profileType != PprofTypeMemory &&
		c.profileType != PprofTypeBlock && c.profileType != PprofTypeMutex {
		return fmt.Errorf("unsupported profile type: %s", c.profileType)
	}
	if c.duration < TaskDurationMinMinute {
		return fmt.Errorf("monitor duration must greater than %v", TaskDurationMinMinute)
	}
	if c.duration > TaskDurationMaxMinute {
		return fmt.Errorf("monitor duration must less than %v", TaskDurationMaxMinute)
	}
	if c.dumpPeriod > TaskDumpPeriodMaxRate {
		return fmt.Errorf("dump period must be less than or equals %v hz", TaskDumpPeriodMaxRate)
	}
	return nil
}

func deserializePprofTaskCommand(command *commonv3.Command) *PprofTaskCommand {
	args := command.Args
	taskId := ""
	serialNumber := ""
	profileType := PprofTypeCPU
	duration := 0
	dumpPeriod := 100
	var startTime int64 = 0
	var createTime int64 = 0
	for _, pair := range args {
		if pair.GetKey() == "SerialNumber" {
			serialNumber = pair.GetValue()
		} else if pair.GetKey() == "TaskId" {
			taskId = pair.GetValue()
		} else if pair.GetKey() == "PprofType" {
			profileType = pair.GetValue()
		} else if pair.GetKey() == "Duration" {
			if val, err := strconv.Atoi(pair.GetValue()); err == nil && val > 0 {
				duration = val
			}
		} else if pair.GetKey() == "DumpPeriod" {
			if val, err := strconv.Atoi(pair.GetValue()); err == nil && val > 0 {
				dumpPeriod = val
			}
		} else if pair.GetKey() == "StartTime" {
			startTime, _ = strconv.ParseInt(pair.GetValue(), 10, 64)
		} else if pair.GetKey() == "CreateTime" {
			createTime, _ = strconv.ParseInt(pair.GetValue(), 10, 64)
		}
	}

	return &PprofTaskCommand{
		BaseCommand: BaseCommand{
			SerialNumber: serialNumber,
			Command:      PprofTaskCommandName,
		},
		taskId:      taskId,
		profileType: profileType,
		duration:    time.Duration(duration) * time.Minute,
		dumpPeriod:  1000 / dumpPeriod,
		startTime:   startTime,
		createTime:  createTime,
	}
}

type PprofTaskService struct {
	logger operator.LogOperator
	entity *reporter.Entity

	pprofFilePath  string
	LastUpdateTime int64

	activePprofFiles map[string]*os.File
}

func NewPprofTaskService(logger operator.LogOperator, entity *reporter.Entity, profileFilePath string) *PprofTaskService {
	return &PprofTaskService{
		logger:           logger,
		entity:           entity,
		pprofFilePath:    profileFilePath,
		activePprofFiles: make(map[string]*os.File),
	}
}

func (service *PprofTaskService) HandleCommand(rawCommand *commonv3.Command) error {
	command := deserializePprofTaskCommand(rawCommand)
	if command.createTime > service.LastUpdateTime {
		service.LastUpdateTime = command.createTime
	}
	if err := command.CheckCommand(); err != nil {
		service.logger.Errorf("check command error, cannot process this profile task. reason %v", err)
		return err
	}

	startTime := time.Duration(command.startTime-time.Now().UnixMilli()) * time.Millisecond

	// The CPU sampling lasts for a duration and then stops
	if command.profileType == PprofTypeCPU {
		time.AfterFunc(startTime, func() {
			_, err := service.startTask(command)
			if err != nil {
				service.logger.Errorf("start CPU pprof error %v \n", err)
				return
			}
			time.AfterFunc(command.duration, func() {
				service.stopTask(command.taskId, command.profileType)
			})
		})
	} else {
		// direct sampling of Memory, Block, and Mutex types
		time.AfterFunc(startTime, func() {
			_, err := service.startTask(command)
			if err != nil {
				service.logger.Errorf("start %s pprof error %v \n", command.profileType, err)
				return
			}
			service.stopTask(command.taskId, command.profileType)
		})
	}

	return nil
}

func (service *PprofTaskService) startTask(command *PprofTaskCommand) (*os.File, error) {
	fileName := service.getPprofFileName(command.profileType)
	var f *os.File
	var err error

	if service.pprofFilePath == "" {
		f, err = os.CreateTemp("", fileName)
	} else {
		f, err = os.Create(filepath.Join(service.pprofFilePath, fileName))
	}
	if err != nil {
		return nil, err
	}

	service.activePprofFiles[command.taskId] = f

	switch command.profileType {
	case PprofTypeCPU:
		runtime.SetCPUProfileRate(command.dumpPeriod)
		if err = pprof.StartCPUProfile(f); err != nil {
			f.Close()
			delete(service.activePprofFiles, command.taskId)
			return nil, err
		}
		service.logger.Infof("CPU profiling task started for %s", command.taskId)
	case PprofTypeMemory:
		service.logger.Infof("Memory profiling task started for %s", command.taskId)
	case PprofTypeBlock:
		runtime.SetBlockProfileRate(command.dumpPeriod)
		service.logger.Infof("Block profiling task started for %s", command.taskId)
	case PprofTypeMutex:
		runtime.SetMutexProfileFraction(command.dumpPeriod)
		service.logger.Infof("Mutex profiling task started for %s", command.taskId)
	default:
		f.Close()
		delete(service.activePprofFiles, command.taskId)
		return nil, fmt.Errorf("unsupported profile type: %s", command.profileType)
	}

	return f, nil
}

func (service *PprofTaskService) getPprofFileName(profileType string) string {
	switch profileType {
	case PprofTypeCPU:
		return CPUPprofFileName
	case PprofTypeMemory:
		return MemoryPprofFileName
	case PprofTypeBlock:
		return BlockPprofFileName
	case PprofTypeMutex:
		return MutexPprofFileName
	default:
		return CPUPprofFileName
	}
}

func (service *PprofTaskService) stopTask(taskId string, profileType string) {
	file, exists := service.activePprofFiles[taskId]
	if !exists {
		service.logger.Errorf("Pprof task file not found for taskId: %s", taskId)
		return
	}

	delete(service.activePprofFiles, taskId)
	filePath := file.Name()

	switch profileType {
	case PprofTypeCPU:
		pprof.StopCPUProfile()
		if err := file.Close(); err != nil {
			service.logger.Errorf("close CPU profile file error %v \n", err)
		}
	case PprofTypeMemory:
		runtime.GC()
		if err := pprof.WriteHeapProfile(file); err != nil {
			service.logger.Errorf("write memory profile error %v \n", err)
		}
		if err := file.Close(); err != nil {
			service.logger.Errorf("close memory profile file error %v \n", err)
		}
	case PprofTypeBlock:
		profile := pprof.Lookup("block")
		if profile != nil {
			if err := profile.WriteTo(file, 0); err != nil {
				service.logger.Errorf("write block profile error %v \n", err)
			}
		}
		if err := file.Close(); err != nil {
			service.logger.Errorf("close block profile file error %v \n", err)
		}
		runtime.SetBlockProfileRate(0)
	case PprofTypeMutex:
		profile := pprof.Lookup("mutex")
		if profile != nil {
			if err := profile.WriteTo(file, 0); err != nil {
				service.logger.Errorf("write mutex profile error %v \n", err)
			}
		}
		if err := file.Close(); err != nil {
			service.logger.Errorf("close mutex profile file error %v \n", err)
		}
		runtime.SetMutexProfileFraction(0)
	default:
		service.logger.Errorf("unsupported profile type: %s", profileType)
		if err := file.Close(); err != nil {
			service.logger.Errorf("close profile file error %v \n", err)
		}
		return
	}

	service.logger.Infof("Pprof task completed for taskId: %s, type: %s", taskId, profileType)

	service.readPprofData(taskId, profileType, filePath)
}

func (service *PprofTaskService) readPprofData(taskId, profileType, filePath string) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		service.logger.Errorf("read pprof file error: %v", err)
		return
	}
	pprofData := &pprofv10.PprofData{
		MetaData: &pprofv10.PprofMetaData{
			Service:         service.entity.ServiceName,
			ServiceInstance: service.entity.ServiceInstanceName,
			TaskId:          taskId,
			Type:            pprofv10.PprofProfilingStatus_PROFILING_SUCCESS,
			ContentSize:     int32(len(content)),
		},
		Result: &pprofv10.PprofData_Content{
			Content: content,
		},
	}
	_ = pprofData

}
