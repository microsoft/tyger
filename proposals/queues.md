# Proposal: Queues and Services in Tyger

## Overview

This document proposes adding **queues** and **services** to Tyger to support
long-running processing workloads, work distribution and scale-out, and
request-response patterns.

### Motivation

#### 1. Decoupling GPU-intensive tasks

Consider an MRI reconstruction pipeline: a run processes raw signal data and
produces reconstructed images. To apply an ML denoising model to
improve image quality, you could include the denoising step in the same
run, but this approach has drawbacks:

- **Setup overhead**: The ML model must be loaded into GPU memory for each run
- **Inefficient GPU utilization**: GPU resources are reserved for the entire run
  duration, even though the GPU is only needed for a small portion of the
  processing time

A better pattern is to offload the denoising to a dedicated **service**: a
long-running process that loads the model once and processes requests from many
runs. The reconstruction run submits the image to a queue, the denoising service
processes it, and the run (or another workflow step) retrieves the result. This
amortizes model loading across many requests and allows GPU resources to be used
only when needed.

#### 2. Work distribution

Queues enable distributing work across a scalable pool of workers in a reliable
way:

- Multiple service replicas can process queue items in parallel
- Failed items are automatically retried
- Submitters can wait for and retrieve results (request-response pattern)
- Work can be batched and processed at a different rate than it's submitted

This proposal introduces:

1. **Queues** - Named collections of work items with a fixed schema
2. **Services** - Named, long-running processes (with a new codespec kind)
3. **Triggers** - Automatic run creation per queue item (for simpler use cases)

## Concepts

### Queue

A queue is a named, durable collection of work items with a fixed schema. The
schema defines:

- **Input buffer slots**: Named placeholders for buffers that submitters provide
- **Output buffer slots**: Named placeholders for buffers that processors provide
  when completing an item
- **Input parameters**: Key-value pairs that submitters provide
- **Output parameters**: Key-value pairs that processors provide when completing an item

#### Queue States

A queue can be in one of three states:

- **open**: Accepting new submissions (default)
- **closed**: Not accepting new submissions; existing items continue processing
- **completed**: Closed and all items have reached a terminal state (completed or failed)

State transitions are one-directional: `open → closed → completed`. Once a
queue is closed, it cannot be reopened. The `completed` state is reached
automatically when a closed queue has no items in `pending` or `processing`
state. This is useful for batch processing scenarios where the full set of work
is known.

### Queue Item

A queue item represents a unit of work. Each item contains:

- A unique system-generated ID
- References to buffers (as defined by the queue schema)
- Key-value parameters (as defined by the queue schema)
- Lifecycle state and metadata (timestamps, retry count, etc.)

### Item Lifecycle

Queue items transition through the following states:

```
                               timeout/release
                          ┌────────────────────────┐
                          ▼                        │
                    ┌─────────┐     receive    ┌────────────┐
                    │ pending │ ──────────────►│ processing │
                    └─────────┘                └────────────┘
                                                 │        │
                                          commit │        │ fail /
                                                 ▼        │ max retries
                                           ┌───────────┐  │
                                           │ completed │  │
                                           └───────────┘  │
                                                          ▼
                                                     ┌────────┐
                                                     │ failed │
                                                     └────────┘
```

- **pending**: Item is waiting to be processed
- **processing**: Item has been received by a processor and is being worked on
- **completed**: Item has been successfully committed with response buffers
- **failed**: Item has exhausted retry attempts or was explicitly marked as failed

When an item is in `processing` state, it has a visibility timeout. If the
processor does not commit or send a heartbeat before the timeout expires, the
item returns to `pending` state for retry. After a configurable number of
retries, the item is marked as `failed`.

## Queue Schema

When creating a queue, you define the schema for items:

```bash
tyger queue create my-queue \
  --input raw_signal \
  --input calibration \
  --output reconstructed \
  --input-param priority \
  --output-param quality_score
```

