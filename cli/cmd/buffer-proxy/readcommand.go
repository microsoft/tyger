package main

import (
	"errors"
	"os"

	bufferproxy "dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/buffer-proxy"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func newReadCommand() *cobra.Command {
	outputFilePath := ""
	dop := bufferproxy.DefaultReadDop
	cmd := &cobra.Command{
		Use:   "read BUFFER_ACCESS_STRING [flags]",
		Short: "Reads the contents of a buffer",
		Long: `Reads data from a buffer using the given access string.

The access string can be a SAS URI to an Azure Storage container or the path to a file
containing a SAS URI.`,
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

			if dop < 1 {
				return errors.New("the degree of parallelism (dop) must be at least 1")
			}

			uri, err := bufferproxy.GetUriFromAccessString(args[0])
			if err != nil {
				log.Fatal().Err(err).Msg("Invalid buffer access string")
			}

			// return the buffer to the pool
			bufferproxy.Read(uri, dop, outputFile)

			return nil
		},
	}

	cmd.Flags().StringVarP(&outputFilePath, "output", "o", outputFilePath, "The file write to. If not specified, data is written to standard out.")
	cmd.Flags().IntVarP(&dop, "dop", "p", dop, "The degree of parallelism")
	return cmd
}
