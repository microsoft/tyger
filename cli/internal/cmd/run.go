// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alecthomas/units"
	"github.com/docker/cli/cli/connhelper"
	dockerimage "github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
	"github.com/dustin/go-humanize"
	"github.com/google/uuid"
	"github.com/hashicorp/go-cleanhttp"
	"github.com/kaz-yamam0t0/go-timeparser/timeparser"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/microsoft/tyger/cli/internal/dataplane"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/thediveo/enumflag"
	"sigs.k8s.io/yaml"
)

var errNotFound = errors.New("not found")

func NewRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "run",
		Aliases:               []string{"runs"},
		Short:                 "Manage runs",
		Long:                  `Manage runs`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	cmd.AddCommand(newRunCreateCommand())
	cmd.AddCommand(newRunExecCommand())
	cmd.AddCommand(newRunShowCommand())
	cmd.AddCommand(newRunWatchCommand())
	cmd.AddCommand(newRunLogsCommand())
	cmd.AddCommand(newRunListCommand())
	cmd.AddCommand(newRunSetCommand())
	cmd.AddCommand(newRunCountsCommand())
	cmd.AddCommand(newRunCancelCommand())

	return cmd
}

func newRunCreateCommand() *cobra.Command {
	cmd := newRunCreateCommandCore("create", nil, func(ctx context.Context, r model.Run) error {
		fmt.Println(r.Id)
		return nil
	})

	cmd.Short = "Creates a run."
	cmd.Long = `Creates a run. Writes the run ID to stdout on success.`

	return cmd
}

func newRunExecCommand() *cobra.Command {
	logs := false
	logTimestamps := false

	var inputBufferParameter string
	var outputBufferParameter string

	preValidate := func(ctx context.Context, run model.Run) error {
		var resolvedCodespec model.Codespec

		if run.Job.Codespec.Inline != nil {
			resolvedCodespec = model.Codespec(*run.Job.Codespec.Inline)
		} else if run.Job.Codespec.Named != nil {
			relativeUri := fmt.Sprintf("/codespecs/%s", *run.Job.Codespec.Named)
			_, err := controlplane.InvokeRequest(ctx, http.MethodGet, relativeUri, nil, nil, &resolvedCodespec)
			if err != nil {
				return err
			}
		} else {
			return errors.New("a codespec for the job must be specified")
		}

		bufferParameters := resolvedCodespec.Buffers
		if bufferParameters == nil {
			return nil
		}
		unmappedInputBuffers := make([]string, 0)
		for _, input := range bufferParameters.Inputs {
			if id, ok := run.Job.Buffers[input]; !ok || id == "_" {
				unmappedInputBuffers = append(unmappedInputBuffers, input)
			}
		}
		unmappedOutputBuffers := make([]string, 0)
		for _, output := range bufferParameters.Outputs {
			if id, ok := run.Job.Buffers[output]; !ok || id == "_" {
				unmappedOutputBuffers = append(unmappedOutputBuffers, output)
			}
		}
		switch len(unmappedInputBuffers) {
		case 0:
			break
		case 1:
			inputBufferParameter = unmappedInputBuffers[0]
		default:
			return errors.New("exec cannot be called if the job has multiple unmapped input buffers")
		}

		switch len(unmappedOutputBuffers) {
		case 0:
			break
		case 1:
			outputBufferParameter = unmappedOutputBuffers[0]
		default:
			return errors.New("exec cannot be called if the job has multiple unmapped output buffers")
		}

		return nil
	}

	blockSize := dataplane.DefaultBlockSize
	writeDop := dataplane.DefaultWriteDop
	readDop := dataplane.DefaultReadDop

	postCreate := func(ctx context.Context, run model.Run) error {
		return attachToRun(ctx, run, inputBufferParameter, outputBufferParameter, blockSize, writeDop, readDop, logs, logTimestamps, os.Stderr)
	}

	cmd := newRunCreateCommandCore("exec", preValidate, postCreate)

	blockSizeString := ""
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		warnIfRunningInPowerShell()

		if blockSizeString != "" {
			if blockSizeString != "" && blockSizeString[len(blockSizeString)-1] != 'B' {
				blockSizeString += "B"
			}
			parsedBlockSize, err := units.ParseBase2Bytes(blockSizeString)
			if err != nil {
				return err
			}

			blockSize = int(parsedBlockSize)
		}

		return nil
	}

	cmd.Short = "Creates a run and reads and writes to its buffers."
	cmd.Long = `Creates a run.
If the job has a single input buffer, stdin is streamed to the buffer.
If the job has a single output buffer, stdout is streamed from the buffer.`

	cmd.Flags().BoolVar(&logs, "logs", false, "Print run logs to stderr.")
	cmd.Flags().BoolVar(&logTimestamps, "timestamps", false, "Print run logs with timestamps.")

	cmd.Flags().StringVar(&blockSizeString, "block-size", blockSizeString, "Split the input stream into buffer blocks of this size.")
	cmd.Flags().IntVar(&writeDop, "write-dop", writeDop, "The degree of parallelism for writing to the input buffer.")
	cmd.Flags().IntVar(&readDop, "read-dop", readDop, "The degree of parallelism for reading from the output buffer.")

	return cmd
}

