// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/alecthomas/units"
	"github.com/erikgeiser/promptkit"
	"github.com/erikgeiser/promptkit/confirmation"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/microsoft/tyger/cli/internal/dataplane"
	"github.com/microsoft/tyger/cli/internal/logging"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func NewBufferCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "buffer",
		Aliases:               []string{"buffers"},
		Short:                 "Manage buffers",
		Long:                  `Manage buffers.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	cmd.AddCommand(newBufferCreateCommand())
	cmd.AddCommand(newBufferAccessCommand())
	cmd.AddCommand(NewBufferReadCommand(func(ctx context.Context, name string, flag int, perm fs.FileMode) (*os.File, error) {
		return os.OpenFile(name, flag, perm)
	}))
	cmd.AddCommand(NewBufferWriteCommand(func(ctx context.Context, name string, flag int, perm fs.FileMode) (*os.File, error) {
		return os.OpenFile(name, flag, perm)
	}))
	cmd.AddCommand(newGenerateCommand())
	cmd.AddCommand(newBufferShowCommand())
	cmd.AddCommand(newBufferSetCommand())
	cmd.AddCommand(newBufferListCommand())
	cmd.AddCommand(newStorageAccountCommand())
	cmd.AddCommand(newBufferExportCommand())
	cmd.AddCommand(newBufferImportCommand())
	cmd.AddCommand(newBufferDeleteCommand())
	cmd.AddCommand(newBufferRestoreCommand())
	cmd.AddCommand(newBufferPurgeCommand())

	return cmd
}

func newBufferCreateCommand() *cobra.Command {
	full := false
	tagEntries := make(map[string]string)
	location := ""
	var ttl string
	cmd := &cobra.Command{
		Use:                   "create [--location LOCATION] [--tag KEY=VALUE ...] [--ttl D.HH:MM:SS]",
		Short:                 "Create a buffer",
		Long:                  `Create a buffer. Writes the buffer ID to stdout on success.`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			newBuffer := model.Buffer{Tags: tagEntries, Location: location}
			options := url.Values{}
			if ttl != "" {
				options.Add("ttl", ttl)
			}

			relativeUri := fmt.Sprintf("v1/buffers?%s", options.Encode())
			buffer := model.Buffer{}
			_, err := controlplane.InvokeRequest(cmd.Context(), http.MethodPost, relativeUri, newBuffer, &buffer)
			if err != nil {
				return err
			}

			if full {
				formattedBuffer, err := json.MarshalIndent(buffer, "", "  ")
				if err != nil {
					return err
				}

				fmt.Println(string(formattedBuffer))
			} else {
				fmt.Println(string(buffer.Id))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&location, "location", location, "the location of the buffer. If not specified, the buffer is created in the default location.")
	cmd.Flags().StringToStringVar(&tagEntries, "tag", nil, "add a key-value tag to the buffer. Can be specified multiple times.")
	cmd.Flags().StringVar(&ttl, "ttl", ttl, "the time-to-live for the buffer. This adjusts the buffer's expiry time for deletion.")
	cmd.Flags().BoolVar(&full, "full-resource", false, "return the full buffer resource and not just the buffer ID")

	return cmd
}

func newBufferSetCommand() *cobra.Command {
	var etag string
	tags := make(map[string]string)
	clearTags := false
	var ttl string
	softDeleted := false

	cmd := &cobra.Command{
		Use:                   "set ID [--clear-tags] [--tag key=value ...] [--etag ETAG] [--ttl D.HH:MM:SS]",
		Short:                 "Updates or replaces tags set on a buffer",
		Long:                  "Updates or replaces tags set on a buffer",
		Args:                  exactlyOneArg("buffer ID"),
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			options := url.Values{}
			if ttl != "" {
				options.Add("ttl", ttl)
			}
			if softDeleted {
				options.Add("softDeleted", strconv.FormatBool(softDeleted))
			}
			relativeUri := fmt.Sprintf("v1/buffers/%s?%s", args[0], options.Encode())
			buffer := model.Buffer{}
			return controlplane.SetTagsOnEntity(cmd.Context(), relativeUri, etag, clearTags, tags, &buffer)
		},
	}

	cmd.Flags().BoolVar(&clearTags, "clear-tags", clearTags, "clear all existing tags from the buffer and replace them with the new tags. If not specified, the existing tags are preserved and updated.")
	cmd.Flags().StringToStringVar(&tags, "tag", nil, "add or update a key-value tag to the buffer. Can be specified multiple times.")
	cmd.Flags().StringVar(&etag, "etag", etag, "the ETag read ETag to guard against concurrent updates, ")
	cmd.Flags().StringVar(&ttl, "ttl", ttl, "the time-to-live for the buffer. This adjusts the buffer's expiry time for deletion.")
	cmd.Flags().BoolVar(&softDeleted, "soft-deleted", softDeleted, "Only include soft-deleted buffers.")

	return cmd
}

func newBufferShowCommand() *cobra.Command {
	softDeleted := false

	cmd := &cobra.Command{
		Use:                   "show BUFFER_ID",
		Short:                 "Show the details of a buffer",
		Long:                  `Show the details of a buffer`,
		DisableFlagsInUseLine: true,
		Args:                  exactlyOneArg("buffer ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			listOptions := url.Values{}
			if softDeleted {
				listOptions.Add("softDeleted", strconv.FormatBool(softDeleted))
			}

			buffer := model.Buffer{}
			relativeUri := fmt.Sprintf("v1/buffers/%s?%s", args[0], listOptions.Encode())
			_, err := controlplane.InvokeRequest(cmd.Context(), http.MethodGet, relativeUri, nil, &buffer)
			if err != nil {
				return err
			}

			formattedBuffer, err := json.MarshalIndent(buffer, "", "  ")
			if err != nil {
				return err
			}

			fmt.Println(string(formattedBuffer))
			return nil
		},
	}

	cmd.Flags().BoolVar(&softDeleted, "soft-deleted", softDeleted, "Only include soft-deleted buffers.")

	return cmd
}

func newBufferAccessCommand() *cobra.Command {
	var flags struct {
		writeable bool
	}

	cmd := &cobra.Command{
		Use:                   "access BUFFER_ID [--write]",
		Short:                 "Get a URI to be able to read or write to a buffer",
		Long:                  `Get a URI to be able to read or write to a buffer`,
		DisableFlagsInUseLine: true,
		Args:                  exactlyOneArg("buffer ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			uri, err := getBufferAccessUri(cmd.Context(), args[0], flags.writeable)
			if err != nil {
				return err
			}

			fmt.Println(uri)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&flags.writeable, "write", "w", false, "request write access instead of read-only access to the buffer.")

	return cmd
}

func getBufferAccessUri(ctx context.Context, bufferId string, writable bool) (*url.URL, error) {
	bufferAccess := model.BufferAccess{}

	queryOptions := url.Values{}
	queryOptions.Add("writeable", strconv.FormatBool(writable))

	tygerClient, err := controlplane.GetClientFromCache()
	if err == nil {
		// We're ignoring the error here and will let InvokeRequest handle it
		switch tygerClient.ConnectionType() {
		case client.TygerConnectionTypeDocker:
			queryOptions.Add("preferTcp", "true")
			if os.Getenv("TYGER_ACCESSING_FROM_DOCKER") == "1" {
				queryOptions.Add("fromDocker", "true")
			}
		}
	}

	uri := fmt.Sprintf("v1/buffers/%s/access?%s", bufferId, queryOptions.Encode())
	_, err = controlplane.InvokeRequest(ctx, http.MethodPost, uri, nil, &bufferAccess)
	if err != nil {
		return nil, err
	}

	return url.Parse(bufferAccess.Uri)
}

func NewBufferReadCommand(openFileFunc func(ctx context.Context, name string, flag int, perm fs.FileMode) (*os.File, error)) *cobra.Command {
	outputFilePath := ""
	dop := dataplane.DefaultReadDop
	cmd := &cobra.Command{
		Use:                   "read { BUFFER_ID | BUFFER_SAS_URI | FILE_WITH_SAS_URI } [flags]",
		Short:                 "Reads the contents of a buffer",
		Long:                  `Reads the contents of a buffer.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			warnIfRunningInPowerShell()

			if dop < 1 {
				log.Fatal().Msg("the degree of parallelism (dop) must be at least 1")
			}

			uri, err := dataplane.GetUriFromAccessString(args[0])
			if err != nil {
				if err == dataplane.ErrAccessStringNotUri {
					uri, err = getBufferAccessUri(cmd.Context(), args[0], false)
					if err != nil {
						log.Fatal().Err(err).Msg("Unable to get read access to buffer")
					}
				} else {
					log.Fatal().Err(err).Msg("Invalid buffer access string")
				}
			}

			ctx, stopFunc := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)

			go func() {
				<-ctx.Done()
				stopFunc()
				log.Warn().Msg("Canceling...")
			}()

			var outputFile *os.File
			if outputFilePath != "" {
				var err error
				outputFile, err = openFileFunc(ctx, outputFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
				if err != nil {
					if err == context.Canceled {
						log.Warn().Msg("OpenFile operation canceled. Exiting.")
						return
					}
					log.Fatal().Err(err).Msg("Unable to open output file for writing")
				}
				defer outputFile.Close()
			} else {
				outputFile = os.Stdout
			}

			if err := dataplane.Read(ctx, uri, outputFile, dataplane.WithReadDop(dop)); err != nil {
				if errors.Is(err, ctx.Err()) {
					err = ctx.Err()
				}
				log.Fatal().Err(err).Msg("Failed to read buffer")
			}
		},
	}

	cmd.Flags().StringVarP(&outputFilePath, "output", "o", outputFilePath, "The file write to. If not specified, data is written to standard out.")
	cmd.Flags().IntVarP(&dop, "dop", "p", dop, "The degree of parallelism")
	return cmd
}