This creates a queue where:
- Submitters must provide `raw_signal` and `calibration` buffer IDs
- Submitters must provide a `priority` parameter
- Processors must provide a `reconstructed` buffer ID when committing
- Processors must provide a `quality_score` parameter in the response

### Queue Configuration

Queues support the following configuration options:

| Option | Description | Default |
|--------|-------------|---------|
| `--visibility-timeout` | Time before an item being processed returns to pending | 5m |
| `--max-retries` | Maximum retry attempts before marking as failed | 3 |
| `--item-ttl` | Time-to-live for completed/failed items | 7d |

## Item Lifecycle Details

### Visibility Timeout

When a processor receives an item, the item becomes invisible to other
processors for the visibility timeout duration. The processor must either:

1. Commit the item (marking it completed)
2. Fail the item explicitly
3. Send heartbeats to extend the timeout

If none of these occur before the timeout, the item returns to `pending` state.
If the retry count has been exceeded, the item is marked as `failed` instead.

The default visibility timeout is configured per-queue but can be overridden
when receiving:

```bash
tyger queue receive my-queue --visibility-timeout 10m
```

### Output Buffer Creation

When receiving an item, the optional `--create-output-buffers` flag causes Tyger
to automatically create buffers for all output slots defined in the queue
schema. The created buffer IDs are included in the receive response. If the item
is not committed before the visibility timeout expires or is explicitly failed,
the pre-created buffers are automatically deleted. This simplifies service
implementations by removing the need to manually create and track output
buffers.

### Retries

Each time an item times out (or is explicitly released), its retry count
increments. When the retry count exceeds `max-retries`, the item is marked as
`failed` and will not be retried.

Failed items remain queryable and can be purged:

```bash
# List failed items
tyger queue items list my-queue --status failed [--limit <limit>]

# Purge failed items
tyger queue items purge my-queue --status failed
```

### Item TTL

Completed and failed items are automatically deleted after the configured TTL.
Items can also be explicitly deleted:

```bash
tyger queue item delete $item_id
```

## Processing Modes

Queues can be consumed in several ways:

### Services (Primary Mode)

A **service** is a named, long-running process that typically consumes items
from a queue. Services are ideal when:

- Setup costs would be high per-run
- You need persistent connections or state
- You want to control scaling explicitly

#### Service Codespec

Services use a new codespec kind: `service`. Unlike job codespecs, service
codespecs do not declare buffer parameters — the service interacts with queues
directly via the CLI.

```bash
tyger codespec create --kind service -f inference-service.yml
```

```yaml
name: inference-service
codespec:
  image: myregistry/inference:latest
  queues:
    - requests
  command:
    - serve.sh
    - --queue
    - $(REQUESTS_QUEUE_NAME)

replicas: 2
queues:
  requests: inference-requests
```

#### Creating a Service

A service can reference an existing codespec by name or define the codespec
inline in a YAML file (consistent with how codespecs can be specified in runs).

```bash
# Reference an existing codespec
tyger service create my-inference-service \
  --codespec inference-service \
  --replicas 2 \
  --queue requests=inference-requests

# Or define inline via YAML (codespec is created implicitly)
tyger service create my-inference-service -f service.yaml
```

This creates a named service. The `--replicas` and `--queue` parameters override
any values given in the YAML. Each replica runs the codespec's container with
the Tyger CLI mounted and pre-authenticated with access to the queues and any
buffer by ID. Other tyger commands are not allowed.

For each queue binding, Tyger sets an environment variable
`<QUEUE_KEY>_QUEUE_NAME` containing the actual queue name (e.g.
`REQUESTS_QUEUE_NAME=inference-requests`).

#### Scaling Services

```bash
# Scale up
tyger service scale my-inference-service --replicas 5

# Scale down
tyger service scale my-inference-service --replicas 1

# Scale to zero (pause)
tyger service scale my-inference-service --replicas 0
```

