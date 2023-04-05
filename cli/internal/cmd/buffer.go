package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/controlplane"
	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/controlplane/model"
	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/dataplane"
	"github.com/alecthomas/units"
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

	return cmd
}

func newBufferCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "create",
		Short:                 "Create a buffer",
		Long:                  `Create a buffer. Writes the buffer ID to stdout on success.`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			bufferResponse := model.Buffer{}
			_, err := controlplane.InvokeRequest(cmd.Context(), http.MethodPost, "v1/buffers", nil, &bufferResponse)
			if err != nil {
				return err
			}

			fmt.Println(bufferResponse.Id)
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
			WarnIfRunningInPowerShell()

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

			dataplane.Read(uri, dop, outputFile)
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
			WarnIfRunningInPowerShell()

			if dop < 1 {
				log.Fatal().Msg("the degree of parallelism (dop) must be at least 1")
			}

			blockSize := dataplane.DefaultBlockSize

			if blockSizeString != "" {
				if blockSizeString != "" && blockSizeString[len(blockSizeString)-1] != 'B' {
					blockSizeString += "B"
				}
				parsedBlockSize, err := units.ParseBase2Bytes(blockSizeString)
				if err != nil {
					log.Fatal().Err(err).Msg("Invalid block size")
				}

				blockSize = int(parsedBlockSize)
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

			dataplane.Write(uri, dop, blockSize, inputReader)
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
