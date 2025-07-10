package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/microsoft/tyger/cli/internal/dataplane"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// set during build
var version = ""

// Field name constants
const (
	fieldBlobMedianDurationMs = "medianBlobDurationMs"
	fieldAvgThroughput        = "avgThroughput"
	fieldDop                  = "dop"
	fieldBlockSize            = "blockSize"
	fieldRegion               = "region"
	fieldIteration            = "iteration"
)

// All field names in a slice
var fieldsOrder = []string{
	fieldRegion,
	fieldBlockSize,
	fieldDop,
	fieldIteration,
	fieldAvgThroughput,
	fieldBlobMedianDurationMs,
}

var (
	interationCount   = 1
	iterationDuration = 10 * time.Second
	locationOverrides = []string{}

	blockSizes = []int64{
		128 * 1024,
		256 * 1024,
		512 * 1024,
		1 * 1024 * 1024,
		2 * 1024 * 1024,
		4 * 1024 * 1024,
		8 * 1024 * 1024,
		16 * 1024 * 1024,
		32 * 1024 * 1024,
	}

	dops = []int{
		1, 2, 4, 8, 12, 16, 24, 32, 48, 64, 96, 128, 192, 256, 384, 512, 768, 1024, 1536, 2048,
	}
)

func main() {
	rootCommand := cobra.Command{
		Use:          "buffer-benchmark",
		Short:        "Benchmark Tyger buffer throughput",
		Version:      version,
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			ctx := cmd.Context()
			logSink := zerolog.ConsoleWriter{
				Out:         os.Stderr,
				TimeFormat:  "2006-01-02T15:04:05",
				NoColor:     !isStdErrTerminal(),
				FieldsOrder: fieldsOrder,
			}

			log.Logger = log.Output(logSink).Level(zerolog.InfoLevel)
			zerolog.DefaultContextLogger = &log.Logger
			ctx = log.Logger.WithContext(ctx)

			locations := locationOverrides
			if len(locations) == 0 {
				locations = getUniqueLocations(ctx)
			}

			for _, region := range locations {
				ctx = log.Ctx(ctx).With().Str(fieldRegion, region).Logger().WithContext(ctx)

				for _, blockSize := range blockSizes {
					highestThoughtput := uint64(0)
					for _, dop := range dops {
						currentThoughtput := runThroughputBenchmark(ctx, region, dop, blockSize)

						if currentThoughtput <= highestThoughtput {
							break
						}

						highestThoughtput = currentThoughtput
					}
				}
			}
		},
	}

	rootCommand.Flags().IntVar(&interationCount, "iterations", interationCount, "Number of iterations to run for each benchmark")
	rootCommand.Flags().IntSliceVar(&dops, "dop", dops, "Degree of parallelism to use for the benchmark")
	rootCommand.Flags().StringSliceVar(&locationOverrides, "location", locationOverrides, "Override the locations to benchmark. If not specified, all available locations will be used.")

	rootCommand.AddCommand(newSortCommand())

	err := rootCommand.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func newSortCommand() *cobra.Command {
	fileName := ""
	cmd := &cobra.Command{
		Use:   "sort",
		Short: "Sorts the benchmark results from a log file",
		Run: func(cmd *cobra.Command, args []string) {
			if fileName == "" {
				log.Fatal().Msg("Please specify a log file using --file")
			}

			results, err := parseConsoleFormattedLogFile(fileName)
			if err != nil {
				log.Fatal().Err(err).Msg("Failed to parse log file")
			}

			filteredResults := make([]map[string]string, 0, len(results))
			for _, res := range results {
				if res[fieldAvgThroughput] == "" {
					continue
				}
				filteredResults = append(filteredResults, res)
			}

			slices.SortFunc(filteredResults, func(a, b map[string]string) int {
				aThoughputString := strings.TrimSuffix(a[fieldAvgThroughput], "ps")
				bThoughputString := strings.TrimSuffix(b[fieldAvgThroughput], "ps")
				aThroughput, err := humanize.ParseBytes(aThoughputString)
				if err != nil {
					log.Fatal().Err(err).Msgf("Failed to parse throughput string: %s", aThoughputString)
				}
				bThroughput, err := humanize.ParseBytes(bThoughputString)
				if err != nil {
					log.Fatal().Err(err).Msgf("Failed to parse throughput string: %s", bThoughputString)
				}

				switch {
				case aThroughput < bThroughput:
					return 1
				case aThroughput > bThroughput:
					return -1
				default:
					return 0
				}
			})

			for _, res := range filteredResults {
				for _, field := range fieldsOrder {
					if value, ok := res[field]; ok {
						fmt.Printf("%s=%s ", field, value)
					}
				}
				fmt.Println()
			}
		},
	}
	cmd.Flags().StringVarP(&fileName, "file", "f", "", "Path to the log file containing benchmark results")
	cmd.MarkFlagRequired("file")
	return cmd
}