#### Service Lifecycle

Services can be in the following states:

- **running**: Service is active with the configured number of replicas
- **stopped**: Service has been scaled to zero replicas
- **failed**: Service has encountered an unrecoverable error and will not be restarted.

```bash
# Show service status
tyger service show my-inference-service

# List all services
tyger service list [--status running|stopped|failed]

# Restart all replicas (e.g., after updating the codespec)
tyger service restart my-inference-service

# Delete a service
tyger service delete my-inference-service
```

#### Service Processing Loop

An example service implementation in Bash:

```bash
#!/bin/bash
set -euo pipefail

queue="$REQUESTS_QUEUE_NAME"

# Load model once at startup
load_model

while true; do
  # Receive returns {"status": "...", "items": [...]}
  response=$(tyger queue receive "$queue" --create-output-buffers)
  status=$(echo "$response" | jq -r '.status')
  item_json=$(echo "$response" | jq -r '.items[0] // empty')

  if [ "$status" = "completed" ]; then
    echo "Queue completed, exiting."
    exit 0
  fi

  if [ -z "$item_json" ]; then
    sleep 1
    continue
  fi

  # Parse item details
  item_id=$(echo "$item_json" | jq -r '.id')
  lease=$(echo "$item_json" | jq -r '.lease')
  input_buffer=$(echo "$item_json" | jq -r '.inputs.data')
  output_buffer=$(echo "$item_json" | jq -r '.outputs.result')

  # Start heartbeat in background
  tyger queue item heartbeat "$item_id" --lease "$lease" --while-alive &
  heartbeat_pid=$!

  # Read input, run inference, write to the pre-created output buffer
  tyger buffer read "$input_buffer" \
    | run_inference \
    | tyger buffer write "$output_buffer"

  # Commit the result and stop heartbeat
  tyger queue item commit "$item_id" --lease "$lease"
  kill $heartbeat_pid 2>/dev/null || true
done
```

### Triggers (Auto-Run Mode)

A **trigger** automatically creates a Tyger run for each queue item. Triggers
are simpler than services but incur per-item setup costs.

#### Creating a Trigger

```bash
tyger trigger create my-trigger \
  --codespec image-processor \
  --input-queue requests \
  --input-mapping raw_signal=input \
  --output-mapping reconstructed=output \
  --concurrency 10
```

This creates a trigger that:
- Watches the `requests` queue
- For each item, creates a run with the `image-processor` codespec
- Maps the queue's `raw_signal` input slot to the codespec's `input` buffer parameter
- Commits the run's `output` buffer as the queue's `reconstructed` output slot
- Allows up to 10 concurrent runs (additional items remain pending until a run completes)

#### Scaling Triggers

The concurrency limit can be adjusted:

```bash
# Increase concurrency
tyger trigger scale my-trigger --concurrency 20

# Pause processing (no new runs created)
tyger trigger scale my-trigger --concurrency 0
```

#### Buffer Mapping

Triggers define how queue slots map to codespec buffer parameters:

- **Input mapping** (`--input-mapping`): Maps queue input slots to codespec input buffer parameters
- **Output mapping** (`--output-mapping`): Maps codespec output buffer parameters to queue output slots

By default, slots and parameters with matching names are mapped automatically.
Explicit mappings override this behavior.

#### Output Queues

Triggers can enqueue results to additional queues:

```bash
tyger trigger create my-trigger \
  --codespec image-processor \
  --input-queue requests \
  --input-mapping raw_signal=input \
  --output-mapping reconstructed=output \
  --output-queue notifications:data=output
```

For multiple slot mappings, separate them with commas:

```bash
  --output-queue downstream:data=output,metadata=meta
```

If the output queue's input slots have the same names as the codespec's output
buffer parameters, the mapping can be omitted:

```bash
  --output-queue notifications
```

This trigger:
- Commits `reconstructed` as the output on the input queue item
- Enqueues a new item to the `notifications` queue with the `data` slot

