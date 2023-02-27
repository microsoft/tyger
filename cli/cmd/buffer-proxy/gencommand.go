package main

import (
	"os"

	bufferproxy "dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/buffer-proxy"
	"github.com/alecthomas/units"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

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

			return bufferproxy.Gen(remainingBytes, outputFile)
		},
	}

	cmd.Flags().StringVarP(&outputFilePath, "output", "o", outputFilePath, "The file write to. If not specified, data is written to standard out.")
	return cmd
}
