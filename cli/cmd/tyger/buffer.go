package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/alecthomas/units"
	"github.com/rs/zerolog/log"

	bufferproxy "dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/buffer-proxy"
	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/cmdline"
	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/tyger"
	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/tyger/model"
	"github.com/spf13/cobra"
)

func newBufferCommand() *cobra.Command {
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
	cmd.AddCommand(newReadCommand())
	cmd.AddCommand(newWriteCommand())

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
			_, err := tyger.InvokeRequest(http.MethodPost, "v1/buffers", nil, &bufferResponse)
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
			uri, err := getBufferAccessUri(args[0], flags.writeable)
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

func getBufferAccessUri(bufferId string, writable bool) (string, error) {
	bufferAccess := model.BufferAccess{}
	uri := fmt.Sprintf("v1/buffers/%s/access?writeable=%t", bufferId, writable)
	_, err := tyger.InvokeRequest(http.MethodPost, uri, nil, &bufferAccess)

	return bufferAccess.Uri, err
}

func newReadCommand() *cobra.Command {
	outputFilePath := ""
	dop := bufferproxy.DefaultReadDop
	cmd := &cobra.Command{
		Use:                   "read BUFFER_ID [flags]",
		Short:                 "Reads the contents of a buffer",
		Long:                  `Reads the contents of a buffer.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmdline.WarnIfRunningInPowerShell()

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

			if dop < 1 {
				return errors.New("the degree of parallelism (dop) must be at least 1")
			}

			uri, err := getBufferAccessUri(args[0], false)
			if err != nil {
				log.Fatal().Err(err).Msg("Unable to get read access to buffer")
			}

			bufferproxy.Read(uri, dop, outputFile)

			return nil
		},
	}

	cmd.Flags().StringVarP(&outputFilePath, "output", "o", outputFilePath, "The file write to. If not specified, data is written to standard out.")
	cmd.Flags().IntVarP(&dop, "dop", "p", dop, "The degree of parallelism")
	return cmd
}

func newWriteCommand() *cobra.Command {
	intputFilePath := ""
	dop := bufferproxy.DefaultWriteDop
	blockSizeString := ""

	cmd := &cobra.Command{
		Use:                   "write BUFFER_ID [flags]",
		Short:                 "Writes to a buffer",
		Long:                  `Write data to a buffer.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmdline.WarnIfRunningInPowerShell()
			var inputFile *os.File
			if intputFilePath != "" {
				var err error
				inputFile, err = os.Open(intputFilePath)
				if err != nil {
					log.Fatal().Err(err).Msg("Unable to open input file for reading")
				}
				defer inputFile.Close()
			} else {
				inputFile = os.Stdin
			}

			if dop < 1 {
				return errors.New("the degree of parallelism (dop) must be at least 1")
			}

			blockSize := bufferproxy.DefaultBlockSize

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

			uri, err := getBufferAccessUri(args[0], true)
			if err != nil {
				log.Fatal().Err(err).Msg("Unable to get write access to the buffer")
			}

			bufferproxy.Write(uri, dop, blockSize, inputFile)
			return nil
		},
	}

	cmd.Flags().StringVarP(&intputFilePath, "input", "i", intputFilePath, "The file to read from. If not specified, data is read from standard in.")
	cmd.Flags().IntVarP(&dop, "dop", "p", dop, "The degree of parallelism")
	cmd.Flags().StringVarP(&blockSizeString, "block-size", "b", blockSizeString, "Split the stream into blocks of this size.")
	return cmd
}