func attachToRunNoBufferIO(ctx context.Context, run model.Run, logs bool, logTimestamps bool, logSink io.Writer) error {
	return attachToRun(ctx, run, "", "", dataplane.DefaultBlockSize, dataplane.DefaultWriteDop, dataplane.DefaultReadDop, logs, logTimestamps, logSink)
}

func attachToRun(ctx context.Context, run model.Run, inputBufferParameter, outputBufferParameter string, blockSize int, writeDop int, readDop int, logs bool, logTimestamps bool, logSink io.Writer) error {
	log.Logger = log.Logger.With().Int64("runId", run.Id).Logger()
	log.Info().Msg("Run created")
	var inputSasUri *url.URL
	var outputSasUri *url.URL
	var err error
	if inputBufferParameter != "" {
		bufferId := run.Job.Buffers[inputBufferParameter]
		inputSasUri, err = getBufferAccessUri(ctx, bufferId, true)
		if err != nil {
			return err
		}
	}
	if outputBufferParameter != "" {
		bufferId := run.Job.Buffers[outputBufferParameter]
		outputSasUri, err = getBufferAccessUri(ctx, bufferId, false)
		if err != nil {
			return err
		}
	}

	mainWg := sync.WaitGroup{}

	var stopFunc context.CancelFunc
	ctx, stopFunc = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-ctx.Done()
		stopFunc()
		log.Warn().Msg("Canceling...")
	}()

	if inputSasUri != nil {
		mainWg.Add(1)
		go func() {
			defer mainWg.Done()
			err := dataplane.Write(ctx, inputSasUri, os.Stdin,
				dataplane.WithWriteBlockSize(blockSize),
				dataplane.WithWriteDop(writeDop))
			if err != nil {
				if errors.Is(err, ctx.Err()) {
					err = ctx.Err()
				}
				log.Fatal().Err(err).Msg("Failed to write input")
			}
		}()
	}

	if outputSasUri != nil {
		mainWg.Add(1)
		go func() {
			defer mainWg.Done()
			err := dataplane.Read(ctx, outputSasUri, os.Stdout,
				dataplane.WithReadDop(readDop))
			if err != nil {
				if errors.Is(err, ctx.Err()) {
					err = ctx.Err()
				}
				log.Fatal().Err(err).Msg("Failed to read output")
			}
		}()
	}

	logsWg := sync.WaitGroup{}
	if logs {
		logsWg.Add(1)
		go func() {
			defer logsWg.Done()
			err := getLogs(ctx, strconv.FormatInt(run.Id, 10), logTimestamps, -1, nil, true, logSink)
			if err != nil {
				log.Error().Err(err).Msg("Failed to get logs")
			}
		}()
	}

	consecutiveErrors := 0
beginWatch:
	var runFailedErr error
	eventChan, errChan := watchRun(ctx, run.Id)

	lastStatus := model.RunStatus(math.MaxUint)
	for {
		select {
		case err := <-errChan:
			log.Error().Err(err).Msg("Error while watching run")
			consecutiveErrors++

			if consecutiveErrors > 1 {
				log.Fatal().Err(err).Msg("Failed to watch run")
			}

			goto beginWatch
		case event, ok := <-eventChan:
			if !ok {
				goto end
			}
			consecutiveErrors = 0
			if event.Status != nil {
				if *event.Status != lastStatus {
					lastStatus = *event.Status
					logEntry := log.Info().Str("status", event.Status.String())
					if event.RunningCount != nil {
						logEntry = logEntry.Int("runningCount", *event.RunningCount)
					}
					logEntry.Msg("Run status changed")
				}

				switch *event.Status {
				case model.Succeeded:
					goto end
				case model.Pending:
				case model.Running:
				default:
					msg := fmt.Sprintf("run failed with status %s", event.Status.String())
					if event.StatusReason != "" {
						msg = fmt.Sprintf("%s (%s)", msg, event.StatusReason)
					}
					runFailedErr = errors.New(msg)
					goto end
				}
			}
		}
	}