func getUniqueLocations(ctx context.Context) []string {
	storageAccounts := []model.StorageAccount{}
	if _, err := controlplane.InvokeRequest(ctx, http.MethodGet, "/buffers/storage-accounts", nil, nil, &storageAccounts); err != nil {
		log.Fatal().Err(err).Msg("Failed to get storage accounts")
	}

	if len(storageAccounts) == 0 {
		log.Fatal().Msg("No storage accounts found")
	}

	locations := make([]string, 0, len(storageAccounts))
	seen := make(map[string]bool)
	for _, sa := range storageAccounts {
		if !seen[sa.Location] {
			seen[sa.Location] = true
			locations = append(locations, sa.Location)
		}
	}

	return locations
}

func runThroughputBenchmark(ctx context.Context, region string, dop int, blockSize int64) uint64 {
	maxBitsPerSecond := uint64(0)
	for i := range interationCount {
		if interationCount > 1 {
			ctx = log.Ctx(ctx).With().Int(fieldIteration, i).Logger().WithContext(ctx)
		}
		bufferId := createBuffer(region)
		accessUrl := getBufferAccessUrl(bufferId)

		syntheticReader := NewSyntheticDataReader(iterationDuration)

		errorBuf := SyncBuffer{}
		dataPlaneCtx := zerolog.New(&errorBuf).Level(zerolog.TraceLevel).WithContext(context.Background())
		dataplane.Write(dataPlaneCtx, dataplane.NewContainer(accessUrl), syntheticReader, dataplane.WithWriteDop(dop), dataplane.WithWriteBlockSize(int(blockSize)))

		// Parse the JSON lines from the error buffer
		jsonLines := parseJSONLines(&errorBuf)
		singleBlobDurations := []time.Duration{}
		for _, entry := range jsonLines {
			if entry["message"] == "Uploaded blob" && int64(entry["contentLength"].(float64)) == blockSize {
				duration := time.Duration(entry["duration"].(float64) * float64(zerolog.DurationFieldUnit))
				singleBlobDurations = append(singleBlobDurations, duration)
			}
		}

		// Calculate and present statistics
		stats := calculateDurationStats(singleBlobDurations)
		throughputString := getAvgThroughputFromLogs(jsonLines)
		log.Ctx(ctx).Info().
			Int64(fieldBlobMedianDurationMs, stats.Median.Milliseconds()).
			Str(fieldAvgThroughput, throughputString).
			Int(fieldDop, dop).
			Str(fieldBlockSize, strings.ReplaceAll(strings.ReplaceAll(humanize.IBytes(uint64(blockSize)), " ", ""), ".0 ", "")).
			Send()

		bitsPerSecond, err := humanize.ParseBytes(strings.TrimSuffix(throughputString, "ps"))
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to parse throughput string")
		}
		if bitsPerSecond > maxBitsPerSecond {
			maxBitsPerSecond = bitsPerSecond
		}
	}

	return maxBitsPerSecond
}

func getAvgThroughputFromLogs(logLines []map[string]any) string {
	for _, entry := range logLines {
		if entry["message"] == "Transfer complete" {
			return entry["avgThroughput"].(string)
		}
	}

	log.Fatal().Msg("No transfer complete log entry found")
	panic("unreachable code")
}

func createBuffer(region string) string {
	return runCommandOrFail("tyger", "buffer", "create", "--location", region)
}

func getBufferAccessUrl(bufferId string) *url.URL {
	urlString := runCommandOrFail("tyger", "buffer", "access", "--write", bufferId)
	parsedUrl, err := url.Parse(urlString)
	if err != nil {
		log.Fatal().Err(err).Msgf("Failed to parse URL: %s", urlString)
	}
	return parsedUrl
}