func NewBufferWriteCommand(openFileFunc func(ctx context.Context, name string, flag int, perm fs.FileMode) (*os.File, error)) *cobra.Command {
	inputFilePath := ""
	dop := dataplane.DefaultWriteDop
	blockSizeString := ""
	flushIntervalString := dataplane.DefaultFlushInterval.String()

	cmd := &cobra.Command{
		Use:                   "write { BUFFER_ID | BUFFER_SAS_URI | FILE_WITH_SAS_URI } [flags]",
		Short:                 "Writes to a buffer",
		Long:                  `Write data to a buffer.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			warnIfRunningInPowerShell()

			if dop < 1 {
				log.Fatal().Msg("the degree of parallelism (dop) must be at least 1")
			}

			uri, err := dataplane.GetUriFromAccessString(args[0])
			if err != nil {
				if err == dataplane.ErrAccessStringNotUri {
					uri, err = getBufferAccessUri(cmd.Context(), args[0], true)
					if err != nil {
						log.Fatal().Err(err).Msg("Unable to get read access to buffer")
					}
				} else {
					log.Fatal().Err(err).Msg("Invalid buffer access string")
				}
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			go func() {
				<-ctx.Done()
				stop()
				log.Warn().Msg("Canceling...")
			}()

			var inputReader io.Reader
			if inputFilePath != "" {
				inputFile, err := openFileFunc(ctx, inputFilePath, os.O_RDONLY, 0)
				if err != nil {
					if err == context.Canceled {
						log.Warn().Msg("OpenFile operation canceled. Will write an empty payload to the buffer.")
						inputReader = bytes.NewReader([]byte{})
						cancelCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
						defer cancel()
						ctx = cancelCtx
					} else {
						log.Fatal().Err(err).Msg("Unable to open input file for reading")
					}
				} else {
					defer inputFile.Close()
					if fileInfo, err := inputFile.Stat(); err == nil && fileInfo.Mode().IsRegular() {
						// in input file is a regular file, so disable periodic flushing
						flushIntervalString = ""
					}

					inputReader = inputFile
				}
			} else {
				inputReader = os.Stdin
			}

			writeOptions := []dataplane.WriteOption{dataplane.WithWriteDop(dop)}
			if blockSizeString != "" {
				if blockSizeString != "" && blockSizeString[len(blockSizeString)-1] != 'B' {
					blockSizeString += "B"
				}
				parsedBlockSize, err := units.ParseBase2Bytes(blockSizeString)
				if err != nil {
					log.Fatal().Err(err).Msg("Invalid block size")
				}

				writeOptions = append(writeOptions, dataplane.WithWriteBlockSize(int(parsedBlockSize)))
			}

			var parsedFlushInterval time.Duration
			if flushIntervalString != "" {
				var err error
				parsedFlushInterval, err = time.ParseDuration(flushIntervalString)
				if err != nil {
					log.Fatal().Err(err).Msg("Invalid flush interval")
				}
			}

			writeOptions = append(writeOptions, dataplane.WithWriteFlushInterval(parsedFlushInterval))

			err = dataplane.Write(ctx, uri, inputReader, writeOptions...)
			if err != nil {
				if errors.Is(err, ctx.Err()) {
					err = ctx.Err()
				}
				log.Fatal().Err(err).Msg("Failed to write buffer")
			}
		},
	}

	cmd.Flags().StringVarP(&inputFilePath, "input", "i", inputFilePath, "The file to read from. If not specified, data is read from standard in.")
	cmd.Flags().IntVarP(&dop, "dop", "p", dop, "The degree of parallelism")
	cmd.Flags().StringVarP(&blockSizeString, "block-size", "b", blockSizeString, "Split the stream into blocks of this size.")
	cmd.Flags().StringVarP(&flushIntervalString, "flush-interval", "f", flushIntervalString, "The longest time to wait before accumulated data is written to the remote service. Data will be flushed either when --block-size of data has been accumulated or when the specified interval has elapsed, whichever comes first. This is ignored if the input is a regular file. Set to 0 to disable.")
	return cmd
}

func newGenerateCommand() *cobra.Command {
	outputFilePath := ""
	cmd := &cobra.Command{
		Use:   "gen SIZE",
		Short: "Generate deterministic data.",
		Long: `Generate SIZE bytes of arbitrary but deterministic data.
The SIZE argument must be a number with an optional unit (e.g. 10MB). 1KB and 1KiB are both treated as 1024 bytes.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var outputFile *os.File
			if outputFilePath != "" {
				var err error
				outputFile, err = os.OpenFile(outputFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
				if err != nil {
					log.Fatal().Err(err).Msg("Unable to open output file for writing")
				}
				defer outputFile.Close()
			} else {
				outputFile = os.Stdout
			}

			sizeString := args[0]
			if sizeString != "" && sizeString[len(sizeString)-1] != 'B' {
				sizeString += "B"
			}

			parsedBytes, err := units.ParseBase2Bytes(sizeString)
			if err != nil {
				return err
			}

			remainingBytes := int64(parsedBytes)

			return dataplane.Gen(remainingBytes, outputFile)
		},
	}

	cmd.Flags().StringVarP(&outputFilePath, "output", "o", outputFilePath, "The file write to. If not specified, data is written to standard out.")
	return cmd
}