When mapping to an output queue, all of that queue's required input slots must
be provided.

#### Trigger Behavior

When an item is submitted to a queue with an attached trigger:

1. Tyger creates a run with the trigger's codespec
2. Input buffers from the queue item are provided as arguments to the codespec's
   input buffer parameters (per input mapping)
3. Tyger creates output buffers for the codespec's output buffer parameters
4. When the run completes successfully:
   - Output buffers are committed to the queue item (per output mapping)
   - Items are enqueued to any output queues (per output queue mappings)
5. If the run fails, the item returns to pending (respecting retry limits)

The container is completely unaware of queues—it just reads from input pipes and
writes to output pipes.

#### Parameter Forwarding

Input parameters from the queue item are passed to the triggered run as
environment variables. Output parameters are not supported for triggers—only
buffer mappings are used for outputs.

#### Trigger Constraints

- Each queue can have at most one trigger attached
- A trigger watches exactly one input queue
- A trigger can write to zero or more output queues

> **Note:** When creating a trigger or service that processes a queue already
> consumed by another trigger or service, Tyger issues a warning. Multiple
> consumers are allowed but may lead to unexpected behavior.

### External Processing

Queues can also be consumed entirely outside of Tyger, using the CLI to interact
with queues. This is useful for integrating with existing systems.

## CLI Reference

### Queue Management

```bash
# Create a queue
tyger queue create <name> [options]
  --input <name>              # Input buffer slot (repeatable)
  --output <name>             # Output buffer slot (repeatable)
  --input-param <name>        # Input parameter (repeatable)
  --output-param <name>       # Output parameter (repeatable)
  --visibility-timeout <dur>  # Default visibility timeout
  --max-retries <n>           # Max retry attempts
  --item-ttl <dur>            # TTL for completed/failed items

# List queues
tyger queue list

# Show queue details (includes state: open, closed, completed)
tyger queue show <name>

# Show item counts by status
tyger queue counts <name>

# Close a queue (prevents new submissions)
tyger queue close <name>

# Delete a queue (cancels trigger runs; services will start failing)
tyger queue delete <name>
```

### Service Management

```bash
# Create a service
tyger service create <name> [options]
  --file SPEC.YAML            # Options specified as a YAML file
  --codespec <name>           # Service codespec
  --replicas <n>              # Number of replicas (default: 1)
  --queue key=value           # queue name mapping (repeatable)
  --node-pool <name>          # Node pool for the service

# List services
tyger service list

# Show service details
tyger service show <name>

# Scale a service
tyger service scale <name> --replicas <n>

# Restart a service (recreates all replicas)
tyger service restart <name>

# Delete a service
tyger service delete <name>
```

### Trigger Management

```bash
# Create a trigger
tyger trigger create <name> [options]
  --codespec <name>                    # Codespec to run (required)
  --input-queue <name>                 # Input queue (required)
  --concurrency <n>                    # Max concurrent runs (default: 1)
  --input-mapping <slot>=<param>       # Map queue slot to codespec buffer parameter
  --output-mapping <param>=<slot>      # Map codespec buffer parameter to output slot
  --output-queue <name>[:<slot>=<param>,...]   # Output queue with optional mapping (repeatable)

# List triggers
tyger trigger list

# Show trigger details
tyger trigger show <name>

# Scale a trigger (adjust max concurrent runs)
tyger trigger scale <name> --concurrency <n>

# Delete a trigger
tyger trigger delete <name>
```

### Submitting Items

```bash
# Submit an item
tyger queue submit <queue-name> [options]
  --input <slot>=<buffer-id>  # Provide input buffer (repeatable)
  --input-param <key>=<value> # Provide parameter (repeatable)
  --idempotency-key <key>     # Prevent duplicate submissions (returns existing item ID if duplicate)

# Example
item_id=$(tyger queue submit my-queue \
  --input raw_signal=$buf1 \
  --input calibration=$buf2 \
  --input-param priority=high \
  --idempotency-key "job-12345")
```

