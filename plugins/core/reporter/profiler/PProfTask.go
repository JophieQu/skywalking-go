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
	commonv3 "skywalking.apache.org/repo/goapi/collect/common/v3"
)

const (
	// Name of the profile task query command
	ProfileTaskCommandName = "ProfileTaskQuery"
	// TaskDurationMinMinute Monitor duration must greater than 1 minutes
	TaskDurationMinMinute = 1 * time.Minute
	// TaskDurationMaxMinute The duration of the monitoring task cannot be greater than 15 minutes
	TaskDurationMaxMinute = 15 * time.Minute
	// Maximum rate for profile data dumping, measured in Hz (samples per second)
	TaskDumpPeriodMaxRate = 100
)

// Profile types
const (
	ProfileTypeCPU    = "CPU"
	ProfileTypeMemory = "MEMORY"
	ProfileTypeBlock  = "BLOCK"
	ProfileTypeMutex  = "MUTEX"
)

// Profile file names
const (
	CPUProfileFileName    = "cpu.pprof"
	MemoryProfileFileName = "memory.pprof"
	BlockProfileFileName  = "block.pprof"
	MutexProfileFileName  = "mutex.pprof"
)

type ProfileTaskCommand struct {
	BaseCommand
	// Task ID uniquely identifies a profiling task
	taskId string
	// Name of the endpoint being profiled
	endpointName string
	// Type of profiling (CPU/Memory/Block/Mutex)
	profileType string
	// unit is minute
	duration time.Duration
	// Unix timestamp in milliseconds when the task should start
	startTime int64
	// Unix timestamp in milliseconds when the task was created
	createTime int64
	// unit is hz
	dumpPeriod int
	// Minimum duration threshold for profiling in milliseconds
	minDurationThreshold int
	// Maximum number of samples that can be collected
	maxSamplingCount int
}