end:
	if runFailedErr == nil {
		mainWg.Wait()
	}

	if logs {
		// The run has completed and we have received all data. We just need to wait for the logs to finish streaming,
		// but we will give up after a period of time.
		c := make(chan struct{})
		go func() {
			defer close(c)
			logsWg.Wait()
		}()

		select {
		case <-c:
			break
		case <-time.After(20 * time.Second):
			log.Warn().Msg("Timed out waiting for logs to finish streaming")
		}
	}

	if runFailedErr != nil {
		log.Fatal().Err(runFailedErr).Msg("Run failed")
	}

	return nil
}

func newRunCreateCommandCore(
	commandName string,
	preValidate func(context.Context, model.Run) error,
	postCreate func(context.Context, model.Run) error) *cobra.Command {
	type codeTargetFlags struct {
		codespec        string
		codespecVersion string
		buffers         map[string]string
		bufferTags      map[string]string
		bufferTtl       string
		nodePool        string
		replicas        int
	}
	var flags struct {
		specFile string
		job      codeTargetFlags
		worker   codeTargetFlags
		cluster  string
		timeout  string
		pull     bool
		tags     map[string]string
	}

	getCodespecRef := func(ctf codeTargetFlags) model.CodespecRef {
		namedRef := model.NamedCodespecRef(ctf.codespec)
		if ctf.codespecVersion != "" {
			namedRef = model.NamedCodespecRef(fmt.Sprintf("%s/versions/%s", ctf.codespec, ctf.codespecVersion))
		}
		return model.CodespecRef{Named: &namedRef}
	}

	cmd := &cobra.Command{
		Use:                   fmt.Sprintf(`%s [--file YAML_SPEC] [other flags]`, commandName),
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {

			newRun := model.Run{}
			if flags.specFile != "" {
				bytes, err := os.ReadFile(flags.specFile)
				if err != nil {
					return fmt.Errorf("failed to read file %s: %w", flags.specFile, err)
				}

				err = yaml.UnmarshalStrict(bytes, &newRun)
				if err != nil {
					return fmt.Errorf("failed to parse file %s: %w", flags.specFile, err)
				}

				if newRun.Job.Codespec.Inline != nil {
					newRun.Job.Codespec.Inline.Kind = "job"
				}
				if newRun.Worker != nil && newRun.Worker.Codespec.Inline != nil {
					newRun.Worker.Codespec.Inline.Kind = "worker"
				}
			}

			if flags.job.codespec != "" {
				newRun.Job.Codespec = getCodespecRef(flags.job)
			}
			if len(flags.job.buffers) > 0 {
				if newRun.Job.Buffers == nil {
					newRun.Job.Buffers = map[string]string{}
				}
				for k, v := range flags.job.buffers {
					newRun.Job.Buffers[k] = v
				}
			}
			if len(flags.job.bufferTags) > 0 {
				if newRun.Job.Tags == nil {
					newRun.Job.Tags = map[string]string{}
				}
				for k, v := range flags.job.bufferTags {
					newRun.Job.Tags[k] = v
				}
			}
			if len(flags.job.bufferTtl) > 0 {
				timeSpanRegex := regexp.MustCompile(`^(\d+)[.](\d\d):(\d\d)(:(\d\d))?$`)
				if !timeSpanRegex.MatchString(flags.job.bufferTtl) {
					return fmt.Errorf("invalid buffer TTL format: %s (should be D.HH:MM)", flags.job.bufferTtl)
				}
				newRun.Job.BufferTtl = flags.job.bufferTtl
			}
			if flags.job.nodePool != "" {
				newRun.Job.NodePool = flags.job.nodePool
			}

			if hasFlagChanged(cmd, "replicas") {
				newRun.Job.Replicas = flags.job.replicas
			}

			cmd.Flags().VisitAll(func(f *pflag.Flag) {
				if newRun.Worker == nil && f.Changed && strings.HasPrefix(f.Name, "worker") {
					newRun.Worker = &model.RunCodeTarget{}
				}
			})

			if newRun.Worker != nil {
				if flags.worker.codespec != "" {
					newRun.Worker.Codespec = getCodespecRef(flags.worker)
				}
				if flags.worker.nodePool != "" {
					newRun.Worker.NodePool = flags.worker.nodePool
				}

				if hasFlagChanged(cmd, "worker-replicas") {
					newRun.Worker.Replicas = flags.worker.replicas
				}
			}

			if flags.cluster != "" {
				newRun.Cluster = flags.cluster
			}

			if flags.timeout != "" {
				duration, err := time.ParseDuration(flags.timeout)
				if err != nil {
					return err
				}

				seconds := int(duration.Seconds())
				newRun.TimeoutSeconds = &seconds
			}

			if len(flags.tags) > 0 {
				if newRun.Tags == nil {
					newRun.Tags = map[string]string{}
				}
				for k, v := range flags.tags {
					newRun.Tags[k] = v
				}
			}

			if preValidate != nil {
				err := preValidate(cmd.Context(), newRun)
				if err != nil {
					return err
				}
			}

			if flags.pull {
				if err := pullImages(cmd.Context(), newRun); err != nil {
					return err
				}
			}

			committedRun := model.Run{}
			customHeaders := controlplane.WithHeaders(http.Header{"Idempotency-Key": []string{uuid.New().String()}})
			_, err := controlplane.InvokeRequest(cmd.Context(), http.MethodPost, "/runs", nil, newRun, &committedRun, customHeaders)
			if err != nil {
				return err
			}

			if postCreate != nil {
				err = postCreate(cmd.Context(), committedRun)
				if err != nil {
					return err
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&flags.specFile, "file", "f", "", "A YAML file with the run specification. All other flags override the values in the file.")

	cmd.Flags().StringVarP(&flags.job.codespec, "codespec", "c", "", "The name of the job codespec to execute")
	cmd.Flags().StringVar(&flags.job.codespecVersion, "version", "", "The version of the job codespec to execute")
	cmd.Flags().IntVarP(&flags.job.replicas, "replicas", "r", 1, "The number of parallel job replicas. Defaults to 1.")
	cmd.Flags().StringVar(&flags.job.nodePool, "node-pool", "", "The name of the nodepool to execute the job in")
	cmd.Flags().StringToStringVarP(&flags.job.buffers, "buffer", "b", nil, "Maps a codespec buffer parameter to a buffer ID")
	cmd.Flags().StringToStringVar(&flags.job.bufferTags, "buffer-tag", nil, "A key-value tag to be applied to any buffer created by the job. Can be specified multiple times.")
	cmd.Flags().StringVar(&flags.job.bufferTtl, "buffer-ttl", "", "The time-to-live for any buffer created by the job (format D.HH:MM)")

	cmd.Flags().StringVar(&flags.worker.codespec, "worker-codespec", "", "The name of the optional worker codespec to execute")
	cmd.Flags().StringVar(&flags.worker.codespecVersion, "worker-version", "", "The version of the optional worker codespec to execute")
	cmd.Flags().IntVar(&flags.worker.replicas, "worker-replicas", 1, "The number of parallel worker replicas. Defaults to 1 if a worker is specified.")
	cmd.Flags().StringVar(&flags.worker.nodePool, "worker-node-pool", "", "The name of the nodepool to execute the optional worker codespec in")

	cmd.Flags().StringVar(&flags.cluster, "cluster", "", "The name of the cluster to execute in")
	cmd.Flags().StringVar(&flags.timeout, "timeout", "", `How log before the run times out. Specified in a sequence of decimal numbers, each with optional fraction and a unit suffix, such as "300s", "1.5h" or "2h45m". Valid time units are "s", "m", "h"`)
	cmd.Flags().StringToStringVar(&flags.tags, "tag", nil, "A key-value tag to be added to the run. Can be specified multiple times.")

	cmd.Flags().BoolVar(&flags.pull, "pull", false, "Pull container images. Applies only to Tyger running in Docker.")

	return cmd
}

func newRunShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:                   "show ID",
		Aliases:               []string{"get"},
		Short:                 "Show the details of a run",
		Long:                  `Show the details of a run.`,
		DisableFlagsInUseLine: true,
		Args:                  exactlyOneArg("run name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			run := model.Run{}
			_, err := controlplane.InvokeRequest(cmd.Context(), http.MethodGet, fmt.Sprintf("/runs/%s", args[0]), nil, nil, &run)
			if err != nil {
				return err
			}

			formattedRun, err := json.MarshalIndent(run, "", "  ")
			if err != nil {
				return err
			}

			fmt.Println(string(formattedRun))
			return nil
		},
	}
}

func newRunWatchCommand() *cobra.Command {
	var flags struct {
		fullResource bool
	}

	cmd := &cobra.Command{
		Use:                   "watch ID",
		Aliases:               []string{"get"},
		Short:                 "Watch the status changes of a run",
		Long:                  "Watch the status changes of a run",
		DisableFlagsInUseLine: true,
		Args:                  exactlyOneArg("run name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			runId, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return err
			}

			consecutiveErrors := 0
		start:
			eventChan, errChan := watchRun(cmd.Context(), runId)
			var lastBytes []byte
			for {
				select {
				case err := <-errChan:
					if err == errNotFound {
						return errors.New("run not found")
					}

					consecutiveErrors++
					if consecutiveErrors > 1 {
						return err
					}

					log.Error().Err(err).Msg("error watching run")
					goto start
				case event, ok := <-eventChan:
					if !ok {
						return nil
					}
					consecutiveErrors = 0
					var valueToPrint any = event
					if !flags.fullResource {
						valueToPrint = event.RunMetadata
					}

					bytes, err := json.Marshal(valueToPrint)
					if err != nil {
						return err
					}
					if !slices.Equal(bytes, lastBytes) {
						fmt.Println(string(bytes))
						lastBytes = bytes
					}
				}
			}
		},
	}

	cmd.Flags().BoolVar(&flags.fullResource, "full-resource", false, "Display the full resource instead of just the system fields")
	return cmd
}

