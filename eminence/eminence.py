#!/usr/bin/env python3

import argparse
import os
import sys
from pathlib import Path
import random
import subprocess
from typing import List, Optional

from eminence_tools import get_dependency_image


def ensure_tyger_logged_in():
    try:
        subprocess.check_call(["tyger", "login", "status"])
    except FileNotFoundError:
        print("tyger CLI application not found.")
        sys.exit(1)
    except subprocess.CalledProcessError as exception:
        print("Run `tyger login` before running this script again.")
        sys.exit(exception.returncode)


def run_scanner(infile: Path, outfile: Path, input_uri: str, output_uri: str, session_id: Optional[str] = None, verbose=False):
    image = get_dependency_image("scanner")

    infile_filename = infile.name
    outfile_filename = outfile.name

    infile_folder_str = str(infile.parent.absolute())
    outfile_folder_str = str(outfile.parent.absolute())

    # If we are in devcontainer, we need to get the mounts right
    if "HOST_WORKSPACE_DIR" in os.environ and "CONTAINER_WORKSPACE_DIR" in os.environ:
        infile_folder_str = infile_folder_str.replace(
            os.environ["CONTAINER_WORKSPACE_DIR"],
            os.environ["HOST_WORKSPACE_DIR"])

        outfile_folder_str = outfile_folder_str.replace(
            os.environ["CONTAINER_WORKSPACE_DIR"],
            os.environ["HOST_WORKSPACE_DIR"])

    scanner_cmd = [
        "docker", "run",
        "--network=host",
        "-v", f"{infile_folder_str}:/in",
        "-v", f"{outfile_folder_str}:/out",
        "-u", f"{os.getuid()}:{os.getgid()}",
        image,
        "--input-file", f"/in/{infile_filename}",
        "--input-buffer", input_uri,
        "--output-file", f"/out/{outfile_filename}",
        "--output-buffer", output_uri,
    ]

    if session_id and len(session_id) > 0:
        scanner_cmd = scanner_cmd + ["--session-id", session_id]

    subprocess.check_call(scanner_cmd)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(
        description='Eminence image reconstruction orchestration')

    parser.add_argument('-f', '--input_file', type=Path, help='Input file', required=True)
    parser.add_argument('-o', '--output_file', type=Path, help='Output file', default=Path("out.h5"))
    parser.add_argument('-i', '--image', type=str, help='Docker image to run', default=get_dependency_image("recon"))
    parser.add_argument('-s', '--session_id', type=str, help='Session ID to pass to scanner process')
    parser.add_argument('--cpu', type=str, help='CPU cores needed')
    parser.add_argument('--memory', type=str, help='memory bytes needed')
    parser.add_argument('--gpu', type=str, help='GPUs needed')
    parser.add_argument('--cluster', type=str, help='The name of the cluster to execute in')
    parser.add_argument('--node-pool', type=str, help='The name of the node pool to execute in')
    parser.add_argument('-t', '--timeout', type=str, help='How log before the run times out. Specified in a sequence of decimal numbers, each with optional fraction and a unit suffix, such as "300s", "1.5h" or "2h45m". Valid time units are "s", "m", "h')
    parser.add_argument('-v', '--verbose', action=argparse.BooleanOptionalAction, help='Verbose output')
    parser.add_argument('recon_args', nargs='*', help='Additional arguments passed to the reconstruction proccess')

    args = parser.parse_args()

    verbose_arg = ["--verbose"] if args.verbose else []

    def run_tyger(arguments: List[str], timeout_seconds=20):
        try:
            out = subprocess.check_output(["tyger"] + verbose_arg + arguments, timeout=timeout_seconds, universal_newlines=True)
            return out.strip()
        except subprocess.CalledProcessError:
            print("Unable to call tyger CLI")
            raise

    ensure_tyger_logged_in()

    input_buffer_id = run_tyger(["create", "buffer"])
    input_buffer_sas_uri = run_tyger(
        ["access", "buffer", input_buffer_id, "-w"])

    output_buffer_id = run_tyger(["create", "buffer"])
    output_buffer_sas_uri = run_tyger(["access", "buffer", output_buffer_id])

    codespec_name = f"eminence-codespec-{random.randint(0,1000)}"

    tyger_cmd = (
        [
            "create", "codespec", codespec_name,
            "-i=input", "-o=output",
            "--image", args.image
        ]
        + (["--cpu", args.cpu] if args.cpu else [])
        + (["--memory", args.memory] if args.memory else [])
        + (["--gpu", args.gpu] if args.gpu else [])
    )

    if args.cpu:
        tyger_cmd += ["--cpu", args.cpu]
    if args.memory:
        tyger_cmd += ["--memory", args.memory]
    if args.gpu:
        tyger_cmd += ["--gpu", args.gpu]

    tyger_cmd += [
        "--",
        "-r", "$(INPUT_BUFFER_URI_FILE)", "-w", "$(OUTPUT_BUFFER_URI_FILE)"
    ]

    if args.recon_args:
        tyger_cmd = tyger_cmd + args.recon_args

    code_spec_version = run_tyger(tyger_cmd)

    run_id = run_tyger(
        [
            "create",
            "run", "--codespec", codespec_name,
            "-b", f"input={input_buffer_id}",
            "-b", f"output={output_buffer_id}"
        ]
        + (["--cluster", args.cluster] if args.cluster else [])
        + (["--node-pool", args.node_pool] if args.node_pool else [])
        + (["--timeout", args.timeout] if args.timeout else []))

    print(f"CodeSpec: {codespec_name} - {code_spec_version}")
    print(f"RunId: {run_id}")

    run_scanner(args.input_file, args.output_file,
                input_buffer_sas_uri, output_buffer_sas_uri,
                args.session_id, args.verbose)