### Processing Items

```bash
# Receive an item for processing
tyger queue receive <queue-name> [options]
  --visibility-timeout <dur>  # Override default timeout
  --create-output-buffers     # Pre-create output buffers (deleted on timeout/fail)

# Response is a JSON object: {"status": "<queue-status>", "items": [...]}
# Each item includes: id, lease token, buffer IDs, and parameters
```

The response includes the queue's current status (`open`, `closed`, or
`completed`) and an `items` array containing zero or one items. This allows
processors to detect when a queue is completed and exit gracefully.

Each received item includes a lease token that must be provided for subsequent
operations on the item. This prevents race conditions where a slow processor
tries to commit an item that has already been reassigned.

```bash
# Extend visibility timeout (send heartbeat)
tyger queue item heartbeat <item-id> --lease <token> [--visibility-timeout <dur>]

# Commit with response
tyger queue item commit <item-id> --lease <token> \
  --output <slot>=<buffer-id> \
  --output-param <key>=<value>

# Mark as failed (no retry)
tyger queue item fail <item-id> --lease <token> --reason "..."

# Release back to queue (allow retry)
tyger queue item release <item-id> --lease <token>
```

### Watching for Responses

```bash
# Wait for item to complete (exits 0 on completed, non-zero on failed)
tyger queue item wait <item-id>

# Show item details (including response if completed)
tyger queue item show <item-id>
```

### Listing Items

```bash
# List items in a queue
tyger queue items list <queue-name> [options]
  --status pending|processing|completed|failed
  --limit <n>

# Purge items
tyger queue items purge <queue-name> --status failed
```

## Container Integration

Services and runs can interact with queues via the CLI.

### CLI Availability

The `tyger` CLI executable is mounted into service and run containers
automatically. The CLI is pre-authenticated using a cached token, so no login is
required.

### Heartbeat Pattern

For long-running item processing, the container should send heartbeats to
prevent visibility timeout. A convenience mode is provided:

```bash
# Run heartbeat in background until parent process exits,
# the item is committed, failed, or released, or the lease token expires.
tyger queue item heartbeat $item_id --lease $token --while-alive &
heartbeat_pid=$!

# Do the work...
./process.sh

# Commit and stop heartbeat
tyger queue item commit $item_id --lease $token --output result=$output_buf
kill $heartbeat_pid 2>/dev/null || true
```

The `--while-alive` flag causes the heartbeat command to exit when any of the
following occur:

- The parent process dies (ensures heartbeats stop if the service crashes)
- The item is committed, failed, or released
- The lease token expires

This ensures heartbeats are automatically cleaned up without requiring explicit
process management in the common case. The explicit `kill` is a belt-and-suspenders
safeguard.

## Request-Response Pattern

Queues support a request-response pattern where the submitter waits for a
response:

```bash
# Submit and capture item ID
item_id=$(tyger queue submit my-queue \
  --input raw_signal=$input_buf \
  --idempotency-key "request-$(date +%s)")

# Wait for completion (blocks until completed or failed)
tyger queue item wait $item_id

# Get the response
response=$(tyger queue item show $item_id)
output_buffer=$(echo "$response" | jq -r '.outputs.reconstructed')

# Read the result
tyger buffer read $output_buffer > result.dat
```

## Future Considerations

The following features are out of scope for the initial implementation but may
be added later:

- **Streaming receive**: `tyger queue watch` to stream items as they arrive
- **Batch receive**: Receive multiple items at once for batch processing
- **Wait on receive**: `tyger queue receive --wait` to block until an item is
  available
- **Service auto-scaling**: Automatically scale service replicas based on queue
  depth
- **Service health checks**: Automatic restart of unhealthy service replicas
- **Optional buffer slots**: Allow queue schemas to define optional input/output
  buffer slots that submitters/processors may omit