func newRunListCommand() *cobra.Command {
	var flags struct {
		limit    int
		since    string
		tags     map[string]string
		statuses []model.RunStatus
	}

	cmd := &cobra.Command{
		Use:                   "list [--since DATE/TIME] [--tag key=value ...] [--status STATUS] [--limit COUNT]",
		Short:                 "List runs",
		Long:                  `List runs. Runs are sorted by descending created time.`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			queryOptions := url.Values{}
			if flags.limit > 0 {
				queryOptions.Add("limit", strconv.Itoa(flags.limit))
			} else {
				flags.limit = math.MaxInt
			}
			if flags.since != "" {
				now := time.Now()
				tm, err := timeparser.ParseTimeStr(flags.since, &now)
				if err != nil {
					return fmt.Errorf("failed to parse time %s", flags.since)
				}
				queryOptions.Add("since", tm.UTC().Format(time.RFC3339Nano))
			}

			for k, v := range flags.tags {
				queryOptions.Add(fmt.Sprintf("tag[%s]", k), v)
			}

			for _, status := range flags.statuses {
				queryOptions.Add("status", status.String())
			}

			return controlplane.InvokePageRequests[model.Run](cmd.Context(), "/runs", queryOptions, flags.limit, !cmd.Flags().Lookup("limit").Changed)
		},
	}

	runStatuses := map[model.RunStatus][]string{}
	for _, status := range model.RunStatuses {
		runStatuses[status] = []string{status.String()}
	}

	cmd.Flags().StringVarP(&flags.since, "since", "s", "", "Results before this datetime (specified in local time) are not included")
	cmd.Flags().StringToStringVar(&flags.tags, "tag", nil, "Only include runs with the given tag. Can be specified multiple times.")
	cmd.Flags().Var(
		enumflag.NewSlice(&flags.statuses, "status", runStatuses, enumflag.EnumCaseInsensitive),
		"status",
		"Only include runs with the given status. When specified multiple times, any of the given statuses are matched.")
	cmd.Flags().IntVarP(&flags.limit, "limit", "l", 1000, "The maximum number of runs to list. Default 1000")

	return cmd
}

