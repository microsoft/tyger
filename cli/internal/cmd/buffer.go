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

	"github.com/alecthomas/units"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/microsoft/tyger/cli/internal/dataplane"
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
	cmd.AddCommand(NewBufferReadCommand(os.OpenFile))
	cmd.AddCommand(NewBufferWriteCommand(os.OpenFile))
	cmd.AddCommand(newGenerateCommand())
	cmd.AddCommand(newBufferShowCommand())
	cmd.AddCommand(newBufferSetCommand())
	cmd.AddCommand(newBufferListCommand())

	return cmd
}

func newBufferCreateCommand() *cobra.Command {
	full := false
	tagEntries := make(map[string]string)
	cmd := &cobra.Command{
		Use:                   "create [--tag key=value ...]",
		Short:                 "Create a buffer",
		Long:                  `Create a buffer. Writes the buffer ID to stdout on success.`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			newBuffer := model.Buffer{Tags: tagEntries}
			buffer := model.Buffer{}
			_, err := controlplane.InvokeRequest(cmd.Context(), http.MethodPost, "v1/buffers", newBuffer, &buffer)
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
	cmd.Flags().StringToStringVar(&tagEntries, "tag", nil, "add a key-value tag to the buffer. Can be specified multiple times.")
	cmd.Flags().BoolVar(&full, "full-resource", false, "return the full buffer resource and not just the buffer ID")

	return cmd
}

type Tags map[string]string

func (t Tags) MarshalJSON() ([]byte, error) {
	if len(t) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(map[string]string(t))
}

func newBufferSetCommand() *cobra.Command {
	var etag string
	tagEntries := make(map[string]string)
	cmd := &cobra.Command{
		Use:                   "set ID [--etag ETAG] [--tag key=value ...]",
		Short:                 "Sets replaces the tags set on a buffer",
		Long:                  `Sets replaces the tags set on a buffer`,
		Args:                  exactlyOneArg("buffer ID"),
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			buffer := model.Buffer{}
			var headers = make(http.Header)
			if etag != "" {
				headers.Add("If-Match", etag)
			}

			_, err := controlplane.InvokeRequestWithHeaders(cmd.Context(), http.MethodPut, fmt.Sprintf("v1/buffers/%s/tags", args[0]), Tags(tagEntries), &buffer, headers)
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
	cmd.Flags().StringVar(&etag, "etag", etag, "the ETag read ETag to guard against concurrent updates, ")
	cmd.Flags().StringToStringVar(&tagEntries, "tag", nil, "add a key-value tag to the buffer. Can be specified multiple times.")

	return cmd
}

func newBufferShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "show BUFFER_ID",
		Short:                 "Show the details of a buffer",
		Long:                  `Show the details of a buffer`,
		DisableFlagsInUseLine: true,
		Args:                  exactlyOneArg("buffer ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			buffer := model.Buffer{}
			_, err := controlplane.InvokeRequest(cmd.Context(), http.MethodGet, fmt.Sprintf("v1/buffers/%s", args[0]), nil, &buffer)
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

func getBufferAccessUri(ctx context.Context, bufferId string, writable bool) (string, error) {
	bufferAccess := model.BufferAccess{}
	uri := fmt.Sprintf("v1/buffers/%s/access?writeable=%t", bufferId, writable)
	_, err := controlplane.InvokeRequest(ctx, http.MethodPost, uri, nil, &bufferAccess)

	return bufferAccess.Uri, err
}

func NewBufferReadCommand(openFileFunc func(name string, flag int, perm fs.FileMode) (*os.File, error)) *cobra.Command {
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

			var outputFile *os.File
			if outputFilePath != "" {
				var err error
				outputFile, err = openFileFunc(outputFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
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

			ctx, stopFunc := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)

			go func() {
				<-ctx.Done()
				stopFunc()
				log.Warn().Msg("Canceling...")
			}()

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

func NewBufferWriteCommand(openFileFunc func(name string, flag int, perm fs.FileMode) (*os.File, error)) *cobra.Command {
	intputFilePath := ""
	dop := dataplane.DefaultWriteDop
	blockSizeString := ""

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

			var inputReader io.Reader
			if intputFilePath != "" {
				inputFile, err := openFileFunc(intputFilePath, os.O_RDONLY, 0)
				if err != nil {
					if err == context.Canceled {
						log.Warn().Msg("OpenFile operation canceled. Will write an empty payload to the buffer.")
						inputReader = bytes.NewReader([]byte{})
					} else {
						log.Fatal().Err(err).Msg("Unable to open input file for reading")
					}
				} else {
					defer inputFile.Close()
					inputReader = inputFile
				}
			} else {
				inputReader = os.Stdin
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			go func() {
				<-ctx.Done()
				stop()
				log.Warn().Msg("Canceling...")
			}()

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

			err = dataplane.Write(ctx, uri, inputReader, writeOptions...)
			if err != nil {
				if errors.Is(err, ctx.Err()) {
					err = ctx.Err()
				}
				log.Fatal().Err(err).Msg("Failed to write buffer")
			}
		},
	}

	cmd.Flags().StringVarP(&intputFilePath, "input", "i", intputFilePath, "The file to read from. If not specified, data is read from standard in.")
	cmd.Flags().IntVarP(&dop, "dop", "p", dop, "The degree of parallelism")
	cmd.Flags().StringVarP(&blockSizeString, "block-size", "b", blockSizeString, "Split the stream into blocks of this size.")
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

			return Gen(remainingBytes, outputFile)
		},
	}

	cmd.Flags().StringVarP(&outputFilePath, "output", "o", outputFilePath, "The file write to. If not specified, data is written to standard out.")
	return cmd
}

func Gen(byteCount int64, outputWriter io.Writer) error {
	diff := int('~') - int('!')
	buf := make([]byte, 300*diff)
	for i := range buf {
		buf[i] = byte('!' + i%diff)
	}

	for byteCount > 0 {
		var count int64
		if byteCount > int64(len(buf)) {
			count = int64(len(buf))
		} else {
			count = byteCount
		}

		_, err := outputWriter.Write(buf[:count])
		if err != nil {
			return err
		}

		byteCount -= count
	}
	return nil
}

func newBufferListCommand() *cobra.Command {
	limit := 0
	tagEntries := make(map[string]string)

	cmd := &cobra.Command{
		Use:                   "list [--tag key=value ...] [--limit COUNT]",
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

			for name, value := range tagEntries {
				listOptions.Add(fmt.Sprintf("tag.%s", name), value)
			}

			relativeUri := fmt.Sprintf("v1/buffers?%s", listOptions.Encode())
			return controlplane.InvokePageRequests[model.Buffer](cmd.Context(), relativeUri, limit, !cmd.Flags().Lookup("limit").Changed)
		},
	}

	cmd.Flags().StringToStringVar(&tagEntries, "tag", nil, "add a key-value tag to the buffer. Can be specified multiple times.")
	cmd.Flags().IntVarP(&limit, "limit", "l", 1000, "The maximum number of buffers to list. Default 1000")

	return cmd
}
