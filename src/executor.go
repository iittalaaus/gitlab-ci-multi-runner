package src

import (
	"fmt"
	"io"
	"time"

	log "github.com/Sirupsen/logrus"
)

type Executor interface {
	Prepare(config *RunnerConfig, build *Build) error
	Start() error
	Wait() error
	Cleanup()
}

func GetExecutor(config RunnerConfig) Executor {
	switch config.Executor {
	case "shell", "":
		return &ShellExecutor{
			BaseExecutor{
				DefaultBuildsDir: "tmp/builds",
			},
		}
	case "docker":
		return &DockerCommandExecutor{
			DockerExecutor{
				BaseExecutor{
					DefaultBuildsDir: "/builds",
				},
			},
		}
	case "docker-ssh":
		return &DockerSshExecutor{
			DockerExecutor{
				BaseExecutor{
					DefaultBuildsDir: "builds",
				},
			},
		}
	default:
		return nil
	}
}

type BaseExecutor struct {
	DefaultBuildsDir string
	config           *RunnerConfig
	build            *Build
	builds_dir       string
	buildAbort       chan bool
	buildLogFinish   chan bool
	buildFinish      chan error
	script_data      []byte
	build_log        io.WriteCloser

	buildAbortFunc func(e *BaseExecutor)
}

func (e *BaseExecutor) debugln(args ...interface{}) {
	args = append([]interface{}{e.config.ShortDescription(), e.build.Id}, args...)
	log.Debugln(args...)
}

func (e *BaseExecutor) println(args ...interface{}) {
	args = append([]interface{}{e.config.ShortDescription(), e.build.Id}, args...)
	log.Println(args...)
}

func (e *BaseExecutor) Prepare(config *RunnerConfig, build *Build) error {
	e.config = config
	e.build = build
	e.buildAbort = make(chan bool, 1)
	e.buildFinish = make(chan error, 1)
	e.buildLogFinish = make(chan bool)

	// Generate build script
	e.builds_dir = e.DefaultBuildsDir
	if len(e.config.BuildsDir) != 0 {
		e.builds_dir = e.config.BuildsDir
	}

	script, err := e.build.Generate(e.builds_dir)
	if err != nil {
		return err
	}
	e.script_data = script

	// Create build log
	build_log, err := e.build.CreateBuildLog()
	if err != nil {
		return err
	}
	e.build_log = build_log
	return nil
}

func (e *BaseExecutor) Cleanup() {
	if e.build != nil {
		e.build.DeleteBuildLog()
	}

	if e.build_log != nil {
		e.build_log.Close()
	}
}

func (e *BaseExecutor) Wait() error {
	var buildState BuildState
	var buildMessage string

	go e.build.WatchTrace(*e.config, e.buildAbort, e.buildLogFinish)

	buildTimeout := e.build.Timeout
	if buildTimeout <= 0 {
		buildTimeout = DEFAULT_TIMEOUT
	}

	// Wait for signals: abort, timeout or finish
	log.Debugln(e.config.ShortDescription(), e.build.Id, "Waiting for signals...")
	select {
	case <-e.buildAbort:
		log.Println(e.config.ShortDescription(), e.build.Id, "Build got aborted.")
		buildState = Failed

		if e.buildAbortFunc != nil {
			e.buildAbortFunc(e)
		}

	case <-time.After(time.Duration(buildTimeout) * time.Second):
		log.Println(e.config.ShortDescription(), e.build.Id, "Build timedout.")
		buildState = Failed
		buildMessage = fmt.Sprintf("\nCI Timeout. Execution took longer then %d seconds", buildTimeout)

		if e.buildAbortFunc != nil {
			e.buildAbortFunc(e)
		}

	case err := <-e.buildFinish:
		// command finished
		if err != nil {
			log.Println(e.config.ShortDescription(), e.build.Id, "Build failed with", err)
			buildState = Failed
			buildMessage = fmt.Sprintf("\nBuild failed with %v", err)
		} else {
			log.Println(e.config.ShortDescription(), e.build.Id, "Build succeeded.")
			buildState = Success
		}
	}

	// wait for update log routine to finish
	log.Debugln(e.config.ShortDescription(), e.build.Id, "Waiting for build log updater to finish")
	e.buildLogFinish <- true
	log.Debugln(e.config.ShortDescription(), e.build.Id, "Build log updater finished.")

	// Send final build state to server
	e.build.FinishBuild(*e.config, buildState, buildMessage)
	return nil
}
