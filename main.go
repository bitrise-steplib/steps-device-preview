package main

import (
	"os"

	"github.com/bitrise-io/go-steputils/v2/export"
	"github.com/bitrise-io/go-steputils/v2/stepconf"
	"github.com/bitrise-io/go-utils/v2/command"
	"github.com/bitrise-io/go-utils/v2/env"
	"github.com/bitrise-io/go-utils/v2/log"
	"github.com/bitrise-io/steps-device-preview/step"
)

func main() {
	os.Exit(run())
}

func run() int {
	logger := log.NewLogger()
	previewStep := createStep(logger)

	config, err := previewStep.ProcessConfig()
	if err != nil {
		logger.Errorf("Failed to process Step inputs: %s", err)
		return 1
	}

	result, err := previewStep.Run(config)
	if err != nil {
		// Set `is_skippable: true` on the Step in your Workflow to keep a failure here from
		// failing the build.
		logger.Errorf("Failed to create the device preview: %s", err)
		return 1
	}

	if err := previewStep.ExportOutputs(result); err != nil {
		logger.Errorf("Failed to export Step outputs: %s", err)
		return 1
	}

	return 0
}

func createStep(logger log.Logger) step.DevicePreview {
	envRepository := env.NewRepository()
	inputParser := stepconf.NewInputParser(envRepository)
	exporter := export.NewDefaultExporter(command.NewFactory(envRepository))

	return step.New(logger, inputParser, &exporter)
}