func runCommandOrFail(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		var stdErr string
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.Stderr != nil {
			stdErr = string(exitErr.Stderr)
		}
		log.Fatal().Err(err).Msgf("failed to run `%s`: %s", cmd.String(), stdErr)
	}

	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}

	return string(out)
}

func parseJSONLines(buf io.Reader) []map[string]any {
	var result []map[string]any

	scanner := bufio.NewScanner(buf)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue // Skip empty lines
		}

		var jsonObj map[string]any
		if err := json.Unmarshal([]byte(line), &jsonObj); err != nil {
			log.Warn().Err(err).Str("line", line).Msg("Failed to parse JSON line")
			continue
		}

		result = append(result, jsonObj)
	}

	if err := scanner.Err(); err != nil {
		log.Warn().Err(err).Msg("Error reading from buffer")
	}

	return result
}

// DurationStats holds statistical information about a slice of durations
type DurationStats struct {
	Min     time.Duration
	Max     time.Duration
	Median  time.Duration
	Average time.Duration
	Count   int
}

// calculateDurationStats computes min, max, median, and average for a slice of durations
func calculateDurationStats(durations []time.Duration) DurationStats {
	if len(durations) == 0 {
		return DurationStats{}
	}

	// Create a copy to avoid modifying the original slice
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	slices.Sort(sorted)

	stats := DurationStats{
		Min:   sorted[0],
		Max:   sorted[len(sorted)-1],
		Count: len(durations),
	}

	// Calculate median
	if len(sorted)%2 == 0 {
		// Even number of elements - average of two middle values
		mid1 := sorted[len(sorted)/2-1]
		mid2 := sorted[len(sorted)/2]
		stats.Median = (mid1 + mid2) / 2
	} else {
		// Odd number of elements - middle value
		stats.Median = sorted[len(sorted)/2]
	}

	// Calculate average
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	stats.Average = total / time.Duration(len(durations))

	return stats
}

// TimedDataReader produces synthetic data (zeros) for a configured duration
type TimedDataReader struct {
	startTime time.Time
	duration  time.Duration
}

// NewSyntheticDataReader creates a new reader that produces zeros for the specified duration
func NewSyntheticDataReader(duration time.Duration) *TimedDataReader {
	return &TimedDataReader{
		startTime: time.Now(),
		duration:  duration,
	}
}

// Read implements io.Reader interface
func (r *TimedDataReader) Read(p []byte) (n int, err error) {
	// Check if duration has elapsed
	if time.Since(r.startTime) >= r.duration {
		return 0, io.EOF
	}

	// Return the length of the buffer - it's already zeros by default
	return len(p), nil
}

type SyncBuffer struct {
	buffer bytes.Buffer
	mutex  sync.Mutex
}

func (s *SyncBuffer) Write(p []byte) (n int, err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.buffer.Write(p)
}

func (s *SyncBuffer) String() string {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.buffer.String()
}

func (s *SyncBuffer) Read(p []byte) (n int, err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.buffer.Read(p)
}

func isStdErrTerminal() bool {
	o, _ := os.Stderr.Stat()
	return (o.Mode() & os.ModeCharDevice) == os.ModeCharDevice
}

func parseConsoleFormattedLogLine(line string) map[string]string {
	result := make(map[string]string)

	// Find the start of key-value pairs (after timestamp and INF)
	parts := strings.Fields(line)
	if len(parts) < 3 {
		return result
	}

	// Skip timestamp and "INF" parts
	kvParts := parts[2:]

	for _, part := range kvParts {
		// Find the first '=' to split key from value
		eqIndex := strings.Index(part, "=")
		if eqIndex == -1 {
			continue
		}

		key := part[:eqIndex]
		value := part[eqIndex+1:]

		// Remove surrounding quotes if present
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}

		result[key] = value
	}

	return result
}

func parseConsoleFormattedLogFile(filename string) ([]map[string]string, error) {
	bytes, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(bytes), "\n")
	var results []map[string]string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parsed := parseConsoleFormattedLogLine(line)
		if len(parsed) > 0 {
			results = append(results, parsed)
		}
	}

	return results, nil
}
