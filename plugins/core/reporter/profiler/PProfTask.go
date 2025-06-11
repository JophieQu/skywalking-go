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

// PprofReporter interface for sending pprof data
// This interface is implemented by Tracer to maintain architectural consistency
type PprofReporter interface {
	ReportPprof(taskId, filePath string)
}

const (
	// Name of the pprof task query command
	PprofTaskCommandName = "PprofTaskQuery"
	// TaskDurationMinMinute Monitor duration must greater than 1 minutes
	TaskDurationMinMinute = 1 * time.Minute
	// TaskDurationMaxMinute The duration of the monitoring task cannot be greater than 15 minutes
	TaskDurationMaxMinute = 15 * time.Minute
)

// CPU profiling rate constants (hz)
const (
	CPUDumpPeriodDefault = 100
	CPUDumpPeriodMin     = 10
	CPUDumpPeriodMax     = 1000
)

// Memory profiling rate constants (KB)
const (
	MemoryDumpPeriodDefault = 64
	MemoryDumpPeriodMin     = 64
	MemoryDumpPeriodMax     = 1024
)

// Block profiling rate constants
const (
	BlockDumpPeriodDefault = 0
	BlockDumpPeriodMin     = 1
	BlockDumpPeriodMax     = 100
)

// Mutex profiling rate constants
const (
	MutexDumpPeriodDefault = 0
	MutexDumpPeriodMin     = 1
	MutexDumpPeriodMax     = 100
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
	// Original dump period value for validation
	originalDumpPeriod int
	// Processed dump period (unit is hz for CPU, converted for internal use)
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

	// Validate dump period based on profile type
	switch c.profileType {
	case PprofTypeCPU:
		if c.originalDumpPeriod < CPUDumpPeriodMin || c.originalDumpPeriod > CPUDumpPeriodMax {
			return fmt.Errorf("CPU dump period must be between %d and %d hz", CPUDumpPeriodMin, CPUDumpPeriodMax)
		}
	case PprofTypeMemory:
		if c.originalDumpPeriod < MemoryDumpPeriodMin || c.originalDumpPeriod > MemoryDumpPeriodMax {
			return fmt.Errorf("Memory dump period must be between %d and %d KB", MemoryDumpPeriodMin, MemoryDumpPeriodMax)
		}
	case PprofTypeBlock:
		if c.originalDumpPeriod < BlockDumpPeriodMin || c.originalDumpPeriod > BlockDumpPeriodMax {
			return fmt.Errorf("Block dump period must be between %d and %d", BlockDumpPeriodMin, BlockDumpPeriodMax)
		}
	case PprofTypeMutex:
		if c.originalDumpPeriod < MutexDumpPeriodMin || c.originalDumpPeriod > MutexDumpPeriodMax {
			return fmt.Errorf("Mutex dump period must be between %d and %d", MutexDumpPeriodMin, MutexDumpPeriodMax)
		}
	}
	return nil
}

func deserializePprofTaskCommand(command *commonv3.Command) *PprofTaskCommand {
	args := command.Args
	taskId := ""
	serialNumber := ""
	profileType := PprofTypeCPU
	duration := 0
	dumpPeriod := -1 // Use -1 to indicate no explicit value provided
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
			if val, err := strconv.Atoi(pair.GetValue()); err == nil && val >= 0 {
				dumpPeriod = val
			}
		} else if pair.GetKey() == "StartTime" {
			startTime, _ = strconv.ParseInt(pair.GetValue(), 10, 64)
		} else if pair.GetKey() == "CreateTime" {
			createTime, _ = strconv.ParseInt(pair.GetValue(), 10, 64)
		}
	}

	// Set default dump period based on profile type if not provided
	if dumpPeriod == -1 {
		switch profileType {
		case PprofTypeCPU:
			dumpPeriod = CPUDumpPeriodDefault
		case PprofTypeMemory:
			dumpPeriod = MemoryDumpPeriodDefault
		case PprofTypeBlock:
			dumpPeriod = BlockDumpPeriodDefault
		case PprofTypeMutex:
			dumpPeriod = MutexDumpPeriodDefault
		default:
			dumpPeriod = CPUDumpPeriodDefault
		}
	}

	// Convert dump period for CPU profiling (hz to nanoseconds per sample)
	finalDumpPeriod := dumpPeriod
	if profileType == PprofTypeCPU {
		finalDumpPeriod = 1000 / dumpPeriod
	}

	return &PprofTaskCommand{
		BaseCommand: BaseCommand{
			SerialNumber: serialNumber,
			Command:      PprofTaskCommandName,
		},
		taskId:             taskId,
		profileType:        profileType,
		duration:           time.Duration(duration) * time.Minute,
		originalDumpPeriod: dumpPeriod,
		dumpPeriod:         finalDumpPeriod,
		startTime:          startTime,
		createTime:         createTime,
	}
}

type PprofTaskService struct {
	logger operator.LogOperator

	pprofFilePath  string
	LastUpdateTime int64

	activePprofFiles map[string]*os.File
	reporter         PprofReporter
}

func NewPprofTaskService(logger operator.LogOperator, profileFilePath string, reporter PprofReporter) *PprofTaskService {
	return &PprofTaskService{
		logger:           logger,
		pprofFilePath:    profileFilePath,
		activePprofFiles: make(map[string]*os.File),
		reporter:         reporter,
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

	if command.profileType == PprofTypeMemory {
		// direct sampling of Memory
		time.AfterFunc(startTime, func() {
			_, err := service.startTask(command)
			if err != nil {
				service.logger.Errorf("start %s pprof error %v \n", command.profileType, err)
				return
			}
			service.stopTask(command.taskId, command.profileType)
		})

	} else {
		// The CPU, Block, and Mutex sampling lasts for a duration and then stops
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

	if reporter := service.getReporter(); reporter != nil {
		reporter.ReportPprof(taskId, filePath)
		service.logger.Infof("Pprof task completed and data sent for taskId: %s", taskId)
	} else {
		service.logger.Errorf("Reporter not available for sending pprof data")
	}
}

func (service *PprofTaskService) getReporter() PprofReporter {
	return service.reporter
}