func (c *ProfileTaskCommand) CheckCommand() error {
	if c.endpointName == "" {
		return fmt.Errorf("endpoint name cannot be empty")
	}
	if c.profileType == "" {
		c.profileType = ProfileTypeCPU
	}
	if c.profileType != ProfileTypeCPU && c.profileType != ProfileTypeMemory &&
		c.profileType != ProfileTypeBlock && c.profileType != ProfileTypeMutex {
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

func deserializeProfileTaskCommand(command *commonv3.Command) *ProfileTaskCommand {
	args := command.Args
	taskId := ""
	serialNumber := ""
	endpointName := ""
	profileType := ProfileTypeCPU
	duration := 0
	minDurationThreshold := 0
	dumpPeriod := 100
	maxSamplingCount := 0
	var startTime int64 = 0
	var createTime int64 = 0
	for _, pair := range args {
		if pair.GetKey() == "SerialNumber" {
			serialNumber = pair.GetValue()
		} else if pair.GetKey() == "EndpointName" {
			endpointName = pair.GetValue()
		} else if pair.GetKey() == "TaskId" {
			taskId = pair.GetValue()
		} else if pair.GetKey() == "ProfileType" {
			profileType = pair.GetValue()
		} else if pair.GetKey() == "Duration" {
			if val, err := strconv.Atoi(pair.GetValue()); err == nil && val > 0 {
				duration = val
			}
		} else if pair.GetKey() == "MinDurationThreshold" {
			minDurationThreshold, _ = strconv.Atoi(pair.GetValue())
		} else if pair.GetKey() == "DumpPeriod" {
			if val, err := strconv.Atoi(pair.GetValue()); err == nil && val > 0 {
				dumpPeriod = val
			}
		} else if pair.GetKey() == "MaxSamplingCount" {
			maxSamplingCount, _ = strconv.Atoi(pair.GetValue())
		} else if pair.GetKey() == "StartTime" {
			startTime, _ = strconv.ParseInt(pair.GetValue(), 10, 64)
		} else if pair.GetKey() == "CreateTime" {
			createTime, _ = strconv.ParseInt(pair.GetValue(), 10, 64)
		}
	}

	return &ProfileTaskCommand{
		BaseCommand: BaseCommand{
			SerialNumber: serialNumber,
			Command:      ProfileTaskCommandName,
		},
		taskId:               taskId,
		endpointName:         endpointName,
		profileType:          profileType,
		duration:             time.Duration(duration) * time.Minute,
		minDurationThreshold: minDurationThreshold,
		dumpPeriod:           1000 / dumpPeriod,
		maxSamplingCount:     maxSamplingCount,
		startTime:            startTime,
		createTime:           createTime,
	}
}

type ProfileTaskService struct {
	logger operator.LogOperator

	pprofFilePath  string
	LastUpdateTime int64

	taskInfo           map[string]*ProfileTaskCommand
	activeProfileFiles map[string]*os.File
}

func NewProfileTaskService(logger operator.LogOperator, profileFilePath string) *ProfileTaskService {
	return &ProfileTaskService{
		logger:             logger,
		pprofFilePath:      profileFilePath,
		activeProfileFiles: make(map[string]*os.File),
		taskInfo:           make(map[string]*ProfileTaskCommand),
	}
}

func (service *ProfileTaskService) HandleCommand(rawCommand *commonv3.Command) error {
	command := deserializeProfileTaskCommand(rawCommand)
	if command.createTime > service.LastUpdateTime {
		service.LastUpdateTime = command.createTime
	}
	if err := command.CheckCommand(); err != nil {
		service.logger.Errorf("check command error, cannot process this profile task. reason %v", err)
		return err
	}

	service.taskInfo[command.taskId] = command

	startTime := time.Duration(command.startTime-time.Now().UnixMilli()) * time.Millisecond

	// The CPU sampling lasts for a duration and then stops
	if command.profileType == ProfileTypeCPU {
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

func (service *ProfileTaskService) startTask(command *ProfileTaskCommand) (*os.File, error) {
	fileName := service.getProfileFileName(command.profileType)
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

	service.activeProfileFiles[command.taskId] = f

	switch command.profileType {
	case ProfileTypeCPU:
		runtime.SetCPUProfileRate(command.dumpPeriod)
		if err = pprof.StartCPUProfile(f); err != nil {
			f.Close()
			delete(service.activeProfileFiles, command.taskId)
			return nil, err
		}
		service.logger.Infof("CPU profiling task started for %s", command.taskId)
	case ProfileTypeMemory:
		service.logger.Infof("Memory profiling task started for %s", command.taskId)
	case ProfileTypeBlock:
		runtime.SetBlockProfileRate(command.dumpPeriod)
		service.logger.Infof("Block profiling task started for %s", command.taskId)
	case ProfileTypeMutex:
		runtime.SetMutexProfileFraction(command.dumpPeriod)
		service.logger.Infof("Mutex profiling task started for %s", command.taskId)
	default:
		f.Close()
		delete(service.activeProfileFiles, command.taskId)
		return nil, fmt.Errorf("unsupported profile type: %s", command.profileType)
	}

	return f, nil
}

func (service *ProfileTaskService) getProfileFileName(profileType string) string {
	switch profileType {
	case ProfileTypeCPU:
		return CPUProfileFileName
	case ProfileTypeMemory:
		return MemoryProfileFileName
	case ProfileTypeBlock:
		return BlockProfileFileName
	case ProfileTypeMutex:
		return MutexProfileFileName
	default:
		return CPUProfileFileName
	}
}

func (service *ProfileTaskService) stopTask(taskId string, profileType string) {
	file, exists := service.activeProfileFiles[taskId]
	if !exists {
		service.logger.Errorf("Profile task file not found for taskId: %s", taskId)
		return
	}

	delete(service.activeProfileFiles, taskId)

	switch profileType {
	case ProfileTypeCPU:
		pprof.StopCPUProfile()
		if err := file.Close(); err != nil {
			service.logger.Errorf("close CPU profile file error %v \n", err)
		}
	case ProfileTypeMemory:
		runtime.GC()
		if err := pprof.WriteHeapProfile(file); err != nil {
			service.logger.Errorf("write memory profile error %v \n", err)
		}
		if err := file.Close(); err != nil {
			service.logger.Errorf("close memory profile file error %v \n", err)
		}
	case ProfileTypeBlock:
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
	case ProfileTypeMutex:
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

	service.logger.Infof("Profile task completed for taskId: %s, type: %s", taskId, profileType)
}

func (service *ProfileTaskService) GetTaskInfo(taskId string) (*ProfileTaskCommand, bool) {
	task, exists := service.taskInfo[taskId]
	return task, exists
}