func newBufferListCommand() *cobra.Command {
	limit := 0
	tagEntries := make(map[string]string)
	excludeTagEntries := make(map[string]string)
	softDeleted := false

	cmd := &cobra.Command{
		Use:                   "list [--tag key=value ...] [--exclude-tag key=value ...] [--limit COUNT] [--soft-deleted]",
		Short:                 "List buffers",
		Long:                  `List buffers. Buffers are sorted by descending created time.`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			listOptions := url.Values{}
			if limit > 0 {
				listOptions.Add("limit", strconv.Itoa(limit))
			} else {
				limit = math.MaxInt
			}

			if softDeleted {
				listOptions.Add("softDeleted", strconv.FormatBool(softDeleted))
			}

			for name, value := range tagEntries {
				listOptions.Add(fmt.Sprintf("tag[%s]", name), value)
			}

			for name, value := range excludeTagEntries {
				listOptions.Add(fmt.Sprintf("excludeTag[%s]", name), value)
			}

			relativeUri := fmt.Sprintf("v1/buffers?%s", listOptions.Encode())
			return controlplane.InvokePageRequests[model.Buffer](cmd.Context(), relativeUri, limit, !cmd.Flags().Lookup("limit").Changed)
		},
	}

	cmd.Flags().StringToStringVar(&tagEntries, "tag", nil, "Only include buffers with the given tag. Can be specified multiple times.")
	cmd.Flags().StringToStringVar(&excludeTagEntries, "exclude-tag", nil, "Exclude buffers with the given tag. Can be specified multiple times.")
	cmd.Flags().BoolVar(&softDeleted, "soft-deleted", softDeleted, "Only include soft-deleted buffers.")
	cmd.Flags().IntVarP(&limit, "limit", "l", 1000, "The maximum number of buffers to list. Default 1000")

	return cmd
}

func newStorageAccountCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "storage-account",
		Aliases:               []string{"storage-accounts"},
		Short:                 "Manage storage accounts",
		Long:                  `Manage storage accounts.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	cmd.AddCommand(newStorageAccountListCommand())

	return cmd
}

func newStorageAccountListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "list",
		Short:                 "List storage accounts",
		Long:                  `List storage accounts.`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			storageAccounts := []model.StorageAccount{}
			if _, err := controlplane.InvokeRequest(cmd.Context(), http.MethodGet, "v1/buffers/storage-accounts", nil, &storageAccounts); err != nil {
				return err
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(storageAccounts)
			return nil
		},
	}

	return cmd
}

func newBufferExportCommand() *cobra.Command {
	request := model.ExportBuffersRequest{
		Filters: make(map[string]string),
	}

	cmd := &cobra.Command{
		Use:                   "export DESTINATION_STORAGE_ENDPOINT [--source-storage-account NAME] [--tag KEY=VALUE ...]",
		Short:                 "Export buffers to a storage account belonging to another Tyger instance. Note that the Tyger server's managed identity must have the necessary permissions to write to the destination storage account. Only supported in cloud environments.",
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			request.DestinationStorageEndpoint = args[0]
			run := model.Run{}
			_, err := controlplane.InvokeRequest(cmd.Context(), http.MethodPost, "v1/buffers/export", request, &run)
			if err != nil {
				log.Fatal().Err(err).Msg("Failed to export buffers")
			}

			if err := attachToRunNoBufferIO(cmd.Context(), run, true, false, getSystemRunLogSink(cmd.Context())); err != nil {
				log.Fatal().Err(err).Msg("Failed to attach to run")
			}
		},
	}

	cmd.Flags().StringVar(&request.SourceStorageAccountName, "source-storage-account", request.SourceStorageAccountName, "The name of the storage account to use as the source. Required if more than one storage account is part of the source Tyger installation.")
	cmd.Flags().StringToStringVar(&request.Filters, "tag", nil, "Only include buffers with the given tag. Can be specified multiple times.")
	cmd.Flags().BoolVar(&request.HashIds, "hash-ids", false, "Hash the buffer IDs.")
	cmd.Flags().MarkHidden("hash-ids")

	return cmd
}

func newBufferImportCommand() *cobra.Command {
	request := model.ImportBuffersRequest{}
	cmd := &cobra.Command{
		Use:                   "import [--storage-account NAME]",
		Short:                 "Import buffers into the local Tyger instance. This command is intended to be run after `tyger buffer export` on another Tyger instance has exported to this instance's storage accounts.",
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			run := model.Run{}
			_, err := controlplane.InvokeRequest(cmd.Context(), http.MethodPost, "v1/buffers/import", request, &run)
			if err != nil {
				log.Fatal().Err(err).Msg("Failed to import buffers")
			}

			if err := attachToRunNoBufferIO(cmd.Context(), run, true, false, getSystemRunLogSink(cmd.Context())); err != nil {
				log.Fatal().Err(err).Msg("Failed to attach to run")
			}
		},
	}

	cmd.Flags().StringVar(&request.StorageAccountName, "storage-account", request.StorageAccountName, "The name of the storage account to use as the source. Required if more than one storage account is part of the Tyger installation.")

	return cmd
}

func newBufferDeleteCommand() *cobra.Command {
	deleteAll := false
	force := false
	tags := make(map[string]string)
	excludeTags := make(map[string]string)

	cmd := &cobra.Command{
		Use:                   "delete ID ... | --tag KEY=VALUE ... | --all",
		Short:                 "Delete buffers",
		Long:                  `Delete buffers (soft-delete)`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			validateExactlyOneMode := func(modes ...bool) bool {
				count := 0
				for _, mode := range modes {
					if mode {
						count++
					}
				}
				return count == 1
			}

			if !validateExactlyOneMode(len(args) > 0, len(tags) > 0, deleteAll) {
				log.Fatal().Msg("One of ID, --tag, or --all must be specified")
			}

			if len(args) > 0 {
				var deleted []model.Buffer
				for _, id := range args {
					buffer := model.Buffer{}
					relativeUri := fmt.Sprintf("v1/buffers/%s", id)
					resp, err := controlplane.InvokeRequest(cmd.Context(), http.MethodDelete, relativeUri, nil, &buffer)

					if resp.StatusCode == http.StatusNotFound {
						return fmt.Errorf("buffer with ID %s not found", id)
					} else if resp.StatusCode == http.StatusPreconditionFailed {
						return fmt.Errorf("buffer with ID %s is already deleted", id)
					}

					if err != nil {
						return err
					}

					deleted = append(deleted, buffer)
				}

				var formatted []byte
				var err error
				if len(deleted) == 1 {
					formatted, err = json.MarshalIndent(deleted[0], "", "  ")
				} else {
					formatted, err = json.MarshalIndent(deleted, "", "  ")
				}
				if err != nil {
					return err
				}
				fmt.Println(string(formatted))

			} else {
				options := url.Values{}
				for name, value := range tags {
					options.Add(fmt.Sprintf("tag[%s]", name), value)
				}
				for name, value := range excludeTags {
					options.Add(fmt.Sprintf("excludeTag[%s]", name), value)
				}

				performDeletion := force

				if !force {
					options.Add("softDeleted", strconv.FormatBool(false))
					countUri := fmt.Sprintf("v1/buffers/count?%s", options.Encode())
					count := 0
					if _, err := controlplane.InvokeRequest(cmd.Context(), http.MethodGet, countUri, nil, &count); err != nil {
						return err
					}
					if count > 0 {
						input := confirmation.New(fmt.Sprintf("Are you sure you want to delete %d buffers?", count), confirmation.Yes)
						input.WrapMode = promptkit.WordWrap
						confirmed, err := input.RunPrompt()
						if err != nil {
							return err
						}
						if confirmed {
							performDeletion = true
						}
					} else {
						fmt.Println("No buffers found.")
						return nil
					}
				}

				if performDeletion {
					relativeUri := fmt.Sprintf("v1/buffers?%s", options.Encode())
					count := 0
					if _, err := controlplane.InvokeRequest(cmd.Context(), http.MethodDelete, relativeUri, nil, &count); err != nil {
						return err
					}
					fmt.Printf("Deleted %d buffers.\n", count)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&deleteAll, "all", deleteAll, "Delete all buffers.")
	cmd.Flags().BoolVar(&force, "force", force, "Skip confirmation before deleting multiple buffers.")
	cmd.Flags().StringToStringVar(&tags, "tag", nil, "Delete all buffers matching tag. Can be specified multiple times.")
	cmd.Flags().StringToStringVar(&excludeTags, "exclude-tag", nil, "Exclude buffers with the given tag. Can be specified multiple times.")

	return cmd
}

func newBufferRestoreCommand() *cobra.Command {
	restoreAll := false
	force := false
	tags := make(map[string]string)
	excludeTags := make(map[string]string)

	cmd := &cobra.Command{
		Use:                   "restore ID ... | --tag KEY=VALUE ... | --all",
		Short:                 "Restore deleted buffers",
		Long:                  `Restore deleted buffers`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			validateExactlyOneMode := func(modes ...bool) bool {
				count := 0
				for _, mode := range modes {
					if mode {
						count++
					}
				}
				return count == 1
			}

			if !validateExactlyOneMode(len(args) > 0, len(tags) > 0, restoreAll) {
				log.Fatal().Msg("One of ID, --tag, or --all must be specified")
			}

			if len(args) > 0 {
				for _, id := range args {
					buffer := model.Buffer{}
					relativeUri := fmt.Sprintf("v1/buffers/%s/restore", id)
					resp, err := controlplane.InvokeRequest(cmd.Context(), http.MethodPost, relativeUri, nil, &buffer)

					if resp.StatusCode == http.StatusNotFound {
						return fmt.Errorf("buffer with ID %s not found", id)
					} else if resp.StatusCode == http.StatusPreconditionFailed {
						return fmt.Errorf("buffer with ID %s is not deleted", id)
					}

					if err != nil {
						return err
					}

					formattedBuffer, err := json.MarshalIndent(buffer, "", "  ")
					if err != nil {
						return err
					}

					fmt.Println(string(formattedBuffer))
				}
			} else {
				options := url.Values{}
				for name, value := range tags {
					options.Add(fmt.Sprintf("tag[%s]", name), value)
				}
				for name, value := range excludeTags {
					options.Add(fmt.Sprintf("excludeTag[%s]", name), value)
				}

				performRestore := force

				if !force {
					options.Add("softDeleted", strconv.FormatBool(true))
					countUri := fmt.Sprintf("v1/buffers/count?%s", options.Encode())
					count := 0
					if _, err := controlplane.InvokeRequest(cmd.Context(), http.MethodGet, countUri, nil, &count); err != nil {
						return err
					}
					if count > 0 {
						input := confirmation.New(fmt.Sprintf("Are you sure you want to restore %d buffers?", count), confirmation.Yes)
						input.WrapMode = promptkit.WordWrap
						confirmed, err := input.RunPrompt()
						if err != nil {
							return err
						}
						if confirmed {
							performRestore = true
						}
					} else {
						fmt.Println("No buffers found.")
						return nil
					}
				}

				if performRestore {
					relativeUri := fmt.Sprintf("v1/buffers/restore?%s", options.Encode())

					count := 0
					if _, err := controlplane.InvokeRequest(cmd.Context(), http.MethodPost, relativeUri, nil, &count); err != nil {
						return err
					}
					fmt.Printf("Restored %d buffers.\n", count)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&restoreAll, "all", restoreAll, "Restore all deleted buffers.")
	cmd.Flags().BoolVar(&force, "force", force, "Skip confirmation before restoring multiple buffers.")
	cmd.Flags().StringToStringVar(&tags, "tag", nil, "Restore all deleted buffers matching tag. Can be specified multiple times.")
	cmd.Flags().StringToStringVar(&excludeTags, "exclude-tag", nil, "Exclude buffers with the given tag. Can be specified multiple times.")

	return cmd
}

func newBufferPurgeCommand() *cobra.Command {
	purgeAll := false
	force := false
	tags := make(map[string]string)
	excludeTags := make(map[string]string)

	cmd := &cobra.Command{
		Use:                   "purge ID ... | --tag KEY=VALUE ... | --all",
		Short:                 "Purge deleted buffers (irreversible)",
		Long:                  `Purge deleted buffers (irreversible)`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			validateExactlyOneMode := func(modes ...bool) bool {
				count := 0
				for _, mode := range modes {
					if mode {
						count++
					}
				}
				return count == 1
			}

			if !validateExactlyOneMode(len(args) > 0, len(tags) > 0, purgeAll) {
				log.Fatal().Msg("One of ID, --tag, or --all must be specified")
			}

			options := url.Values{}
			options.Add("ttl", "0")
			options.Add("softDeleted", strconv.FormatBool(true))

			if len(args) > 0 {
				var deleted []model.Buffer
				for _, id := range args {
					buffer := model.Buffer{}
					relativeUri := fmt.Sprintf("v1/buffers/%s?%s", id, options.Encode())
					resp, err := controlplane.InvokeRequest(cmd.Context(), http.MethodDelete, relativeUri, nil, &buffer)

					if resp.StatusCode == http.StatusNotFound {
						return fmt.Errorf("buffer with ID %s not found", id)
					} else if resp.StatusCode == http.StatusPreconditionFailed {
						return fmt.Errorf("buffer with ID %s is not deleted", id)
					}

					if err != nil {
						return err
					}

					deleted = append(deleted, buffer)
				}

				var formatted []byte
				var err error
				if len(deleted) == 1 {
					formatted, err = json.MarshalIndent(deleted[0], "", "  ")
				} else {
					formatted, err = json.MarshalIndent(deleted, "", "  ")
				}
				if err != nil {
					return err
				}
				fmt.Println(string(formatted))

			} else {
				for name, value := range tags {
					options.Add(fmt.Sprintf("tag[%s]", name), value)
				}
				for name, value := range excludeTags {
					options.Add(fmt.Sprintf("excludeTag[%s]", name), value)
				}

				performPurge := force

				if !force {
					countUri := fmt.Sprintf("v1/buffers/count?%s", options.Encode())
					count := 0
					if _, err := controlplane.InvokeRequest(cmd.Context(), http.MethodGet, countUri, nil, &count); err != nil {
						return err
					}
					if count > 0 {
						input := confirmation.New(fmt.Sprintf("Are you sure you want to purge %d buffers?", count), confirmation.Yes)
						input.WrapMode = promptkit.WordWrap
						confirmed, err := input.RunPrompt()
						if err != nil {
							return err
						}
						if confirmed {
							performPurge = true
						}
					} else {
						fmt.Println("No buffers found.")
						return nil
					}
				}

				if performPurge {
					relativeUri := fmt.Sprintf("v1/buffers?%s", options.Encode())

					count := 0
					if _, err := controlplane.InvokeRequest(cmd.Context(), http.MethodDelete, relativeUri, nil, &count); err != nil {
						return err
					}
					fmt.Printf("Purged %d buffers.\n", count)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&purgeAll, "all", purgeAll, "Purge all deleted buffers.")
	cmd.Flags().BoolVar(&force, "force", force, "Skip confirmation before purging multiple buffers.")
	cmd.Flags().StringToStringVar(&tags, "tag", nil, "Purge all deleted buffers matching tag. Can be specified multiple times.")
	cmd.Flags().StringToStringVar(&excludeTags, "exclude-tag", nil, "Exclude buffers with the given tag. Can be specified multiple times.")

	return cmd
}

// If we are using the zerolog console writer, this returns an io.Writer that
// feeds lines (that are expected to contain JSON) to the console writer, so that the output is formatted.
func getSystemRunLogSink(ctx context.Context) io.Writer {
	loggingSink := logging.GetLogSinkFromContext(ctx)
	if consoleWriter, ok := loggingSink.(zerolog.ConsoleWriter); ok {
		formatter := logging.NewZeroLogFormatter(consoleWriter)
		return formatter
	}

	return os.Stderr
}
