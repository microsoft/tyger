package main

import (
	"errors"
	"os"

	bufferproxy "dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/buffer-proxy"
	"github.com/alecthomas/units"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func newWriteCommand() *cobra.Command {
	intputFilePath := ""
	dop := bufferproxy.DefaultWriteDop
	blockSizeString := ""

	cmd := &cobra.Command{
		Use:   "write BUFFER_ACCESS_STRING [flags]",
		Short: "Writes to a buffer",
		Long: `Write data to a buffer using the given access string.

The access string can be a SAS URI to an Azure Storage container or the path to a file
containing a SAS URI.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			uri, err := bufferproxy.GetUriFromAccessString(args[0])
			if err != nil {
				log.Fatal().Err(err).Msg("Invalid buffer access string")
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
