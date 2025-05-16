#!/usr/bin/env python3

import io
import sys
import time

reader = io.BufferedReader(sys.stdin.buffer)
writer = io.BufferedWriter(sys.stdout.buffer)

buffer_size = 1024*1024

print("Copying stdin to stdout with a rate limit of 1MB/s...", file=sys.stderr)
count = 0
while True:
	start = time.time()
	chunk = reader.read(buffer_size)
	if not chunk:
		break
	writer.write(chunk)
	count += len(chunk)
	duration = time.time() - start
	if duration < 1:
		time.sleep(1 - duration)

writer.flush()
print(f"Finished! Wrote {count / buffer_size} MB", file=sys.stderr)