func newRunSetCommand() *cobra.Command {
	var etag string
	tags := make(map[string]string)
	clearTags := false
	cmd := &cobra.Command{
		Use:                   "set ID [--clear-tags] [--tag key=value ...] [--etag ETAG]",
		Short:                 "Updates or replaces tags set on a run",
		Long:                  "Updates or replaces tags set on a run",
		Args:                  exactlyOneArg("Run ID"),
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			output := model.Run{}
			return controlplane.SetFieldsOnEntity(cmd.Context(), fmt.Sprintf("/runs/%s", args[0]), nil, etag, clearTags, tags, nil, &output)
		},
	}

	cmd.Flags().BoolVar(&clearTags, "clear-tags", clearTags, "clear all existing tags from the buffer and replace them with the new tags. If not specified, the existing tags are preserved and updated.")
	cmd.Flags().StringToStringVar(&tags, "tag", nil, "add or update a key-value tag to the buffer. Can be specified multiple times.")
	cmd.Flags().StringVar(&etag, "etag", etag, "the ETag read ETag to guard against concurrent updates, ")

	return cmd
}

func newRunCountsCommand() *cobra.Command {
	var flags struct {
		since string
		tags  map[string]string
	}

	cmd := &cobra.Command{
		Use:                   "counts [--since DATE/TIME]",
		Short:                 "Shows the count of runs by status",
		Aliases:               []string{"count"},
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			queryOptions := url.Values{}
			if flags.since != "" {
				now := time.Now()
				tm, err := timeparser.ParseTimeStr(flags.since, &now)
				if err != nil {
					return fmt.Errorf("failed to parse time %s", flags.since)
				}
				queryOptions.Add("since", tm.UTC().Format(time.RFC3339Nano))
			}

			for k, v := range flags.tags {
				queryOptions.Add(fmt.Sprintf("tag[%s]", k), v)
			}

			results := map[string]int{}
			if _, err := controlplane.InvokeRequest(cmd.Context(), http.MethodGet, "/runs/counts", queryOptions, nil, &results); err != nil {
				return err
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(results)
			return nil
		},
	}

	cmd.Flags().StringVarP(&flags.since, "since", "s", "", "Results before this datetime (specified in local time) are not included")
	cmd.Flags().StringToStringVar(&flags.tags, "tag", nil, "Only include runs with the given tag. Can be specified multiple times.")

	return cmd
}

func newRunCancelCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "cancel ID",
		Aliases:               []string{"stop"},
		Short:                 "Cancel a run",
		Long:                  "Cancel a run",
		DisableFlagsInUseLine: true,
		Args:                  exactlyOneArg("run name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			run := model.Run{}
			_, err := controlplane.InvokeRequest(cmd.Context(), http.MethodPost, fmt.Sprintf("/runs/%s/cancel", args[0]), nil, nil, &run)

			if err != nil {
				return err
			}

			if run.Status == nil {
				return fmt.Errorf("unable to cancel job %s", args[0])
			} else if *run.Status == model.Canceling {
				fmt.Println("Cancel issued for job", args[0])
			} else if *run.Status == model.Canceled {
				fmt.Println("Job", args[0], "has been canceled")
			} else {
				if run.StatusReason != "" {
					return fmt.Errorf("unable to cancel job %s because its status is %s (%s)", args[0], run.Status.String(), run.StatusReason)
				} else {
					return fmt.Errorf("unable to cancel job %s because its status is %s", args[0], run.Status.String())
				}
			}

			return nil
		},
	}

	return cmd
}

func newRunLogsCommand() *cobra.Command {
	var flags struct {
		timestamps bool
		tailLines  int
		since      string
		follow     bool
	}

	cmd := &cobra.Command{
		Use:                   "logs RUNID",
		Short:                 "Get the logs of a run",
		Long:                  `Get the logs of a run.`,
		DisableFlagsInUseLine: true,
		Args:                  exactlyOneArg("run ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var sinceTime *time.Time
			if flags.since != "" {
				now := time.Now()
				var err error
				sinceTime, err = timeparser.ParseTimeStr(flags.since, &now)
				if err != nil {
					return fmt.Errorf("failed to parse time %s", flags.since)
				}
			}

			return getLogs(cmd.Context(), args[0], flags.timestamps, flags.tailLines, sinceTime, flags.follow, os.Stdout)
		},
	}

	cmd.Flags().BoolVar(&flags.timestamps, "timestamps", false, "Include timestamps on each line in the log output")
	cmd.Flags().IntVar(&flags.tailLines, "tail", -1, "Lines of recent log file to display")
	cmd.Flags().StringVarP(&flags.since, "since", "s", "", "Lines before this datetime (specified in local time) are not included")
	cmd.Flags().BoolVarP(&flags.follow, "follow", "f", false, "Specify if the logs should be streamed")

	return cmd
}

func getLogs(ctx context.Context, runId string, timestamps bool, tailLines int, since *time.Time, follow bool, outputSink io.Writer) error {
	queryOptions := url.Values{}
	if follow || timestamps {
		queryOptions.Add("timestamps", "true")
	}
	if tailLines >= 0 {
		queryOptions.Add("tailLines", strconv.Itoa(tailLines))
	}
	if since != nil {
		queryOptions.Add("since", since.UTC().Format(time.RFC3339Nano))
	}

	if follow {
		queryOptions.Add("follow", "true")
	}

	// If the connection drops while we are following logs, we'll try again from the last received timestamp

	for {
		queryString := queryOptions.Encode()
		if len(queryString) > 0 {
			queryString = "?" + queryString
		}
		resp, err := controlplane.InvokeRequest(ctx, http.MethodGet, fmt.Sprintf("/runs/%s/logs%s", runId, queryString), nil, nil, nil, controlplane.WithLeaveResponseOpen())
		if err != nil {
			return err
		}

		defer resp.Body.Close()

		if !follow {
			_, err = io.Copy(outputSink, resp.Body)
			return err
		}

		lastTimestamp, err := followLogs(resp.Body, timestamps, outputSink)
		if err == nil || err == io.EOF {
			return nil
		}

		if len(lastTimestamp) > 0 {
			queryOptions.Set("since", lastTimestamp)
		}
	}
}

func followLogs(body io.Reader, includeTimestamps bool, outputSink io.Writer) (lastTimestamp string, err error) {
	reader := bufio.NewReader(body)
	atStartOfLine := true
	for {
		if atStartOfLine {
			localLastTimestamp, err := reader.ReadString(' ')
			if err != nil {
				return lastTimestamp, err
			}
			lastTimestamp = localLastTimestamp
			if includeTimestamps {
				fmt.Fprint(outputSink, lastTimestamp)
			}
		}

		line, isPrefix, err := reader.ReadLine()
		if err != nil {
			return lastTimestamp, err
		}

		atStartOfLine = !isPrefix
		if isPrefix {
			fmt.Fprint(outputSink, string(line))
		} else {
			fmt.Fprintln(outputSink, string(line))
		}
	}
}

func watchRun(ctx context.Context, runId int64) (<-chan model.Run, <-chan error) {
	runEventChan := make(chan model.Run)
	errChan := make(chan error)

	go func() {
		defer close(runEventChan)

		options := url.Values{}
		options.Add("watch", "true")
		resp, err := controlplane.InvokeRequest(ctx, http.MethodGet, fmt.Sprintf("/runs/%d", runId), options, nil, nil, controlplane.WithLeaveResponseOpen())
		if err != nil {
			errChan <- err
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			errChan <- errNotFound
			return
		}

		if resp.StatusCode != http.StatusOK {
			errChan <- fmt.Errorf("unexpected response code %d", resp.StatusCode)
			return
		}

		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err == io.EOF {
				if line != "" {
					panic("expected last line to be empty")
				}
				return
			}
			if err != nil {
				errChan <- err
				return
			}

			run := model.Run{}

			if err := json.Unmarshal([]byte(line), &run); err != nil {
				errChan <- err
				return
			}

			runEventChan <- run
		}
	}()

	return runEventChan, errChan
}

func pullImages(ctx context.Context, newRun model.Run) error {
	serviceMetadata := model.ServiceMetadata{}
	_, err := controlplane.InvokeRequest(ctx, http.MethodGet, "/metadata", nil, nil, &serviceMetadata)
	if err != nil {
		return fmt.Errorf("failed to get service metadata: %w", err)
	}

	if !slices.Contains(serviceMetadata.Capabilities, "Docker") {
		log.Warn().Msg("The --pull parameter is only supported when running Tyger in Docker.")
	} else {
		imagesToPull := make([]string, 0)
		accumulateImage := func(codespecRef model.CodespecRef) error {
			if codespecRef.Inline != nil {
				imagesToPull = append(imagesToPull, codespecRef.Inline.Image)
				return nil
			}

			if codespecRef.Named == nil {
				return nil
			}

			codeSpec := model.Codespec{}
			_, err := controlplane.InvokeRequest(ctx, http.MethodGet, fmt.Sprintf("/codespecs/%s", *codespecRef.Named), nil, nil, &codeSpec)
			if err != nil {
				return fmt.Errorf("failed to get codespec %s: %w", *codespecRef.Named, err)
			}

			imagesToPull = append(imagesToPull, codeSpec.Image)

			return nil
		}

		if err := accumulateImage(newRun.Job.Codespec); err != nil {
			return err
		}
		if newRun.Worker != nil {
			if err := accumulateImage(newRun.Worker.Codespec); err != nil {
				return err
			}
		}

		tygerClient, err := controlplane.GetClientFromCache()
		if err != nil {
			return fmt.Errorf("failed to get Tyger client: %w", err)
		}

		var dockerClientOpt []dockerclient.Opt

		if tygerClient.ConnectionType() == client.TygerConnectionTypeSsh {
			sshParams, err := client.ParseSshUrl(tygerClient.RawControlPlaneUrl)
			if err != nil {
				return fmt.Errorf("failed to parse SSH URL: %w", err)
			}

			// Capture SSH options
			defaultArgs := sshParams.FormatCmdLine()
			optionArgs := []string{
				"-o", "ConnectTimeout=none", // The default of 30s that docker adds can cause a _hang_ of 30s: https://github.com/PowerShell/Win32-OpenSSH/issues/1352
			}
			for i := 0; i < len(defaultArgs); i++ {
				if defaultArgs[i] == "-o" {
					optionArgs = append(optionArgs, defaultArgs[i], defaultArgs[i+1])
					i++
				}
			}

			// clear fields that result in a URI that docker won't underatand
			sshParams.CliPath = ""
			sshParams.SocketPath = ""
			sshParams.Options = nil

			connhelper, err := connhelper.GetConnectionHelperWithSSHOpts(sshParams.URL().String(), optionArgs)
			if err != nil {
				return fmt.Errorf("failed to get connection helper: %w", err)
			}

			httpClient := cleanhttp.DefaultClient()
			httpClient.Transport.(*http.Transport).DialContext = connhelper.Dialer

			dockerClientOpt = []dockerclient.Opt{
				dockerclient.WithAPIVersionNegotiation(),
				dockerclient.WithHost(sshParams.URL().String()),
				dockerclient.WithDialContext(connhelper.Dialer),
			}
		} else {
			dockerClientOpt = []dockerclient.Opt{dockerclient.WithAPIVersionNegotiation(), dockerclient.FromEnv}
		}

		dockerCli, err := dockerclient.NewClientWithOpts(dockerClientOpt...)
		if err != nil {
			return fmt.Errorf("failed to create Docker client: %w", err)
		}

		for _, imageName := range imagesToPull {
			out, err := dockerCli.ImagePull(context.Background(), imageName, dockerimage.PullOptions{})
			if err != nil {
				return fmt.Errorf("failed to pull image %s: %w", imageName, err)
			}
			defer out.Close()

			decoder := json.NewDecoder(out)
			for {
				var progress dockerPullProgress
				if err := decoder.Decode(&progress); err == io.EOF {
					break
				} else if err != nil {
					return fmt.Errorf("failed to decode image pull progress: %w", err)
				}

				if progress.Status != "" {
					logger := log.Ctx(ctx).With().Str("image", imageName).Logger()
					detail := ""
					if progress.ProgressDetail != nil && progress.ProgressDetail.Total > 0 {
						detail = fmt.Sprintf(" (%s/%s)", humanize.IBytes(progress.ProgressDetail.Current), humanize.IBytes(progress.ProgressDetail.Total))
					}
					logger.Info().Msgf("%s%s", progress.Status, detail)
				}
			}

			log.Info().Msgf("Pulled image %s", imageName)
		}
	}

	return nil
}

type dockerPullProgress struct {
	Status         string                    `json:"status"`
	Progress       string                    `json:"progress"`
	ProgressDetail *dockerPullProgressDetail `json:"progressDetail"`
}

type dockerPullProgressDetail struct {
	Current uint64 `json:"current"`
	Total   uint64 `json:"total"`
}
